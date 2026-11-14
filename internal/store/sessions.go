package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Статусы сессии.
const (
	SessionActive      = "active"
	SessionEnded       = "ended"
	SessionInterrupted = "interrupted"
)

// Session — прогон детектора на одном потоке.
type Session struct {
	ID            int64           `json:"id" db:"id"`
	Stream        string          `json:"stream" db:"stream"`
	Status        string          `json:"status" db:"status"`
	OriginAt      time.Time       `json:"origin_at" db:"origin_at"`
	StartedAt     *time.Time      `json:"started_at,omitempty" db:"started_at"`
	EndedAt       *time.Time      `json:"ended_at,omitempty" db:"ended_at"`
	Implicit      bool            `json:"implicit" db:"implicit"`
	Source        *string         `json:"source,omitempty" db:"source"`
	SourceKind    *string         `json:"source_kind,omitempty" db:"source_kind"`
	Hall          *string         `json:"hall,omitempty" db:"hall"`
	Table         *string         `json:"table,omitempty" db:"table_name"`
	Game          *string         `json:"game,omitempty" db:"game"`
	FPS           *float64        `json:"fps,omitempty" db:"fps"`
	SchemaVersion *string         `json:"schema,omitempty" db:"schema_version"`
	Tool          *string         `json:"tool,omitempty" db:"tool"`
	BallCount     *int            `json:"ball_count,omitempty" db:"ball_count"`
	Calibration   json.RawMessage `json:"calibration,omitempty" db:"calibration"`
	Summary       json.RawMessage `json:"summary,omitempty" db:"summary"`
	Duration      float64         `json:"duration" db:"last_t"`
	ReceivedAt    time.Time       `json:"received_at" db:"received_at"`
	Shots         int64           `json:"shots" db:"shots"`
}

const sessionSelect = `
SELECT s.id, s.stream, s.status, s.origin_at, s.started_at, s.ended_at, s.implicit,
       s.source, s.source_kind, s.hall, s.table_name, s.game, s.fps, s.schema_version, s.tool,
       s.ball_count, s.calibration, s.summary, s.last_t, s.received_at,
       (SELECT count(*) FROM shots sh WHERE sh.session_id = s.id AND sh.status = 'closed') AS shots
FROM sessions s`

// SessionFilter — отбор сессий.
type SessionFilter struct {
	Stream string
	Status string
	Page
}

// ListSessions возвращает сессии, новые первыми. Калибровка и итог прогона в
// список не входят — они отдаются с отдельной сессией.
func (s *Store) ListSessions(ctx context.Context, f SessionFilter) ([]Session, error) {
	var (
		conds []string
		args  []any
	)
	if f.Stream != "" {
		args = append(args, f.Stream)
		conds = append(conds, fmt.Sprintf("s.stream = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("s.status = $%d", len(args)))
	}
	q := sessionSelect
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit, f.Offset)
	q += fmt.Sprintf(" ORDER BY s.origin_at DESC, s.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	items, err := many[Session](s.pool.Query(ctx, q, args...))
	for i := range items {
		items[i].Calibration, items[i].Summary = nil, nil
	}
	return items, err
}

// GetSession возвращает сессию по идентификатору.
func (s *Store) GetSession(ctx context.Context, id int64) (Session, error) {
	return one[Session](s.pool.Query(ctx, sessionSelect+" WHERE s.id = $1", id))
}

// CloseSession завершает активную сессию со статусом status: обрывает незакрытый
// удар и закрывает автоматические тренировки, жившие внутри сессии. endT — время
// последнего события по шкале сессии; берётся максимум с уже сохранённым.
// Возвращает false, если сессия не была активной.
func CloseSession(ctx context.Context, q DBTX, id int64, status string, endT float64) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE sessions
		   SET status = $2,
		       last_t = GREATEST(last_t, $3),
		       ended_at = origin_at + make_interval(secs => GREATEST(last_t, $3))
		 WHERE id = $1 AND status = 'active'`, id, status, endT)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := q.Exec(ctx,
		`UPDATE shots SET status = 'aborted' WHERE session_id = $1 AND status = 'open'`, id); err != nil {
		return false, err
	}
	_, err = q.Exec(ctx, `
		UPDATE trainings t
		   SET ended_at = GREATEST(t.started_at, s.ended_at)
		  FROM sessions s
		 WHERE s.id = $1 AND t.session_id = s.id AND t.ended_at IS NULL`, id)
	return err == nil, err
}

// CloseSession завершает сессию по запросу извне — когда детектор оборвался,
// не прислав session_end. Повторный вызов ничего не меняет.
func (s *Store) CloseSession(ctx context.Context, id int64) (Session, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return Session{}, err
	}
	if _, err := CloseSession(ctx, s.pool, id, SessionInterrupted, 0); err != nil {
		return Session{}, err
	}
	return s.GetSession(ctx, id)
}

// StoredEvent — событие из сырого потока.
type StoredEvent struct {
	ID         int64           `json:"id" db:"id"`
	SessionID  int64           `json:"session_id" db:"session_id"`
	ShotID     *int64          `json:"shot_id,omitempty" db:"shot_id"`
	T          float64         `json:"t" db:"t"`
	Type       string          `json:"type" db:"type"`
	Immediate  bool            `json:"immediate" db:"immediate"`
	OccurredAt time.Time       `json:"occurred_at" db:"occurred_at"`
	Payload    json.RawMessage `json:"payload" db:"payload"`
}

const eventSelect = `SELECT id, session_id, shot_id, t, type, immediate, occurred_at, payload FROM events`

// EventFilter — отбор событий сессии. Выдача идёт в порядке приёма.
type EventFilter struct {
	SessionID int64
	Types     []string
	AfterID   int64
	Limit     int
}

// ListEvents возвращает события сессии в порядке приёма, начиная после AfterID.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]StoredEvent, error) {
	q := eventSelect + ` WHERE session_id = $1 AND id > $2`
	args := []any{f.SessionID, f.AfterID}
	if len(f.Types) > 0 {
		args = append(args, f.Types)
		q += fmt.Sprintf(" AND type = ANY($%d)", len(args))
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY id LIMIT $%d", len(args))
	return many[StoredEvent](s.pool.Query(ctx, q, args...))
}
