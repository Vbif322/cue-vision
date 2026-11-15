// Package api — HTTP-интерфейс сервиса: приём событий, тренировки, статистика.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Vbif322/cue-vision/internal/auth"
	"github.com/Vbif322/cue-vision/internal/config"
	"github.com/Vbif322/cue-vision/internal/detector"
	"github.com/Vbif322/cue-vision/internal/ingest"
	"github.com/Vbif322/cue-vision/internal/store"
)

// Server связывает HTTP-обработчики с хранилищем и обработчиком потока.
type Server struct {
	cfg       config.Config
	store     *store.Store
	processor *ingest.Processor
	detectors *detector.Manager
	log       *slog.Logger

	loginLimiter *auth.Limiter
}

// New создаёт сервер.
func New(cfg config.Config, st *store.Store, processor *ingest.Processor, detectors *detector.Manager, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg, store: st, processor: processor, detectors: detectors, log: log,
		loginLimiter: auth.NewLimiter(loginMaxFailures, loginWindow),
	}
}

// Handler собирает маршруты.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	handle := func(level access, pattern string, fn handlerFunc) {
		api.Handle(pattern, s.wrap(s.guard(level, fn)))
	}
	route := func(pattern string, fn handlerFunc) { handle(accessUser, pattern, fn) }
	admin := func(pattern string, fn handlerFunc) { handle(accessAdmin, pattern, fn) }

	handle(accessPublic, "POST /api/v1/auth/login", s.login)
	handle(accessPublic, "POST /api/v1/auth/logout", s.logout)
	route("GET /api/v1/auth/me", s.me)
	route("POST /api/v1/auth/password", s.changePassword)

	admin("GET /api/v1/users", s.listUsers)
	admin("POST /api/v1/users", s.createUser)
	admin("GET /api/v1/users/{id}", s.getUser)
	admin("PATCH /api/v1/users/{id}", s.updateUser)
	admin("DELETE /api/v1/users/{id}", s.deleteUser)

	route("POST /api/v1/events", s.ingestEvents)

	route("GET /api/v1/sessions", s.listSessions)
	route("GET /api/v1/sessions/{id}", s.getSession)
	route("GET /api/v1/sessions/{id}/stats", s.sessionStats)
	route("GET /api/v1/sessions/{id}/shots", s.sessionShots)
	route("GET /api/v1/sessions/{id}/events", s.sessionEvents)
	route("POST /api/v1/sessions/{id}/close", s.closeSession)

	route("POST /api/v1/trainings", s.startTraining)
	route("GET /api/v1/trainings", s.listTrainings)
	route("GET /api/v1/trainings/{id}", s.getTraining)
	route("PATCH /api/v1/trainings/{id}", s.updateTraining)
	route("POST /api/v1/trainings/{id}/finish", s.finishTraining)
	route("GET /api/v1/trainings/{id}/stats", s.trainingStats)
	route("GET /api/v1/trainings/{id}/shots", s.trainingShots)

	route("GET /api/v1/shots/{id}", s.getShot)

	route("POST /api/v1/players", s.createPlayer)
	route("GET /api/v1/players", s.listPlayers)
	route("GET /api/v1/players/{id}", s.getPlayer)
	route("GET /api/v1/players/{id}/stats", s.playerStats)

	route("GET /api/v1/detectors", s.listDetectors)
	route("POST /api/v1/detectors/{stream}/commands", s.sendCommand)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/api/", s.crossOrigin(api))
	return s.logging(mux)
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// httpError — ошибка с готовым кодом ответа и текстом для клиента.
type httpError struct {
	status int
	msg    string
	extra  map[string]any
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &httpError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func (s *Server) wrap(fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		var (
			he       *httpError
			conflict *store.ConflictError
			tooLarge *http.MaxBytesError
		)
		switch {
		case errors.As(err, &he):
			body := map[string]any{"error": he.msg}
			for k, v := range he.extra {
				body[k] = v
			}
			writeJSON(w, he.status, body)
		case errors.Is(err, store.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.As(err, &conflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": conflict.Msg})
		case errors.As(err, &tooLarge):
			writeJSON(w, http.StatusRequestEntityTooLarge,
				map[string]string{"error": fmt.Sprintf("тело запроса больше %d байт", tooLarge.Limit)})
		default:
			s.log.Error("запрос завершился ошибкой", "method", r.Method, "path", r.URL.Path, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "внутренняя ошибка"})
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "http", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "ms", time.Since(started).Milliseconds())
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

type list[T any] struct {
	Items []T `json:"items"`
}

const (
	defaultLimit = 50
	maxLimit     = 1000
	maxJSONBody  = 1 << 20
)

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("идентификатор должен быть положительным числом")
	}
	return id, nil
}

func queryInt(r *http.Request, name string, fallback, lo, hi int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < lo || v > hi {
		return 0, badRequest("параметр %s: ожидается число от %d до %d", name, lo, hi)
	}
	return v, nil
}

func pageOf(r *http.Request) (store.Page, error) {
	limit, err := queryInt(r, "limit", defaultLimit, 1, maxLimit)
	if err != nil {
		return store.Page{}, err
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<40)
	if err != nil {
		return store.Page{}, err
	}
	return store.Page{Limit: int(limit), Offset: int(offset)}, nil
}

// decodeJSON читает тело запроса; пустое тело допустимо, если allowEmpty.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return badRequest("некорректное тело запроса: %v", err)
	}
	return nil
}

// streamParam читает имя потока из параметра запроса.
func streamParam(raw string, fallback string) (string, error) {
	if raw == "" {
		raw = fallback
	}
	if raw != "" && !config.ValidStream(raw) {
		return "", badRequest("имя потока: латинские буквы, цифры, точка, дефис, подчёркивание, до 64 символов")
	}
	return raw, nil
}

// parseTime принимает RFC 3339 либо дату; дата — полночь в поясе loc.
func parseTime(raw string, loc *time.Location) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, raw, loc); err == nil {
		return &t, nil
	}
	return nil, badRequest("время %q: ожидается RFC 3339 или дата ГГГГ-ММ-ДД", raw)
}
