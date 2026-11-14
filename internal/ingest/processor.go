// Package ingest превращает поток событий детектора в сессии, удары и падения.
//
// Обработка идемпотентна: каждое событие сначала записывается в сырой поток с
// отпечатком содержимого, и производные записи строятся только для события,
// которого там ещё не было. Поэтому пачку можно безопасно прислать повторно —
// целиком или с любого места.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vbif322/cue-vision/internal/event"
	"github.com/Vbif322/cue-vision/internal/store"
)

// Options — настройки обработки.
type Options struct {
	// AutoTraining — удар вне открытой вручную тренировки заводит автоматическую,
	// живущую до конца сессии.
	AutoTraining bool
	// SkipTypes — типы событий, которые не сохраняются (по умолчанию покадровые positions).
	SkipTypes map[string]bool
}

// Request — пачка событий одного потока в порядке получения.
type Request struct {
	// Stream — имя потока: один экземпляр детектора над одним столом.
	Stream string
	// Realtime — события приходят в темпе игры (WebSocket), а не загружаются файлом.
	Realtime bool
	Events   []event.Event
}

// Result — итог приёма пачки.
type Result struct {
	Received   int               `json:"received"`
	Stored     int               `json:"stored"`
	Duplicates int               `json:"duplicates"`
	Skipped    int               `json:"skipped"`
	Invalid    int               `json:"invalid"`
	Sessions   []int64           `json:"sessions"`
	Errors     []event.LineError `json:"errors,omitempty"`
}

// MaxReportedErrors — сколько замечаний по отдельным событиям попадает в ответ.
const MaxReportedErrors = 20

// Processor принимает события и ведёт производные таблицы.
type Processor struct {
	pool *pgxpool.Pool
	opt  Options
	log  *slog.Logger
	now  func() time.Time
}

// New создаёт обработчик.
func New(pool *pgxpool.Pool, opt Options, log *slog.Logger) *Processor {
	return &Processor{pool: pool, opt: opt, log: log, now: time.Now}
}

// Ingest принимает пачку одной транзакцией: либо вся пачка учтена, либо ничего.
func (p *Processor) Ingest(ctx context.Context, req Request) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		res = Result{Received: len(req.Events), Sessions: []int64{}}
		if err := store.LockStream(ctx, tx, req.Stream); err != nil {
			return err
		}
		r := &run{
			p:        p,
			tx:       tx,
			stream:   req.Stream,
			realtime: req.Realtime,
			now:      p.now().UTC().Truncate(time.Microsecond),
			res:      &res,
			touched:  map[int64]bool{},
		}
		for _, ev := range req.Events {
			if err := r.handle(ctx, ev); err != nil {
				return fmt.Errorf("событие %s (t=%.2f): %w", ev.Type, ev.T, err)
			}
		}
		return r.finish(ctx)
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// flushSize — сколько событий без производных записей копится до отправки одним пакетом.
const flushSize = 256

// shotScoped — события, принадлежащие удару, внутри которого они пришли.
var shotScoped = map[string]bool{
	event.CueStrike:     true,
	event.BallStart:     true,
	event.BallStop:      true,
	event.BallCollision: true,
	event.CushionHit:    true,
	event.BallPocketed:  true,
	event.PocketRattle:  true,
	event.ShotEnd:       true,
}

// derived — события, по которым строятся производные записи. Они вставляются по
// одному, чтобы знать, новое ли событие; остальные уходят пакетами.
var derived = map[string]bool{
	event.SessionStart:      true,
	event.SessionEnd:        true,
	event.TableCalibrated:   true,
	event.TableRecalibrated: true,
	event.BallsInitialized:  true,
	event.ShotStart:         true,
	event.CueStrike:         true,
	event.BallPocketed:      true,
	event.ShotEnd:           true,
}

type run struct {
	p        *Processor
	tx       pgx.Tx
	stream   string
	realtime bool
	now      time.Time
	res      *Result
	sess     *sessionState
	pending  []pendingEvent
	touched  map[int64]bool
}

type sessionState struct {
	id     int64
	origin time.Time
	// game — игра из session_start; от неё зависит, какой шар считать битком.
	game  string
	lastT float64
	dirty bool
	shot  *shotState
}

type shotState struct {
	id      int64
	no      int
	tStart  float64
	cueBall string
	hasCue  bool
}

type pendingEvent struct {
	sessionID int64
	shotID    *int64
	at        time.Time
	hash      []byte
	ev        event.Event
}

func (r *run) handle(ctx context.Context, ev event.Event) error {
	if r.p.opt.SkipTypes[ev.Type] {
		r.res.Skipped++
		return nil
	}
	hash, err := ev.Hash()
	if err != nil {
		r.res.Invalid++
		r.note(ev, "событие не разобрано: "+err.Error())
		return nil
	}

	switch {
	case ev.Type == event.SessionStart:
		if err := r.beginSession(ctx, ev); err != nil {
			return err
		}
	case r.sess == nil:
		if err := r.attachSession(ctx, ev); err != nil {
			return err
		}
	}
	s := r.sess
	if ev.T > s.lastT {
		s.lastT = ev.T
		s.dirty = true
	}
	pe := pendingEvent{sessionID: s.id, shotID: r.shotLink(ev), at: s.at(ev.T), hash: hash, ev: ev}

	if !derived[ev.Type] {
		r.pending = append(r.pending, pe)
		if len(r.pending) >= flushSize {
			return r.flush(ctx)
		}
		return nil
	}

	if err := r.flush(ctx); err != nil {
		return err
	}
	var eventID int64
	err = r.tx.QueryRow(ctx, insertEventSQL+" RETURNING id", pe.args()...).Scan(&eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		r.res.Duplicates++
		return nil
	}
	if err != nil {
		return err
	}
	r.res.Stored++

	switch ev.Type {
	case event.SessionEnd:
		return r.onSessionEnd(ctx, ev)
	case event.TableCalibrated, event.TableRecalibrated:
		_, err = r.tx.Exec(ctx, `UPDATE sessions SET calibration = $2 WHERE id = $1`, s.id, string(ev.Raw))
		return err
	case event.BallsInitialized:
		return r.onBallsInitialized(ctx, ev)
	case event.ShotStart:
		return r.onShotStart(ctx, ev, eventID)
	case event.CueStrike:
		return r.onCueStrike(ctx, ev)
	case event.BallPocketed:
		return r.onBallPocketed(ctx, ev, eventID)
	case event.ShotEnd:
		return r.onShotEnd(ctx, ev, eventID)
	}
	return nil
}

const insertEventSQL = `
INSERT INTO events (session_id, shot_id, t, frame, type, immediate, conf, occurred_at, hash, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (session_id, hash) DO NOTHING`

func (pe pendingEvent) args() []any {
	return []any{pe.sessionID, pe.shotID, pe.ev.T, pe.ev.Frame, pe.ev.Type, pe.ev.Immediate, pe.ev.Conf,
		pe.at, pe.hash, string(pe.ev.Raw)}
}

func (r *run) flush(ctx context.Context) error {
	if len(r.pending) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, pe := range r.pending {
		batch.Queue(insertEventSQL, pe.args()...)
	}
	br := r.tx.SendBatch(ctx, batch)
	for range r.pending {
		tag, err := br.Exec()
		if err != nil {
			_ = br.Close()
			return err
		}
		if tag.RowsAffected() == 1 {
			r.res.Stored++
		} else {
			r.res.Duplicates++
		}
	}
	r.pending = r.pending[:0]
	return br.Close()
}

func (r *run) finish(ctx context.Context) error {
	if err := r.flush(ctx); err != nil {
		return err
	}
	return r.saveSession(ctx)
}

// note добавляет замечание по событию в ответ; само событие при этом сохраняется.
func (r *run) note(ev event.Event, msg string) {
	r.p.log.Warn("событие принято с замечанием", "stream", r.stream, "type", ev.Type, "t", ev.T, "note", msg)
	if len(r.res.Errors) < MaxReportedErrors {
		r.res.Errors = append(r.res.Errors, event.LineError{Line: ev.Line, Error: msg})
	}
}

func (r *run) touch(sessionID int64) {
	if !r.touched[sessionID] {
		r.touched[sessionID] = true
		r.res.Sessions = append(r.res.Sessions, sessionID)
	}
}

func (s *sessionState) at(t float64) time.Time {
	return s.origin.Add(time.Duration(t * float64(time.Second)))
}

func (r *run) shotLink(ev event.Event) *int64 {
	if ev.Immediate || !shotScoped[ev.Type] || r.sess.shot == nil {
		return nil
	}
	id := r.sess.shot.id
	return &id
}

// --- сессии ---

// beginSession находит или заводит сессию по session_start и делает её текущей.
func (r *run) beginSession(ctx context.Context, ev event.Event) error {
	if err := r.flush(ctx); err != nil {
		return err
	}
	if err := r.saveSession(ctx); err != nil {
		return err
	}
	var d event.SessionStartData
	if err := ev.Decode(&d); err != nil {
		r.note(ev, "поля session_start не разобраны: "+err.Error())
		d = event.SessionStartData{}
	}

	// Начало шкалы времени. По контракту это started_at детектора; он же отличает
	// повторную загрузку той же сессии от новой. Исключения — поток без
	// started_at и replay в реальном времени: запись проигрывается сейчас, а
	// started_at в ней остался от исходного прогона.
	origin := r.now.Add(-time.Duration(ev.T * float64(time.Second)))
	r.now = r.now.Add(time.Microsecond)
	var startedAt *time.Time
	if t, err := time.Parse(time.RFC3339, d.StartedAt); err == nil {
		t = t.UTC().Truncate(time.Microsecond)
		startedAt = &t
		if !(r.realtime && d.SourceKind == "replay") {
			origin = t
		}
	}

	var id int64
	err := r.tx.QueryRow(ctx,
		`SELECT id FROM sessions WHERE stream = $1 AND origin_at = $2`, r.stream, origin).Scan(&id)
	if err == nil {
		return r.loadSession(ctx, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if err := r.interruptActive(ctx); err != nil {
		return err
	}
	err = r.tx.QueryRow(ctx, `
		INSERT INTO sessions (stream, origin_at, started_at, source, source_kind, hall, table_name,
		                      game, fps, schema_version, tool)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		r.stream, origin, startedAt, nullable(d.Source), nullable(d.SourceKind), nullable(d.Hall),
		nullable(d.Table), nullable(d.Game), d.FPS, nullable(d.Schema), nullable(d.Tool)).Scan(&id)
	if err != nil {
		return err
	}
	r.sess = &sessionState{id: id, origin: origin, game: d.Game}
	r.touch(id)
	return nil
}

// attachSession выбирает сессию для события, пришедшего без session_start в этой
// пачке: активную сессию потока, а если её нет — заводит неявную.
func (r *run) attachSession(ctx context.Context, ev event.Event) error {
	var id int64
	err := r.tx.QueryRow(ctx,
		`SELECT id FROM sessions WHERE stream = $1 AND status = 'active'`, r.stream).Scan(&id)
	if err == nil {
		return r.loadSession(ctx, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	origin := r.now.Add(-time.Duration(ev.T * float64(time.Second)))
	r.now = r.now.Add(time.Microsecond)
	err = r.tx.QueryRow(ctx, `
		INSERT INTO sessions (stream, origin_at, implicit, source_kind)
		VALUES ($1, $2, true, 'unknown') RETURNING id`, r.stream, origin).Scan(&id)
	if err != nil {
		return err
	}
	r.sess = &sessionState{id: id, origin: origin}
	r.touch(id)
	return nil
}

func (r *run) loadSession(ctx context.Context, id int64) error {
	s := &sessionState{id: id}
	if err := r.tx.QueryRow(ctx,
		`SELECT origin_at, last_t, coalesce(game, '') FROM sessions WHERE id = $1`, id).Scan(&s.origin, &s.lastT, &s.game); err != nil {
		return err
	}
	shot := &shotState{}
	err := r.tx.QueryRow(ctx, `
		SELECT id, shot_no, t_start, coalesce(cue_ball, ''), cue_ball IS NOT NULL
		  FROM shots WHERE session_id = $1 AND status = 'open'
		 ORDER BY id DESC LIMIT 1`, id).Scan(&shot.id, &shot.no, &shot.tStart, &shot.cueBall, &shot.hasCue)
	switch {
	case err == nil:
		s.shot = shot
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	r.sess = s
	r.touch(id)
	return nil
}

func (r *run) saveSession(ctx context.Context) error {
	s := r.sess
	if s == nil || !s.dirty {
		return nil
	}
	s.dirty = false
	_, err := r.tx.Exec(ctx, `UPDATE sessions SET last_t = GREATEST(last_t, $2) WHERE id = $1`, s.id, s.lastT)
	return err
}

// interruptActive закрывает сессию потока, оставшуюся без session_end: детектор
// перезапустился, и новая сессия начинается с новой шапки (EVENTS.md §7).
func (r *run) interruptActive(ctx context.Context) error {
	var id int64
	err := r.tx.QueryRow(ctx,
		`SELECT id FROM sessions WHERE stream = $1 AND status = 'active'`, r.stream).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = store.CloseSession(ctx, r.tx, id, store.SessionInterrupted, 0)
	return err
}

func (r *run) onSessionEnd(ctx context.Context, ev event.Event) error {
	s := r.sess
	if _, err := r.tx.Exec(ctx,
		`UPDATE sessions SET summary = $2 WHERE id = $1`, s.id, string(ev.Raw)); err != nil {
		return err
	}
	if _, err := store.CloseSession(ctx, r.tx, s.id, store.SessionEnded, s.lastT); err != nil {
		return err
	}
	// Всё, что придёт на поток дальше без шапки, — уже другая сессия.
	r.sess = nil
	return nil
}

func (r *run) onBallsInitialized(ctx context.Context, ev event.Event) error {
	var d event.BallsInitializedData
	if err := ev.Decode(&d); err != nil {
		r.note(ev, "поля balls_initialized не разобраны: "+err.Error())
		return nil
	}
	// Неразобранные слипшиеся шары тоже на столе: раскладка без них — нижняя граница.
	_, err := r.tx.Exec(ctx,
		`UPDATE sessions SET ball_count = $2 WHERE id = $1`, r.sess.id, len(d.Balls)+d.Unresolved)
	return err
}

// --- удары ---

func (r *run) onShotStart(ctx context.Context, ev event.Event, eventID int64) error {
	var d event.ShotStartData
	if err := ev.Decode(&d); err != nil {
		r.note(ev, "поля shot_start не разобраны: "+err.Error())
		return nil
	}
	if err := r.abortOpenShot(ctx); err != nil {
		return err
	}
	shotID, err := r.insertShot(ctx, d.Shot, ev.T, eventID)
	if err != nil {
		return err
	}
	r.sess.shot = &shotState{id: shotID, no: d.Shot, tStart: ev.T}
	return nil
}

// insertShot заводит удар, начавшийся в момент tStart, и привязывает к нему событие.
func (r *run) insertShot(ctx context.Context, shotNo int, tStart float64, eventID int64) (int64, error) {
	s := r.sess
	at := s.at(tStart)
	trainingID, err := r.resolveTraining(ctx, at)
	if err != nil {
		return 0, err
	}
	var shotID int64
	err = r.tx.QueryRow(ctx, `
		INSERT INTO shots (session_id, training_id, shot_no, t_start, started_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, s.id, trainingID, shotNo, tStart, at).Scan(&shotID)
	if err != nil {
		return 0, err
	}
	_, err = r.tx.Exec(ctx, `UPDATE events SET shot_id = $1 WHERE id = $2`, shotID, eventID)
	return shotID, err
}

// abortOpenShot закрывает удар, не дождавшийся shot_end: в статистику он не идёт.
func (r *run) abortOpenShot(ctx context.Context) error {
	s := r.sess
	if s.shot == nil {
		return nil
	}
	_, err := r.tx.Exec(ctx, `UPDATE shots SET status = 'aborted' WHERE id = $1`, s.shot.id)
	s.shot = nil
	return err
}

// resolveTraining находит тренировку, в окно которой попадает момент at. Открытая
// вручную тренировка важнее автоматической; если нет ни той, ни другой и
// автоматические тренировки включены, заводится новая — до конца сессии.
func (r *run) resolveTraining(ctx context.Context, at time.Time) (*int64, error) {
	var id int64
	err := r.tx.QueryRow(ctx, `
		SELECT id FROM trainings
		 WHERE stream = $1 AND started_at <= $2 AND (ended_at IS NULL OR ended_at > $2)
		   AND (origin = 'manual' OR session_id = $3)
		 ORDER BY (origin = 'manual') DESC, started_at DESC
		 LIMIT 1`, r.stream, at, r.sess.id).Scan(&id)
	if err == nil {
		return &id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if !r.p.opt.AutoTraining {
		return nil, nil
	}
	err = r.tx.QueryRow(ctx, `
		INSERT INTO trainings (stream, session_id, origin, started_at)
		VALUES ($1, $2, 'auto', $3) RETURNING id`, r.stream, r.sess.id, at).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (r *run) onCueStrike(ctx context.Context, ev event.Event) error {
	shot := r.sess.shot
	// Удар открывает один cue_strike; повторный внутри того же удара его не переписывает.
	if shot == nil || shot.hasCue || ev.Immediate {
		return nil
	}
	var d event.CueStrikeData
	if err := ev.Decode(&d); err != nil {
		r.note(ev, "поля cue_strike не разобраны: "+err.Error())
		return nil
	}
	_, err := r.tx.Exec(ctx, `
		UPDATE shots
		   SET cue_ball = $2, cue_x = $3, cue_y = $4, cue_direction = $5, cue_speed = $6, cue_source = $7
		 WHERE id = $1`, shot.id, nullable(d.Ball), d.X, d.Y, d.Direction, d.Speed, nullable(d.Source))
	if err != nil {
		return err
	}
	shot.cueBall, shot.hasCue = d.Ball, true
	return nil
}

func (r *run) onBallPocketed(ctx context.Context, ev event.Event, eventID int64) error {
	var d event.BallPocketedData
	if err := ev.Decode(&d); err != nil {
		r.note(ev, "поля ball_pocketed не разобраны: "+err.Error())
		return nil
	}
	if d.Ball == "" {
		r.note(ev, "ball_pocketed без шара")
		return nil
	}
	s := r.sess
	var (
		shotID  *int64
		cueBall string
	)
	if s.shot != nil {
		shotID, cueBall = &s.shot.id, s.shot.cueBall
	} else {
		// Падение подтверждается дольше остальных событий и может прийти, когда
		// удар уже закрыт: ищем удар, в интервал которого оно попадает.
		var id int64
		err := r.tx.QueryRow(ctx, `
			SELECT id, coalesce(cue_ball, '') FROM shots
			 WHERE session_id = $1 AND status <> 'aborted' AND t_start <= $2 AND (t_end IS NULL OR t_end >= $2)
			 ORDER BY t_start DESC LIMIT 1`, s.id, ev.T).Scan(&id, &cueBall)
		switch {
		case err == nil:
			shotID = &id
			if _, err := r.tx.Exec(ctx, `UPDATE events SET shot_id = $1 WHERE id = $2`, id, eventID); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
	}
	_, err := r.tx.Exec(ctx, `
		INSERT INTO pots (session_id, shot_id, ball, pocket, entry_speed, is_cue, t, occurred_at, conf)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.id, shotID, d.Ball, nullable(d.Pocket), d.EntrySpeed, IsCueBall(d.Ball, CueBallFor(s.game, cueBall)), ev.T, s.at(ev.T), ev.Conf)
	return err
}

func (r *run) onShotEnd(ctx context.Context, ev event.Event, eventID int64) error {
	var d event.ShotEndData
	if err := ev.Decode(&d); err != nil {
		r.note(ev, "поля shot_end не разобраны: "+err.Error())
		return nil
	}
	s := r.sess
	// Открытый удар с другим номером — не тот, что заканчивается этим событием.
	if s.shot != nil && d.Shot != 0 && s.shot.no != 0 && s.shot.no != d.Shot {
		if err := r.abortOpenShot(ctx); err != nil {
			return err
		}
	}
	shot := s.shot
	// Удар, собранный по сводке, без своих событий: искать в них белый бессмысленно.
	synthesized := shot == nil
	if shot == nil {
		// shot_start не видели (подключились посреди удара): строим удар по сводке.
		tStart := ev.T
		if d.Duration != nil && *d.Duration > 0 && *d.Duration <= ev.T {
			tStart = ev.T - *d.Duration
		}
		shotID, err := r.insertShot(ctx, d.Shot, tStart, eventID)
		if err != nil {
			return err
		}
		shot = &shotState{id: shotID, no: d.Shot, tStart: tStart}
	}

	facts := ShotFacts{CueBall: CueBallFor(s.game, shot.cueBall), FirstContact: d.FirstContact, Pocketed: d.Pocketed}
	duration := ev.T - shot.tStart
	if d.Duration != nil {
		duration = *d.Duration
	}
	var (
		geo  event.Geometry
		miss event.NearMiss
	)
	if d.Geometry != nil {
		geo = *d.Geometry
	}
	if d.NearMiss != nil {
		miss = *d.NearMiss
	}

	// Детектор открыл удар по цветному шару, а биток в снукере — белый. Если белый
	// так и не тронулся, это не удар игрока; если тронулся — удар пересчитывается
	// от белого, а геометрия и близкий промах детектора, посчитанные от чужого
	// битка, отбрасываются.
	if shot.cueBall != "" && facts.CueBall == "" && s.game == GameSnooker && !synthesized {
		white, err := r.findWhiteMotion(ctx, shot.id)
		if err != nil {
			return err
		}
		if white == nil {
			return r.rejectShot(ctx, shot.id, ev.T, duration, d,
				"биток не двигался: удар открыт по "+shot.cueBall)
		}
		if err := r.reassignCueBall(ctx, shot.id, white); err != nil {
			return err
		}
		facts.CueBall, facts.FirstContact = white.Ball, white.FirstContact
		geo, miss = event.Geometry{}, event.NearMiss{}
	}
	if d.DeclaredTarget != nil {
		facts.TargetBall, facts.TargetPocket = d.DeclaredTarget.Ball, d.DeclaredTarget.Pocket
	}
	if facts.TargetPocket != "" {
		pocketOf, err := r.shotPockets(ctx, shot.id)
		if err != nil {
			return err
		}
		facts.PocketOf = pocketOf
	}
	v := Classify(facts)

	_, err := r.tx.Exec(ctx, `
		UPDATE shots
		   SET status = 'closed', t_end = $2, duration = $3, first_contact = $4, contacts = $5,
		       cushions = $6, pocketed = $7, object_potted = $8, cue_potted = $9,
		       cut_angle = $10, cue_to_object = $11, object_to_pocket = $12, obstructed = $13,
		       near_miss_ball = $14, near_miss_pocket = $15, near_miss_distance = $16,
		       near_miss_side = $17, near_miss_conf = $18,
		       target_ball = $19, target_pocket = $20, outcome = $21, success = $22, conf = $23
		 WHERE id = $1`,
		shot.id, ev.T, duration, nullable(facts.FirstContact), nonNil(d.Contacts),
		d.Cushions, nonNil(d.Pocketed), v.ObjectPotted, v.CuePotted,
		geo.CutAngle, geo.CueToObject, geo.ObjectToPocket, geo.Obstructed,
		nullable(miss.Ball), nullable(miss.Pocket), miss.Distance, nullable(miss.Side), miss.Conf,
		nullable(facts.TargetBall), nullable(facts.TargetPocket), v.Outcome, v.Success, ev.Conf)
	if err != nil {
		return err
	}
	s.shot = nil
	return nil
}

// rejectShot закрывает удар, не признанный игровым: в статистику он не идёт, но
// сводка детектора и причина остаются для разбора.
func (r *run) rejectShot(ctx context.Context, shotID int64, tEnd, duration float64,
	d event.ShotEndData, reason string) error {
	_, err := r.tx.Exec(ctx, `
		UPDATE shots
		   SET status = 'rejected', reject_reason = $2, t_end = $3, duration = $4,
		       first_contact = $5, contacts = $6, cushions = $7, pocketed = $8
		 WHERE id = $1`,
		shotID, reason, tEnd, duration, nullable(d.FirstContact), nonNil(d.Contacts), d.Cushions, nonNil(d.Pocketed))
	r.sess.shot = nil
	return err
}

// reassignCueBall делает битком удара белый: место и скорость берутся из его
// собственного старта, направление неизвестно.
func (r *run) reassignCueBall(ctx context.Context, shotID int64, w *whiteMotion) error {
	source := ""
	if w.Speed != nil {
		source = "ball_start"
	}
	_, err := r.tx.Exec(ctx, `
		UPDATE shots
		   SET cue_ball = $2, cue_x = $3, cue_y = $4, cue_direction = NULL, cue_speed = $5, cue_source = $6
		 WHERE id = $1`, shotID, w.Ball, w.X, w.Y, w.Speed, nullable(source))
	return err
}

func (r *run) shotPockets(ctx context.Context, shotID int64) (map[string]string, error) {
	rows, err := r.tx.Query(ctx,
		`SELECT ball, pocket FROM pots WHERE shot_id = $1 AND pocket IS NOT NULL ORDER BY t`, shotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ball, pocket string
		if err := rows.Scan(&ball, &pocket); err != nil {
			return nil, err
		}
		out[ball] = pocket
	}
	return out, rows.Err()
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
