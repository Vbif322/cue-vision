package api

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Vbif322/cue-vision/internal/config"
	"github.com/Vbif322/cue-vision/internal/detector"
	"github.com/Vbif322/cue-vision/internal/event"
	"github.com/Vbif322/cue-vision/internal/ingest"
	"github.com/Vbif322/cue-vision/internal/store"
)

// --- приём событий ---

// ingestEvents принимает поток событий: JSON Lines, как его пишет детектор, или
// JSON-массив. Тело может быть сжато gzip.
func (s *Server) ingestEvents(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	stream, err := streamParam(q.Get("stream"), config.DefaultStream)
	if err != nil {
		return err
	}
	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	var reader io.Reader = body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(body)
		if err != nil {
			return badRequest("тело заявлено как gzip, но не распаковывается: %v", err)
		}
		defer zr.Close()
		// Предел действует и на распакованный объём.
		reader = http.MaxBytesReader(w, zr, s.cfg.MaxBodyBytes)
	}

	events, bad, err := event.ReadAll(reader)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return err
		}
		return badRequest("поток не прочитан: %v", err)
	}
	if len(events) == 0 {
		return &httpError{
			status: http.StatusBadRequest,
			msg:    "в теле нет ни одного пригодного события",
			extra:  map[string]any{"errors": capErrors(bad)},
		}
	}

	res, err := s.processor.Ingest(r.Context(), ingest.Request{
		Stream:   stream,
		Realtime: q.Get("realtime") == "true",
		Events:   events,
	})
	if err != nil {
		return err
	}
	res.Received += len(bad)
	res.Invalid += len(bad)
	res.Errors = capErrors(append(bad, res.Errors...))
	writeJSON(w, http.StatusOK, res)
	return nil
}

func capErrors(errs []event.LineError) []event.LineError {
	if len(errs) > ingest.MaxReportedErrors {
		return errs[:ingest.MaxReportedErrors]
	}
	return errs
}

// --- сессии ---

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) error {
	page, err := pageOf(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	stream, err := streamParam(q.Get("stream"), "")
	if err != nil {
		return err
	}
	status := q.Get("status")
	switch status {
	case "", store.SessionActive, store.SessionEnded, store.SessionInterrupted:
	default:
		return badRequest("параметр status: active, ended или interrupted")
	}
	items, err := s.store.ListSessions(r.Context(), store.SessionFilter{Stream: stream, Status: status, Page: page})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list[store.Session]{Items: items})
	return nil
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, session)
	return nil
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	session, err := s.store.CloseSession(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, session)
	return nil
}

func (s *Server) sessionStats(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		return err
	}
	stats, err := s.store.Stats(r.Context(), store.SessionScope(id))
	if err != nil {
		return err
	}
	if stats.Warnings, err = s.store.SessionWarnings(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "stats": stats})
	return nil
}

func (s *Server) sessionShots(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	page, err := pageOf(r)
	if err != nil {
		return err
	}
	if _, err := s.store.GetSession(r.Context(), id); err != nil {
		return err
	}
	items, err := s.store.ListSessionShots(r.Context(), id, page)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list[store.Shot]{Items: items})
	return nil
}

func (s *Server) sessionEvents(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	limit, err := queryInt(r, "limit", 200, 1, maxLimit)
	if err != nil {
		return err
	}
	afterID, err := queryInt(r, "after_id", 0, 0, 1<<62)
	if err != nil {
		return err
	}
	if _, err := s.store.GetSession(r.Context(), id); err != nil {
		return err
	}
	var types []string
	for _, t := range strings.Split(r.URL.Query().Get("type"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			types = append(types, t)
		}
	}
	items, err := s.store.ListEvents(r.Context(), store.EventFilter{
		SessionID: id, Types: types, AfterID: afterID, Limit: int(limit),
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list[store.StoredEvent]{Items: items})
	return nil
}

// --- тренировки ---

type startTrainingRequest struct {
	Stream    string     `json:"stream"`
	PlayerID  *int64     `json:"player_id"`
	Title     *string    `json:"title"`
	Exercise  *string    `json:"exercise"`
	Notes     *string    `json:"notes"`
	StartedAt *time.Time `json:"started_at"`
}

func (s *Server) startTraining(w http.ResponseWriter, r *http.Request) error {
	var req startTrainingRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	stream, err := streamParam(req.Stream, config.DefaultStream)
	if err != nil {
		return err
	}
	in := store.NewTraining{
		Stream:    stream,
		PlayerID:  req.PlayerID,
		Title:     req.Title,
		Exercise:  req.Exercise,
		Notes:     req.Notes,
		StartedAt: time.Now().UTC(),
	}
	if req.StartedAt != nil {
		in.StartedAt = req.StartedAt.UTC()
	}
	training, err := s.store.StartTraining(r.Context(), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, training)
	return nil
}

func (s *Server) listTrainings(w http.ResponseWriter, r *http.Request) error {
	page, err := pageOf(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	stream, err := streamParam(q.Get("stream"), "")
	if err != nil {
		return err
	}
	playerID, err := queryInt(r, "player_id", 0, 1, 1<<62)
	if err != nil {
		return err
	}
	sessionID, err := queryInt(r, "session_id", 0, 1, 1<<62)
	if err != nil {
		return err
	}
	status := q.Get("status")
	switch status {
	case "", "active", "finished":
	default:
		return badRequest("параметр status: active или finished")
	}
	items, err := s.store.ListTrainings(r.Context(), store.TrainingFilter{
		Stream: stream, PlayerID: playerID, SessionID: sessionID, Status: status, Page: page,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list[store.Training]{Items: items})
	return nil
}

func (s *Server) getTraining(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	training, err := s.store.GetTraining(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, training)
	return nil
}

// updateTraining меняет только переданные поля; null очищает поле.
func (s *Server) updateTraining(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := decodeJSON(w, r, &fields, false); err != nil {
		return err
	}
	var patch store.TrainingPatch
	for name, raw := range fields {
		var err error
		switch name {
		case "player_id":
			patch.PlayerID, err = optional[int64](raw)
		case "title":
			patch.Title, err = optional[string](raw)
		case "exercise":
			patch.Exercise, err = optional[string](raw)
		case "notes":
			patch.Notes, err = optional[string](raw)
		default:
			return badRequest("поле %q менять нельзя: доступны player_id, title, exercise, notes", name)
		}
		if err != nil {
			return badRequest("поле %q: %v", name, err)
		}
	}
	if _, err := s.store.GetTraining(r.Context(), id); err != nil {
		return err
	}
	training, err := s.store.UpdateTraining(r.Context(), id, patch)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, training)
	return nil
}

func optional[T any](raw json.RawMessage) (store.Optional[T], error) {
	out := store.Optional[T]{Set: true}
	if string(raw) == "null" {
		return out, nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return out, err
	}
	out.Value = &v
	return out, nil
}

type finishTrainingRequest struct {
	EndedAt *time.Time `json:"ended_at"`
}

func (s *Server) finishTraining(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var req finishTrainingRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	at := time.Now().UTC()
	if req.EndedAt != nil {
		at = req.EndedAt.UTC()
	}
	training, err := s.store.FinishTraining(r.Context(), id, at)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, training)
	return nil
}

func (s *Server) trainingStats(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	training, err := s.store.GetTraining(r.Context(), id)
	if err != nil {
		return err
	}
	stats, err := s.store.Stats(r.Context(), store.TrainingScope(id))
	if err != nil {
		return err
	}
	end := time.Now()
	if training.EndedAt != nil {
		end = *training.EndedAt
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"training": training,
		"duration": max(0, round2(end.Sub(training.StartedAt).Seconds())),
		"stats":    stats,
	})
	return nil
}

func (s *Server) trainingShots(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	page, err := pageOf(r)
	if err != nil {
		return err
	}
	if _, err := s.store.GetTraining(r.Context(), id); err != nil {
		return err
	}
	items, err := s.store.ListTrainingShots(r.Context(), id, page)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list[store.Shot]{Items: items})
	return nil
}

func (s *Server) getShot(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	shot, err := s.store.GetShot(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, shot)
	return nil
}

// --- игроки ---

type createPlayerRequest struct {
	Name string `json:"name"`
}

func (s *Server) createPlayer(w http.ResponseWriter, r *http.Request) error {
	var req createPlayerRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 200 {
		return badRequest("имя игрока: от 1 до 200 байт")
	}
	player, err := s.store.CreatePlayer(r.Context(), name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, player)
	return nil
}

func (s *Server) listPlayers(w http.ResponseWriter, r *http.Request) error {
	page, err := pageOf(r)
	if err != nil {
		return err
	}
	items, err := s.store.ListPlayers(r.Context(), page)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list[store.Player]{Items: items})
	return nil
}

func (s *Server) getPlayer(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	player, err := s.store.GetPlayer(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, player)
	return nil
}

// playerStats — сводка по всем тренировкам игрока за период и разбивка по дням.
func (s *Server) playerStats(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	tz := q.Get("tz")
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return badRequest("параметр tz: неизвестный часовой пояс %q", tz)
	}
	from, err := parseTime(q.Get("from"), loc)
	if err != nil {
		return err
	}
	to, err := parseTime(q.Get("to"), loc)
	if err != nil {
		return err
	}
	player, err := s.store.GetPlayer(r.Context(), id)
	if err != nil {
		return err
	}
	stats, err := s.store.Stats(r.Context(), store.PlayerScope(id, from, to))
	if err != nil {
		return err
	}
	days, err := s.store.PlayerProgress(r.Context(), id, from, to, tz)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"player": player,
		"from":   from,
		"to":     to,
		"tz":     tz,
		"stats":  stats,
		"by_day": days,
	})
	return nil
}

// --- детекторы ---

func (s *Server) listDetectors(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, list[detector.Status]{Items: s.detectors.Statuses()})
	return nil
}

// command — управляющее сообщение детектору (EVENTS.md §7.1).
type command struct {
	Cmd    string `json:"cmd"`
	Ball   string `json:"ball,omitempty"`
	Pocket string `json:"pocket,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

var pockets = map[string]bool{"tl": true, "tm": true, "tr": true, "bl": true, "bm": true, "br": true}

func (c command) validate() error {
	switch c.Cmd {
	case "set_target":
		// Пустой заказ допустим: он снимает ранее заданный.
		if c.Pocket != "" && !pockets[c.Pocket] {
			return badRequest("pocket: одна из tl, tm, tr, bl, bm, br")
		}
		if c.Mode != "" {
			return badRequest("set_target не принимает mode")
		}
	case "set_mode":
		if c.Mode != "training" && c.Mode != "match" {
			return badRequest("mode: training или match")
		}
		if c.Ball != "" || c.Pocket != "" {
			return badRequest("set_mode не принимает ball и pocket")
		}
	case "reset_layout", "recalibrate":
		if c.Ball != "" || c.Pocket != "" || c.Mode != "" {
			return badRequest("%s не принимает параметров", c.Cmd)
		}
	default:
		return badRequest("cmd: set_target, set_mode, reset_layout или recalibrate")
	}
	return nil
}

func (s *Server) sendCommand(w http.ResponseWriter, r *http.Request) error {
	stream := r.PathValue("stream")
	var cmd command
	if err := decodeJSON(w, r, &cmd, false); err != nil {
		return err
	}
	if err := cmd.validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	switch err := s.detectors.Send(r.Context(), stream, payload); {
	case errors.Is(err, detector.ErrUnknownStream):
		return &httpError{status: http.StatusNotFound, msg: err.Error()}
	case errors.Is(err, detector.ErrNotConnected):
		return &httpError{status: http.StatusServiceUnavailable, msg: err.Error()}
	case err != nil:
		return &httpError{status: http.StatusBadGateway, msg: "команда не доставлена: " + err.Error()}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"sent": true, "command": cmd})
	return nil
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
