package ingest_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Vbif322/cue-vision/internal/event"
	"github.com/Vbif322/cue-vision/internal/ingest"
	"github.com/Vbif322/cue-vision/internal/store"
	"github.com/Vbif322/cue-vision/internal/testdb"
)

// Запись прогона детектора: 100 с, 5 ударов, шапка и session_end на месте.
const sessionFile = "../../testdata/session_100s.jsonl"

// Запись без session_start и session_end: 7 ударов.
const headlessFile = "../../testdata/bench_6reds_noheader.jsonl"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newProcessor(st *store.Store, auto bool) *ingest.Processor {
	return ingest.New(st.Pool(), ingest.Options{
		AutoTraining: auto,
		SkipTypes:    map[string]bool{event.Positions: true},
	}, quiet)
}

func readFile(t *testing.T, path string) []event.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	events, bad, err := event.ReadAll(f)
	if err != nil || len(bad) != 0 {
		t.Fatalf("чтение %s: err=%v bad=%v", path, err, bad)
	}
	return events
}

func parse(t *testing.T, lines ...string) []event.Event {
	t.Helper()
	events, bad, err := event.ReadAll(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil || len(bad) != 0 {
		t.Fatalf("разбор событий: err=%v bad=%v", err, bad)
	}
	return events
}

func mustIngest(t *testing.T, p *ingest.Processor, req ingest.Request) ingest.Result {
	t.Helper()
	res, err := p.Ingest(context.Background(), req)
	if err != nil {
		t.Fatalf("приём событий: %v", err)
	}
	return res
}

func onlySession(t *testing.T, st *store.Store, stream string) store.Session {
	t.Helper()
	sessions, err := st.ListSessions(context.Background(), store.SessionFilter{Stream: stream, Page: store.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("на потоке %q сессий: %d, ожидалась одна", stream, len(sessions))
	}
	return sessions[0]
}

// checkSessionStats сверяет статистику записи session_100s с разбором по кадрам:
// удар 1 — красный мимо левой нижней лузы, 2 — синий в tm, 3 — красный в tl,
// 4 — промах, 5 — белый (размытый, детектор его упустил) забивает жёлтый в bm
// около 01:36.2. Детектор счёл битком жёлтый; в снукере биток всегда белый, и
// сервис пересчитывает удар от white_02.
func checkSessionStats(t *testing.T, st store.Stats) {
	t.Helper()
	if st.Shots != 5 || st.Pots != 3 || st.Misses != 2 || st.InOffs != 0 || st.NoContact != 0 {
		t.Errorf("исходы: shots=%d pots=%d misses=%d in_offs=%d no_contact=%d",
			st.Shots, st.Pots, st.Misses, st.InOffs, st.NoContact)
	}
	if st.PotRate == nil || *st.PotRate != 0.6 {
		t.Errorf("pot_rate = %v, ожидалось 0.6", st.PotRate)
	}
	if st.BallsPotted != 3 || st.BestStreak != 2 {
		t.Errorf("balls_potted=%d best_streak=%d, ожидалось 3 и 2", st.BallsPotted, st.BestStreak)
	}
	if st.NearMisses != 1 || st.NearMissRight != 1 || st.NearMissLeft != 0 {
		t.Errorf("близкие промахи: всего=%d справа=%d слева=%d", st.NearMisses, st.NearMissRight, st.NearMissLeft)
	}
	// Скорость удара 5 — собственный старт white_02 (0.502), а не жёлтого (0.961).
	if st.AvgCueSpeed == nil || *st.AvgCueSpeed != 0.4282 {
		t.Errorf("avg_cue_speed = %v, ожидалось 0.4282", st.AvgCueSpeed)
	}
	pockets := map[string]store.PocketStat{}
	for _, p := range st.ByPocket {
		pockets[p.Pocket] = p
	}
	if pockets["tm"].Pots != 1 || pockets["tl"].Pots != 1 || pockets["bm"].Pots != 1 || pockets["bl"].NearMisses != 1 {
		t.Errorf("по лузам: %+v", st.ByPocket)
	}
	balls := map[string]int64{}
	for _, b := range st.ByBall {
		balls[b.Ball] = b.Pots
	}
	if balls["blue"] != 1 || balls["red"] != 1 || balls["yellow"] != 1 || len(balls) != 3 {
		t.Errorf("по цветам: %+v", st.ByBall)
	}
	// Углы резки ударов 1–4: 9.2, 10.1, 56.4, 60.5; забиты второй и третий.
	want := map[string][2]int64{"0-15": {2, 1}, "45-60": {1, 1}, "60-90": {1, 0}}
	for _, b := range st.ByCutAngle {
		if w := want[b.Label]; b.Shots != w[0] || b.Pots != w[1] {
			t.Errorf("угол %s: shots=%d pots=%d, ожидалось %v", b.Label, b.Shots, b.Pots, w)
		}
	}
}

func TestIngestSessionFile(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)
	events := readFile(t, sessionFile)

	res := mustIngest(t, p, ingest.Request{Stream: "t1", Events: events})
	if res.Received != len(events) || res.Stored != len(events) || res.Duplicates != 0 || len(res.Sessions) != 1 {
		t.Fatalf("итог приёма: %+v при %d событиях", res, len(events))
	}

	listed := onlySession(t, st, "t1")
	if listed.Calibration != nil || listed.Summary != nil {
		t.Errorf("список сессий не должен нести калибровку и итог прогона")
	}
	sess, err := st.GetSession(ctx, listed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != store.SessionEnded || sess.Implicit || sess.Shots != 5 {
		t.Errorf("сессия: status=%s implicit=%v shots=%d", sess.Status, sess.Implicit, sess.Shots)
	}
	if sess.Hall == nil || *sess.Hall != "testsoun1" || sess.BallCount == nil || *sess.BallCount != 21 {
		t.Errorf("шапка сессии: hall=%v ball_count=%v", sess.Hall, sess.BallCount)
	}
	if sess.Duration != 99.96 || sess.Calibration == nil || sess.Summary == nil {
		t.Errorf("duration=%v calibration=%s summary=%s", sess.Duration, sess.Calibration, sess.Summary)
	}
	wantStart := time.Date(2026, 11, 8, 6, 8, 39, 0, time.UTC)
	if !sess.OriginAt.Equal(wantStart) || sess.EndedAt == nil ||
		!sess.EndedAt.Equal(wantStart.Add(99960*time.Millisecond)) {
		t.Errorf("время сессии: origin=%v ended=%v", sess.OriginAt, sess.EndedAt)
	}

	stats, err := st.Stats(ctx, store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	checkSessionStats(t, stats)

	// Удары вне открытой вручную тренировки собраны в одну автоматическую,
	// и она закрылась вместе с сессией.
	trainings, err := st.ListTrainings(ctx, store.TrainingFilter{Stream: "t1", Page: store.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(trainings) != 1 {
		t.Fatalf("тренировок: %d, ожидалась одна", len(trainings))
	}
	tr := trainings[0]
	if tr.Origin != store.TrainingAuto || tr.Status != "finished" || tr.Shots != 5 || tr.Pots != 3 {
		t.Errorf("автоматическая тренировка: %+v", tr)
	}
	trStats, err := st.Stats(ctx, store.TrainingScope(tr.ID))
	if err != nil {
		t.Fatal(err)
	}
	checkSessionStats(t, trStats)

	// Удар хранит биток, геометрию и события, из которых сложился.
	shots, err := st.ListSessionShots(ctx, sess.ID, store.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.GetShot(ctx, shots[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if third.ShotNo != 3 || *third.CueBall != "white_02" || *third.FirstContact != "red_01" ||
		*third.Outcome != ingest.OutcomePot || *third.CutAngle != 56.4 {
		t.Errorf("удар 3: %+v", third.Shot)
	}
	if len(third.Pots) != 1 || third.Pots[0].Ball != "red_01" || *third.Pots[0].Pocket != "tl" {
		t.Errorf("падения удара 3: %+v", third.Pots)
	}
	if len(third.Events) != 9 || third.Events[0].Type != event.ShotStart ||
		third.Events[len(third.Events)-1].Type != event.ShotEnd {
		t.Errorf("события удара 3: %d шт.", len(third.Events))
	}

	// Удар 5 детектор открыл по жёлтому; сервис пересчитал его от белого.
	fifth, err := st.GetShot(ctx, shots[4].ID)
	if err != nil {
		t.Fatal(err)
	}
	if *fifth.CueBall != "white_02" || *fifth.FirstContact != "red_04" || *fifth.Outcome != ingest.OutcomePot ||
		fifth.CueSpeed == nil || *fifth.CueSpeed != 0.502 || fifth.CueDirection != nil {
		t.Errorf("удар 5: %+v", fifth.Shot)
	}
	if len(fifth.Pots) != 1 || fifth.Pots[0].Ball != "yellow" || fifth.Pots[0].IsCue {
		t.Errorf("падения удара 5: %+v", fifth.Pots)
	}

	// Повторная загрузка того же файла ничего не меняет.
	again := mustIngest(t, p, ingest.Request{Stream: "t1", Events: events})
	if again.Stored != 0 || again.Duplicates != len(events) {
		t.Errorf("повторная загрузка: %+v", again)
	}
	stats, err = st.Stats(ctx, store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	checkSessionStats(t, stats)
	if n := len(mustListTrainings(t, st, "t1")); n != 1 {
		t.Errorf("после повторной загрузки тренировок: %d", n)
	}
}

func mustListTrainings(t *testing.T, st *store.Store, stream string) []store.Training {
	t.Helper()
	items, err := st.ListTrainings(context.Background(), store.TrainingFilter{Stream: stream, Page: store.Page{Limit: 50}})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// Поток, присланный кусками с разрывами посреди удара и с перекрытием кусков,
// даёт тот же результат, что и загруженный целиком.
func TestIngestInChunks(t *testing.T) {
	st := testdb.New(t)
	p := newProcessor(st, true)
	events := readFile(t, sessionFile)

	const chunk, overlap = 7, 3
	for start := 0; start < len(events); start += chunk {
		from := max(0, start-overlap)
		mustIngest(t, p, ingest.Request{Stream: "t1", Events: events[from:min(len(events), start+chunk)]})
	}

	sess := onlySession(t, st, "t1")
	if sess.Status != store.SessionEnded {
		t.Errorf("статус сессии: %s", sess.Status)
	}
	stats, err := st.Stats(context.Background(), store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	checkSessionStats(t, stats)
	if n := len(mustListTrainings(t, st, "t1")); n != 1 {
		t.Errorf("тренировок: %d, ожидалась одна", n)
	}
}

// Открытая вручную тренировка забирает удары своего окна, остальные остаются
// в автоматической тренировке сессии.
func TestManualTrainingWindow(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)

	player, err := st.CreatePlayer(ctx, "Иван")
	if err != nil {
		t.Fatal(err)
	}
	origin := time.Date(2026, 11, 8, 6, 8, 39, 0, time.UTC)
	manual, err := st.StartTraining(ctx, store.NewTraining{
		Stream: "t1", PlayerID: &player.ID, StartedAt: origin.Add(20 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.StartTraining(ctx, store.NewTraining{Stream: "t1", StartedAt: origin}); err == nil {
		t.Fatal("вторая открытая тренировка на том же потоке должна быть отклонена")
	}
	if _, err := st.FinishTraining(ctx, manual.ID, origin.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}

	mustIngest(t, p, ingest.Request{Stream: "t1", Events: readFile(t, sessionFile)})

	// Удары начинаются на 1.73, 15.59, 25.97, 81.80 и 96.00 с: в окно 20–90 с попадают два.
	got, err := st.GetTraining(ctx, manual.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shots != 2 || got.Pots != 1 || got.PlayerName == nil || *got.PlayerName != "Иван" {
		t.Errorf("ручная тренировка: %+v", got)
	}
	var auto []store.Training
	for _, tr := range mustListTrainings(t, st, "t1") {
		if tr.Origin == store.TrainingAuto {
			auto = append(auto, tr)
		}
	}
	if len(auto) != 1 || auto[0].Shots != 3 {
		t.Errorf("автоматические тренировки: %+v", auto)
	}

	stats, err := st.Stats(ctx, store.PlayerScope(player.ID, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Shots != 2 || stats.Pots != 1 || stats.Misses != 1 {
		t.Errorf("статистика игрока: shots=%d pots=%d misses=%d", stats.Shots, stats.Pots, stats.Misses)
	}
	days, err := st.PlayerProgress(ctx, player.ID, nil, nil, "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Day != "2026-11-08" || days[0].Shots != 2 || days[0].Trainings != 1 {
		t.Errorf("по дням: %+v", days)
	}
	// Период, в который тренировка не попадает.
	later := origin.Add(24 * time.Hour)
	stats, err = st.Stats(ctx, store.PlayerScope(player.ID, &later, nil))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Shots != 0 || stats.PotRate != nil {
		t.Errorf("статистика вне периода: shots=%d pot_rate=%v", stats.Shots, stats.PotRate)
	}
}

func TestAutoTrainingDisabled(t *testing.T) {
	st := testdb.New(t)
	p := newProcessor(st, false)
	mustIngest(t, p, ingest.Request{Stream: "t1", Events: readFile(t, sessionFile)})
	if n := len(mustListTrainings(t, st, "t1")); n != 0 {
		t.Errorf("тренировок: %d, автоматические выключены", n)
	}
	sess := onlySession(t, st, "t1")
	stats, err := st.Stats(context.Background(), store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	checkSessionStats(t, stats)
}

// Поток без шапки попадает в неявную сессию; новая шапка на том же потоке её прерывает.
func TestHeadlessStreamAndRestart(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)

	res := mustIngest(t, p, ingest.Request{Stream: "t1", Events: readFile(t, headlessFile)})
	if res.Duplicates != 0 || len(res.Sessions) != 1 {
		t.Fatalf("итог приёма: %+v", res)
	}
	sess := onlySession(t, st, "t1")
	if !sess.Implicit || sess.Status != store.SessionActive || sess.Shots != 7 {
		t.Fatalf("неявная сессия: implicit=%v status=%s shots=%d", sess.Implicit, sess.Status, sess.Shots)
	}
	stats, err := st.Stats(ctx, store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	// В записи одно подтверждённое падение — red_03 в ударе 5.
	if stats.Shots != 7 || stats.Pots != 1 || stats.BallsPotted != 1 || stats.NearMisses != 2 {
		t.Errorf("статистика: shots=%d pots=%d balls=%d near=%d",
			stats.Shots, stats.Pots, stats.BallsPotted, stats.NearMisses)
	}
	// Угол резки 179.1° из старой записи — вне шкалы 0–90 и в разбивку не идёт.
	var bucketed int64
	for _, b := range stats.ByCutAngle {
		bucketed += b.Shots
	}
	if bucketed != 6 {
		t.Errorf("ударов в разбивке по углу: %d, ожидалось 6", bucketed)
	}

	// Детектор перезапустился: новая шапка закрывает прежнюю сессию и её тренировку.
	mustIngest(t, p, ingest.Request{Stream: "t1", Events: parse(t,
		`{"t":0,"frame":0,"type":"session_start","source_kind":"live","started_at":"2030-01-01T10:00:00Z","delivery":"immediate"}`,
		`{"t":2.0,"frame":50,"type":"shot_start","shot":1}`,
	)})
	old, err := st.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != store.SessionInterrupted || old.EndedAt == nil {
		t.Errorf("прежняя сессия: status=%s ended=%v", old.Status, old.EndedAt)
	}
	for _, tr := range mustListTrainings(t, st, "t1") {
		if tr.SessionID != nil && *tr.SessionID == sess.ID && tr.Status != "finished" {
			t.Errorf("тренировка прерванной сессии осталась открытой: %+v", tr)
		}
	}

	// Сессию без session_end можно закрыть вручную; незаконченный удар обрывается.
	sessions, err := st.ListSessions(ctx, store.SessionFilter{Stream: "t1", Status: store.SessionActive, Page: store.Page{Limit: 5}})
	if err != nil || len(sessions) != 1 {
		t.Fatalf("активные сессии: %v %v", sessions, err)
	}
	closed, err := st.CloseSession(ctx, sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != store.SessionInterrupted || closed.Shots != 0 {
		t.Errorf("закрытая вручную сессия: status=%s shots=%d", closed.Status, closed.Shots)
	}
	shots, err := st.ListSessionShots(ctx, closed.ID, store.Page{Limit: 5})
	if err != nil || len(shots) != 1 || shots[0].Status != "aborted" {
		t.Errorf("незаконченный удар: %+v %v", shots, err)
	}
}

func sessionStart(kind, startedAt string) string {
	return fmt.Sprintf(`{"t":0,"frame":0,"type":"session_start","source_kind":%q,"started_at":%q,"delivery":"immediate"}`,
		kind, startedAt)
}

// Заказ, переданный детектору, делает оценку удара точной: шар должен упасть в названную лузу.
func TestDeclaredTarget(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)

	mustIngest(t, p, ingest.Request{Stream: "t1", Events: parse(t,
		sessionStart("live", "2026-11-10T12:00:00Z"),
		// Удар 1: заказан red_01 в tr, упал в tr.
		`{"t":1.0,"frame":25,"type":"shot_start","shot":1}`,
		`{"t":1.0,"frame":25,"type":"cue_strike","ball":"white","x":0.5,"y":0.5,"direction":0,"speed":1.2,"source":"cue"}`,
		`{"t":1.8,"frame":45,"type":"ball_pocketed","ball":"red_01","pocket":"tr","entry_speed":0.9,"conf":0.97}`,
		`{"t":3.0,"frame":75,"type":"shot_end","shot":1,"duration":2.0,"first_contact":"red_01","pocketed":["red_01"],"cushions":0,"declared_target":{"ball":"red_01","pocket":"tr"}}`,
		// Удар 2: заказан red_02 в tl, упал в bl — не засчитан.
		`{"t":5.0,"frame":125,"type":"shot_start","shot":2}`,
		`{"t":5.0,"frame":125,"type":"cue_strike","ball":"white","x":0.5,"y":0.5,"direction":180,"speed":1.0,"source":"cue"}`,
		`{"t":5.9,"frame":147,"type":"ball_pocketed","ball":"red_02","pocket":"bl","entry_speed":0.4}`,
		`{"t":7.0,"frame":175,"type":"shot_end","shot":2,"duration":2.0,"first_contact":"red_02","pocketed":["red_02"],"cushions":1,"declared_target":{"ball":"red_02","pocket":"tl"}}`,
		// Удар 3: без заказа, упали прицельный и биток.
		`{"t":9.0,"frame":225,"type":"shot_start","shot":3}`,
		`{"t":9.0,"frame":225,"type":"cue_strike","ball":"white","x":0.5,"y":0.5,"direction":90,"speed":2.0,"source":"ball_motion"}`,
		`{"t":9.5,"frame":237,"type":"ball_pocketed","ball":"red_03","pocket":"bm","entry_speed":1.1}`,
		`{"t":10.2,"frame":255,"type":"ball_pocketed","ball":"white","pocket":"br","entry_speed":0.3}`,
		`{"t":11.0,"frame":275,"type":"shot_end","shot":3,"duration":2.0,"first_contact":"red_03","pocketed":["red_03","white"],"cushions":2}`,
		// Удар 4: биток прокатился, никого не задев.
		`{"t":13.0,"frame":325,"type":"shot_start","shot":4}`,
		`{"t":13.0,"frame":325,"type":"cue_strike","ball":"white","x":0.5,"y":0.5,"direction":45,"speed":0.5,"source":"cue"}`,
		`{"t":15.0,"frame":375,"type":"shot_end","shot":4,"duration":2.0,"contacts":[],"cushions":0}`,
		// Незнакомый тип и предупреждение сохраняются, обработку не ломают.
		`{"t":15.5,"frame":387,"type":"something_new","payload":{"a":1}}`,
		`{"t":15.6,"frame":390,"type":"warning","code":"ball_count_mismatch","expected":8,"observed":7,"delivery":"immediate"}`,
	)})

	sess := onlySession(t, st, "t1")
	stats, err := st.Stats(ctx, store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Shots != 4 || stats.Pots != 1 || stats.Misses != 1 || stats.InOffs != 1 || stats.NoContact != 1 {
		t.Errorf("исходы: %+v", stats)
	}
	if stats.DeclaredShots != 2 || stats.BallsPotted != 3 || stats.BestStreak != 1 {
		t.Errorf("declared=%d balls=%d streak=%d", stats.DeclaredShots, stats.BallsPotted, stats.BestStreak)
	}
	// Биток в br не считается забитым шаром.
	if len(stats.ByPocket) != 3 {
		t.Errorf("по лузам: %+v", stats.ByPocket)
	}
	warnings, err := st.SessionWarnings(ctx, sess.ID)
	if err != nil || warnings["ball_count_mismatch"] != 1 {
		t.Errorf("предупреждения: %v %v", warnings, err)
	}
	unknown, err := st.ListEvents(ctx, store.EventFilter{SessionID: sess.ID, Types: []string{"something_new"}, Limit: 10})
	if err != nil || len(unknown) != 1 {
		t.Errorf("незнакомое событие: %v %v", unknown, err)
	}
}

// Запись, проигранная в реальном времени, каждый раз даёт новую сессию со шкалой
// от момента получения; та же запись, загруженная файлом, узнаётся по started_at.
func TestReplayIsANewSession(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)
	events := parse(t,
		sessionStart("replay", "2026-11-08T06:08:39Z"),
		`{"t":1.0,"frame":25,"type":"shot_start","shot":1}`,
		`{"t":3.0,"frame":75,"type":"shot_end","shot":1,"duration":2.0,"cushions":0}`,
		`{"t":4.0,"frame":100,"type":"session_end","frames":100,"delivery":"immediate"}`,
	)

	before := time.Now().Add(-time.Second)
	for range 2 {
		res := mustIngest(t, p, ingest.Request{Stream: "live", Realtime: true, Events: events})
		if res.Stored != len(events) {
			t.Fatalf("replay в реальном времени: %+v", res)
		}
	}
	live, err := st.ListSessions(ctx, store.SessionFilter{Stream: "live", Page: store.Page{Limit: 10}})
	if err != nil || len(live) != 2 {
		t.Fatalf("сессий после двух проигрываний: %d %v", len(live), err)
	}
	for _, s := range live {
		if s.OriginAt.Before(before) || s.StartedAt == nil || s.StartedAt.Year() != 2026 {
			t.Errorf("шкала replay: origin=%v started=%v", s.OriginAt, s.StartedAt)
		}
	}

	for range 2 {
		mustIngest(t, p, ingest.Request{Stream: "file", Events: events})
	}
	file := onlySession(t, st, "file")
	if !file.OriginAt.Equal(time.Date(2026, 11, 8, 6, 8, 39, 0, time.UTC)) || file.Shots != 1 {
		t.Errorf("загрузка файлом: origin=%v shots=%d", file.OriginAt, file.Shots)
	}
}

// Удар, начатый до конца ручной тренировки, но подтверждённый детектором позже,
// всё равно относится к ней: принадлежность решает время удара, а не время доставки.
func TestLateShotBelongsToFinishedTraining(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)
	origin := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	mustIngest(t, p, ingest.Request{Stream: "t1", Realtime: true, Events: parse(t,
		sessionStart("live", "2026-11-10T12:00:00Z"),
		`{"t":2.0,"frame":50,"type":"shot_start","shot":1}`,
		`{"t":4.0,"frame":100,"type":"shot_end","shot":1,"duration":2.0,"cushions":0}`,
	)})
	manual, err := st.StartTraining(ctx, store.NewTraining{Stream: "t1", StartedAt: origin.Add(10 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishTraining(ctx, manual.ID, origin.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Эти события доходят уже после закрытия тренировки.
	mustIngest(t, p, ingest.Request{Stream: "t1", Realtime: true, Events: parse(t,
		`{"t":28.0,"frame":700,"type":"shot_start","shot":2}`,
		`{"t":31.0,"frame":775,"type":"shot_end","shot":2,"duration":3.0,"cushions":1}`,
		`{"t":40.0,"frame":1000,"type":"shot_start","shot":3}`,
		`{"t":42.0,"frame":1050,"type":"shot_end","shot":3,"duration":2.0,"cushions":0}`,
	)})

	got, err := st.GetTraining(ctx, manual.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shots != 1 {
		t.Errorf("ударов в ручной тренировке: %d, ожидался один (t=28)", got.Shots)
	}
	byOrigin := map[string]int64{}
	for _, tr := range mustListTrainings(t, st, "t1") {
		byOrigin[tr.Origin] += tr.Shots
	}
	if byOrigin[store.TrainingAuto] != 2 {
		t.Errorf("ударов в автоматических тренировках: %d, ожидалось два (t=2 и t=40)", byOrigin[store.TrainingAuto])
	}
}

// Тренировка, открытая и закрытая задним числом, забирает уже принятые удары
// своего окна и отпускает те, что оказались за его концом.
func TestBackdatedTraining(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)
	origin := time.Date(2026, 11, 8, 6, 8, 39, 0, time.UTC)
	mustIngest(t, p, ingest.Request{Stream: "t1", Events: readFile(t, sessionFile)})

	// Удары на 25.97, 81.80 и 96.00 с переходят из автоматической тренировки.
	manual, err := st.StartTraining(ctx, store.NewTraining{Stream: "t1", StartedAt: origin.Add(20 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if manual.Shots != 3 {
		t.Errorf("ударов после открытия задним числом: %d, ожидалось 3", manual.Shots)
	}
	// Удар на 96-й секунде оказывается за концом окна.
	finished, err := st.FinishTraining(ctx, manual.ID, origin.Add(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if finished.Shots != 2 || finished.Status != "finished" {
		t.Errorf("после закрытия задним числом: %+v", finished)
	}
	again, err := st.FinishTraining(ctx, manual.ID, origin.Add(30*time.Second))
	if err != nil || again.Shots != 2 || !again.EndedAt.Equal(*finished.EndedAt) {
		t.Errorf("повторное закрытие не должно ничего менять: %+v %v", again, err)
	}
	if _, err := st.FinishTraining(ctx, 999999, origin); err != store.ErrNotFound {
		t.Errorf("закрытие несуществующей тренировки: %v", err)
	}

	var auto int64
	for _, tr := range mustListTrainings(t, st, "t1") {
		if tr.Origin == store.TrainingAuto {
			auto += tr.Shots
		}
	}
	if auto != 2 {
		t.Errorf("ударов в автоматической тренировке: %d, ожидалось 2", auto)
	}
	sess := onlySession(t, st, "t1")
	if sess.Shots != 5 {
		t.Errorf("удары сессии не должны теряться: %d", sess.Shots)
	}
}

func snookerStart(startedAt string) string {
	return fmt.Sprintf(`{"t":0,"frame":0,"type":"session_start","source_kind":"file","started_at":%q,"game":"snooker","delivery":"immediate"}`,
		startedAt)
}

// В снукере биток всегда белый. Детектор открывает удар по любому тронувшемуся
// шару, в том числе когда помощник катит шар кием или игрок ставит шар рукой;
// сервис признаёт ударом только движение, в котором участвовал белый. Сценарии
// взяты из тренировки дальних ударов 19 июня 2026 (удары 3, 2 и 4 детектора).
func TestSnookerCueBallIsWhite(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)

	mustIngest(t, p, ingest.Request{Stream: "t1", Events: parse(t,
		snookerStart("2026-06-19T14:48:12Z"),
		// Удар 1: помощник подкатил коричневый, белый стоит — не удар.
		`{"t":44.33,"frame":1108,"type":"shot_start","shot":1}`,
		`{"t":44.33,"frame":1108,"type":"cue_strike","ball":"brown","x":0.98,"y":0.45,"speed":0.05,"source":"ball_motion"}`,
		`{"t":44.33,"frame":1108,"type":"ball_start","ball":"brown","speed":0.049,"source":"detection"}`,
		`{"t":44.38,"frame":1109,"type":"ball_collision","balls":["brown","red_02"],"x":0.97,"y":0.47}`,
		`{"t":45.54,"frame":1138,"type":"shot_end","shot":1,"duration":1.21,"first_contact":"red_02","contacts":["brown-red_02"],"cushions":0,"geometry":{"cue_to_object":0.02,"cut_angle":12.0}}`,
		// Удар 2: удар открыт по красному помощника, затем игрок бьёт белым:
		// белый задевает зелёный и падает в tl. Сводка детектора — от красного.
		`{"t":34.69,"frame":867,"type":"shot_start","shot":2}`,
		`{"t":34.69,"frame":867,"type":"cue_strike","ball":"red","x":0.95,"y":0.4,"speed":0.115,"source":"ball_motion"}`,
		`{"t":35.20,"frame":880,"type":"ball_collision","balls":["red","black"],"x":0.95,"y":0.42}`,
		`{"t":37.70,"frame":942,"type":"ball_start","ball":"white","x":0.33,"y":0.6,"speed":0.543,"source":"detection"}`,
		`{"t":37.75,"frame":943,"type":"ball_collision","balls":["white","green"],"x":0.92,"y":0.88}`,
		`{"t":41.98,"frame":1049,"type":"ball_pocketed","ball":"white","pocket":"tl","entry_speed":0.3}`,
		`{"t":42.10,"frame":1052,"type":"shot_end","shot":2,"duration":7.41,"first_contact":"black","contacts":["red-black","white-green"],"pocketed":["white"],"cushions":1,"geometry":{"cue_to_object":0.05,"cut_angle":120.0},"near_miss":{"ball":"black","pocket":"tr","distance":0.05,"side":"left","conf":0.8}}`,
		// Удар 3: детектор упустил размытый белый и открыл удар по жёлтому, который
		// белый забил в bm (session_100s, удар 5).
		`{"t":96.00,"frame":2400,"type":"shot_start","shot":3}`,
		`{"t":96.00,"frame":2400,"type":"cue_strike","ball":"yellow","x":0.46,"y":0.82,"speed":0.961,"source":"ball_motion"}`,
		`{"t":96.16,"frame":2404,"type":"ball_pocketed","ball":"yellow","pocket":"bm","entry_speed":0.9}`,
		`{"t":96.24,"frame":2406,"type":"ball_start","ball":"white","x":0.58,"y":0.74,"speed":0.502,"source":"detection"}`,
		`{"t":97.01,"frame":2425,"type":"ball_collision","balls":["white","red_04"],"x":0.98,"y":0.66}`,
		`{"t":99.96,"frame":2499,"type":"shot_end","shot":3,"duration":3.96,"contacts":["white-red_04"],"pocketed":["yellow"],"cushions":0}`,
	)})

	sess := onlySession(t, st, "t1")
	stats, err := st.Stats(ctx, store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Shots != 2 || stats.InOffs != 1 || stats.Pots != 1 || stats.Misses != 0 || stats.NoContact != 0 {
		t.Errorf("исходы: shots=%d in_offs=%d pots=%d misses=%d no_contact=%d",
			stats.Shots, stats.InOffs, stats.Pots, stats.Misses, stats.NoContact)
	}
	if len(stats.ByBall) != 1 || stats.ByBall[0].Ball != "yellow" {
		t.Errorf("по цветам: белый в tl не забитый шар: %+v", stats.ByBall)
	}
	// Средние берутся только от признанных ударов и от белого: 0.543 и 0.502.
	if stats.AvgCueSpeed == nil || *stats.AvgCueSpeed != 0.5225 || stats.NearMisses != 0 || stats.AvgCutAngle != nil {
		t.Errorf("avg_cue_speed=%v near_misses=%d avg_cut_angle=%v",
			stats.AvgCueSpeed, stats.NearMisses, stats.AvgCutAngle)
	}

	shots, err := st.ListSessionShots(ctx, sess.ID, store.Page{Limit: 10})
	if err != nil || len(shots) != 3 {
		t.Fatalf("удары: %d %v", len(shots), err)
	}
	byNo := map[int]store.Shot{}
	for _, s := range shots {
		byNo[s.ShotNo] = s
	}
	rejected := byNo[1]
	if rejected.Status != "rejected" || rejected.RejectReason == nil || rejected.Outcome != nil {
		t.Errorf("удар помощника: %+v", rejected)
	}
	inOff := byNo[2]
	if inOff.Status != "closed" || *inOff.CueBall != "white" || *inOff.FirstContact != "green" ||
		*inOff.Outcome != ingest.OutcomeInOff || *inOff.CueSpeed != 0.543 || *inOff.CueSource != "ball_start" ||
		inOff.CutAngle != nil || inOff.NearMissBall != nil {
		t.Errorf("удар с падением белого: %+v", inOff)
	}
	if pot := byNo[3]; *pot.CueBall != "white" || *pot.Outcome != ingest.OutcomePot || *pot.FirstContact != "red_04" {
		t.Errorf("удар с упущенным белым: %+v", pot)
	}
}

// Детектор сообщает, что тронуло шар (cause в ball_start), а решает сервис:
// белый, которого двигала только рука, — не удар игрока. Сценарии — раскладка
// на тренировке 19 июня 2026: помощник катит белый ладонью, а затем толкает его
// кием одной рукой; плюс удар, открытый по белому, и осторожность при неизвестной
// причине.
func TestSnookerHandMovedWhiteIsNotAShot(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)

	mustIngest(t, p, ingest.Request{Stream: "t1", Events: parse(t,
		snookerStart("2026-06-19T14:48:12Z"),
		// Удар 1: удар открыт по чёрному; белый покатили ладонью, он толкнул
		// розовый и отскочил (отскок своей причины не добавляет) — не удар.
		`{"t":18.23,"frame":455,"type":"shot_start","shot":1}`,
		`{"t":18.23,"frame":455,"type":"cue_strike","ball":"black","x":0.9,"y":0.5,"speed":0.166,"source":"ball_motion"}`,
		`{"t":18.23,"frame":455,"type":"ball_start","ball":"black","speed":0.166,"source":"detection","cause":"unknown"}`,
		`{"t":22.77,"frame":569,"type":"ball_start","ball":"white","x":0.71,"y":0.39,"speed":0.149,"source":"detection","cause":"hand"}`,
		`{"t":22.80,"frame":570,"type":"ball_collision","balls":["white","pink"],"x":0.72,"y":0.37}`,
		`{"t":22.81,"frame":570,"type":"ball_start","ball":"pink","speed":0.129,"source":"detection","cause":"ball","cause_ball":"white"}`,
		`{"t":23.20,"frame":580,"type":"ball_start","ball":"white","x":0.73,"y":0.40,"speed":0.12,"source":"detection","cause":"unknown"}`,
		`{"t":25.33,"frame":633,"type":"ball_stop","ball":"white","x":0.98,"y":0.61}`,
		`{"t":32.14,"frame":803,"type":"shot_end","shot":1,"duration":13.91,"contacts":["white-pink"],"cushions":0}`,
		// Удар 2: тот же удар раскладки, но после ладони белый толкнули кием —
		// это движение от кия, и удар считается от него: скорость 0.335, касаний нет.
		`{"t":40.00,"frame":1000,"type":"shot_start","shot":2}`,
		`{"t":40.00,"frame":1000,"type":"cue_strike","ball":"black","x":0.9,"y":0.5,"speed":0.17,"source":"ball_motion"}`,
		`{"t":42.77,"frame":1069,"type":"ball_start","ball":"white","x":0.71,"y":0.39,"speed":0.149,"source":"detection","cause":"hand"}`,
		`{"t":42.80,"frame":1070,"type":"ball_collision","balls":["white","pink"],"x":0.72,"y":0.37}`,
		`{"t":45.33,"frame":1133,"type":"ball_stop","ball":"white","x":0.98,"y":0.61}`,
		`{"t":46.61,"frame":1165,"type":"ball_start","ball":"white","x":0.94,"y":0.61,"speed":0.335,"source":"detection","cause":"cue"}`,
		`{"t":47.51,"frame":1187,"type":"cushion_hit","ball":"white","cushion":"top"}`,
		`{"t":51.06,"frame":1276,"type":"ball_stop","ball":"white","x":0.19,"y":0.52}`,
		`{"t":52.14,"frame":1303,"type":"shot_end","shot":2,"duration":12.14,"contacts":["white-pink","white-cushion:top"],"cushions":1}`,
		// Удар 3: детектор открыл удар по белому, белый двигала только рука.
		`{"t":60.00,"frame":1500,"type":"shot_start","shot":3}`,
		`{"t":60.00,"frame":1500,"type":"cue_strike","ball":"white","x":0.3,"y":0.5,"speed":0.2,"source":"ball_motion"}`,
		`{"t":60.00,"frame":1500,"type":"ball_start","ball":"white","x":0.3,"y":0.5,"speed":0.2,"source":"detection","cause":"hand"}`,
		`{"t":62.00,"frame":1550,"type":"shot_end","shot":3,"duration":2.0,"cushions":0}`,
		// Удар 4: рука названа, но у белого видели кий (cue_strike от кия) —
		// улика удара есть, и удар не отвергается.
		`{"t":70.00,"frame":1750,"type":"shot_start","shot":4}`,
		`{"t":70.00,"frame":1750,"type":"cue_strike","ball":"white","x":0.3,"y":0.5,"direction":10.0,"speed":0.25,"source":"cue","cue_direction":10.0,"cue_speed":0.1}`,
		`{"t":70.00,"frame":1750,"type":"ball_start","ball":"white","x":0.3,"y":0.5,"speed":0.25,"source":"cue","cause":"hand"}`,
		`{"t":72.00,"frame":1800,"type":"shot_end","shot":4,"duration":2.0,"cushions":0}`,
		// Удар 5: удар открыт по красному помощника, причина старта белого
		// неизвестна (кия не видели) — сомнение трактуется в пользу удара.
		`{"t":80.00,"frame":2000,"type":"shot_start","shot":5}`,
		`{"t":80.00,"frame":2000,"type":"cue_strike","ball":"red","x":0.9,"y":0.4,"speed":0.12,"source":"ball_motion"}`,
		`{"t":80.50,"frame":2012,"type":"ball_start","ball":"white","x":0.21,"y":0.51,"speed":0.8,"source":"detection","cause":"unknown"}`,
		`{"t":81.40,"frame":2035,"type":"ball_collision","balls":["white","red_05"],"x":0.9,"y":0.8}`,
		`{"t":85.00,"frame":2125,"type":"shot_end","shot":5,"duration":5.0,"first_contact":"red","contacts":["white-red_05"],"cushions":0}`,
		// Удар 6: открыт по белому от руки (игрок поправил биток), затем белый
		// остановился и ушёл от кия — биток считается от второго старта.
		`{"t":90.00,"frame":2250,"type":"shot_start","shot":6}`,
		`{"t":90.00,"frame":2250,"type":"cue_strike","ball":"white","x":0.25,"y":0.5,"speed":0.3,"source":"ball_motion"}`,
		`{"t":90.00,"frame":2250,"type":"ball_start","ball":"white","x":0.25,"y":0.5,"speed":0.3,"source":"detection","cause":"hand"}`,
		`{"t":90.60,"frame":2265,"type":"ball_stop","ball":"white","x":0.21,"y":0.51}`,
		`{"t":91.00,"frame":2275,"type":"ball_start","ball":"white","x":0.21,"y":0.51,"speed":0.9,"source":"detection","cause":"cue"}`,
		`{"t":91.80,"frame":2295,"type":"ball_collision","balls":["white","red_05"],"x":0.9,"y":0.8}`,
		`{"t":92.10,"frame":2302,"type":"ball_pocketed","ball":"red_05","pocket":"br","entry_speed":0.4}`,
		`{"t":95.00,"frame":2375,"type":"shot_end","shot":6,"duration":5.0,"first_contact":"red_05","contacts":["white-red_05"],"pocketed":["red_05"],"cushions":0}`,
	)})

	sess := onlySession(t, st, "t1")
	shots, err := st.ListSessionShots(ctx, sess.ID, store.Page{Limit: 10})
	if err != nil || len(shots) != 6 {
		t.Fatalf("удары: %d %v", len(shots), err)
	}
	byNo := map[int]store.Shot{}
	for _, s := range shots {
		byNo[s.ShotNo] = s
	}
	for _, no := range []int{1, 3} {
		s := byNo[no]
		if s.Status != "rejected" || s.RejectReason == nil || !strings.Contains(*s.RejectReason, "рука") || s.Outcome != nil {
			t.Errorf("удар %d: белый двигала только рука, ожидался rejected: %+v", no, s)
		}
	}
	if s := byNo[2]; s.Status != "closed" || *s.CueBall != "white" || *s.CueSpeed != 0.335 ||
		*s.CueX != 0.94 || s.FirstContact != nil || *s.Outcome != ingest.OutcomeNoContact {
		t.Errorf("удар 2: белый ушёл от кия после ладони: %+v", s)
	}
	if s := byNo[4]; s.Status != "closed" || *s.Outcome != ingest.OutcomeNoContact || *s.CueSource != "cue" {
		t.Errorf("удар 4: кий у белого видели: %+v", s)
	}
	if s := byNo[5]; s.Status != "closed" || *s.CueBall != "white" || *s.CueSpeed != 0.8 ||
		*s.FirstContact != "red_05" || *s.Outcome != ingest.OutcomeMiss {
		t.Errorf("удар 5: причина неизвестна, удар остаётся: %+v", s)
	}
	if s := byNo[6]; s.Status != "closed" || *s.CueSpeed != 0.9 || *s.CueX != 0.21 ||
		*s.FirstContact != "red_05" || *s.Outcome != ingest.OutcomePot {
		t.Errorf("удар 6: биток от удара кием, а не от поправки рукой: %+v", s)
	}

	stats, err := st.Stats(ctx, store.SessionScope(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Shots != 4 || stats.Pots != 1 || stats.Misses != 1 || stats.NoContact != 2 {
		t.Errorf("исходы: shots=%d pots=%d misses=%d no_contact=%d",
			stats.Shots, stats.Pots, stats.Misses, stats.NoContact)
	}
}

// Без game: snooker детектору верят: в пирамиде битком бывает любой шар.
func TestCueBallTrustedOutsideSnooker(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	p := newProcessor(st, true)

	mustIngest(t, p, ingest.Request{Stream: "t1", Events: parse(t,
		sessionStart("file", "2026-11-10T12:00:00Z"),
		`{"t":1.0,"frame":25,"type":"shot_start","shot":1}`,
		`{"t":1.0,"frame":25,"type":"cue_strike","ball":"yellow","x":0.5,"y":0.5,"speed":0.9,"source":"cue"}`,
		`{"t":1.4,"frame":35,"type":"ball_pocketed","ball":"yellow","pocket":"bm"}`,
		`{"t":3.0,"frame":75,"type":"shot_end","shot":1,"duration":2.0,"pocketed":["yellow"],"cushions":0}`,
	)})
	stats, err := st.Stats(ctx, store.SessionScope(onlySession(t, st, "t1").ID))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Shots != 1 || stats.InOffs != 1 {
		t.Errorf("исходы: shots=%d in_offs=%d", stats.Shots, stats.InOffs)
	}
}
