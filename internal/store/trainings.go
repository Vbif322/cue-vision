package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Происхождение тренировки.
const (
	TrainingManual = "manual"
	TrainingAuto   = "auto"
)

// Training — тренировка: окно времени на потоке. Удар относится к тренировке,
// если момент его начала попадает в [started_at, ended_at).
type Training struct {
	ID         int64      `json:"id" db:"id"`
	Stream     string     `json:"stream" db:"stream"`
	Status     string     `json:"status" db:"status"`
	Origin     string     `json:"origin" db:"origin"`
	PlayerID   *int64     `json:"player_id,omitempty" db:"player_id"`
	PlayerName *string    `json:"player_name,omitempty" db:"player_name"`
	SessionID  *int64     `json:"session_id,omitempty" db:"session_id"`
	Title      *string    `json:"title,omitempty" db:"title"`
	Exercise   *string    `json:"exercise,omitempty" db:"exercise"`
	Notes      *string    `json:"notes,omitempty" db:"notes"`
	StartedAt  time.Time  `json:"started_at" db:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty" db:"ended_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	Shots      int64      `json:"shots" db:"shots"`
	Pots       int64      `json:"pots" db:"pots"`
}

const trainingSelect = `
SELECT t.id, t.stream,
       CASE WHEN t.ended_at IS NULL THEN 'active' ELSE 'finished' END AS status,
       t.origin, t.player_id, p.name AS player_name, t.session_id, t.title, t.exercise, t.notes,
       t.started_at, t.ended_at, t.created_at,
       coalesce(a.shots, 0) AS shots, coalesce(a.pots, 0) AS pots
FROM trainings t
LEFT JOIN players p ON p.id = t.player_id
LEFT JOIN LATERAL (
    SELECT count(*) AS shots, count(*) FILTER (WHERE sh.success) AS pots
      FROM shots sh
     WHERE sh.training_id = t.id AND sh.status = 'closed'
) a ON true`

// TrainingFilter — отбор тренировок.
type TrainingFilter struct {
	Stream   string
	PlayerID int64
	Status   string // active | finished
	// SessionID — тренировки, в которые попали удары сессии, и автоматическая
	// тренировка сессии (у неё может ещё не быть ударов).
	SessionID int64
	Page
}

// ListTrainings возвращает тренировки, новые первыми.
func (s *Store) ListTrainings(ctx context.Context, f TrainingFilter) ([]Training, error) {
	var (
		conds []string
		args  []any
	)
	if f.Stream != "" {
		args = append(args, f.Stream)
		conds = append(conds, fmt.Sprintf("t.stream = $%d", len(args)))
	}
	if f.PlayerID != 0 {
		args = append(args, f.PlayerID)
		conds = append(conds, fmt.Sprintf("t.player_id = $%d", len(args)))
	}
	if f.SessionID != 0 {
		args = append(args, f.SessionID)
		n := len(args)
		conds = append(conds, fmt.Sprintf(
			"(t.session_id = $%d OR EXISTS (SELECT 1 FROM shots sh WHERE sh.training_id = t.id AND sh.session_id = $%d))", n, n))
	}
	switch f.Status {
	case "active":
		conds = append(conds, "t.ended_at IS NULL")
	case "finished":
		conds = append(conds, "t.ended_at IS NOT NULL")
	}
	q := trainingSelect
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit, f.Offset)
	q += fmt.Sprintf(" ORDER BY t.started_at DESC, t.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	return many[Training](s.pool.Query(ctx, q, args...))
}

// GetTraining возвращает тренировку по идентификатору.
func (s *Store) GetTraining(ctx context.Context, id int64) (Training, error) {
	return getTraining(ctx, s.pool, id)
}

func getTraining(ctx context.Context, q DBTX, id int64) (Training, error) {
	return one[Training](q.Query(ctx, trainingSelect+" WHERE t.id = $1", id))
}

// NewTraining — параметры открываемой тренировки.
type NewTraining struct {
	Stream    string
	PlayerID  *int64
	Title     *string
	Exercise  *string
	Notes     *string
	StartedAt time.Time
}

// autoCloseSlack — запас к времени последнего события сессии при закрытии
// автоматической тренировки. Подтверждённые события отстают от камеры на окно
// подтверждения (около 4.3 с), и удар, начатый до границы, ещё может быть в пути.
const autoCloseSlack = 5.0

// StartTraining открывает тренировку на потоке. Открытая вручную тренировка на
// потоке может быть только одна; автоматическая, если идёт, на этом заканчивается.
func (s *Store) StartTraining(ctx context.Context, in NewTraining) (Training, error) {
	var out Training
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := LockStream(ctx, tx, in.Stream); err != nil {
			return err
		}
		var openID int64
		err := tx.QueryRow(ctx,
			`SELECT id FROM trainings WHERE stream = $1 AND origin = 'manual' AND ended_at IS NULL`,
			in.Stream).Scan(&openID)
		if err == nil {
			return &ConflictError{Msg: fmt.Sprintf("на потоке %q уже идёт тренировка %d", in.Stream, openID)}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Автоматическая тренировка уступает место, но не дальше своей сессии:
		// у прерванного потока последнее событие могло быть давно.
		if _, err := tx.Exec(ctx, `
			UPDATE trainings t
			   SET ended_at = GREATEST(t.started_at,
			                           LEAST($2::timestamptz, s.origin_at + make_interval(secs => s.last_t + $3)))
			  FROM sessions s
			 WHERE t.stream = $1 AND t.origin = 'auto' AND t.ended_at IS NULL AND s.id = t.session_id`,
			in.Stream, in.StartedAt, autoCloseSlack); err != nil {
			return err
		}
		var id int64
		err = tx.QueryRow(ctx, `
			INSERT INTO trainings (stream, player_id, origin, title, exercise, notes, started_at)
			VALUES ($1, $2, 'manual', $3, $4, $5, $6) RETURNING id`,
			in.Stream, in.PlayerID, in.Title, in.Exercise, in.Notes, in.StartedAt).Scan(&id)
		if isForeignKeyViolation(err) {
			return fmt.Errorf("игрок: %w", ErrNotFound)
		}
		if err != nil {
			return err
		}
		// Тренировку можно открыть задним числом. Удары, уже принятые в её окно
		// и никем вручную не занятые, переходят к ней: принадлежность определяет
		// время удара, а не порядок, в котором пришли запросы.
		if _, err := tx.Exec(ctx, `
			UPDATE shots sh
			   SET training_id = $1
			  FROM sessions s
			 WHERE s.id = sh.session_id AND s.stream = $2 AND sh.started_at >= $3
			   AND (sh.training_id IS NULL OR EXISTS (
			           SELECT 1 FROM trainings a WHERE a.id = sh.training_id AND a.origin = 'auto'))`,
			id, in.Stream, in.StartedAt); err != nil {
			return err
		}
		out, err = getTraining(ctx, tx, id)
		return err
	})
	return out, err
}

// FinishTraining закрывает тренировку моментом at. Уже закрытая не меняется.
// Удары, начатые в момент at и позже, из тренировки выходят — это возможно, когда
// её закрывают задним числом.
func (s *Store) FinishTraining(ctx context.Context, id int64, at time.Time) (Training, error) {
	var out Training
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var stream string
		err := tx.QueryRow(ctx, `SELECT stream FROM trainings WHERE id = $1`, id).Scan(&stream)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := LockStream(ctx, tx, stream); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE trainings SET ended_at = GREATEST(started_at, $2) WHERE id = $1 AND ended_at IS NULL`, id, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			if _, err := tx.Exec(ctx, `
				UPDATE shots sh
				   SET training_id = NULL
				  FROM trainings t
				 WHERE t.id = $1 AND sh.training_id = t.id AND sh.started_at >= t.ended_at`, id); err != nil {
				return err
			}
		}
		out, err = getTraining(ctx, tx, id)
		return err
	})
	return out, err
}

// TrainingPatch — изменяемые поля тренировки. У каждого поля флаг Set отличает
// «не трогать» от «очистить».
type TrainingPatch struct {
	PlayerID Optional[int64]
	Title    Optional[string]
	Exercise Optional[string]
	Notes    Optional[string]
}

// Optional — значение, которое могли не передать, передать или обнулить.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// UpdateTraining меняет описание тренировки и её привязку к игроку.
func (s *Store) UpdateTraining(ctx context.Context, id int64, p TrainingPatch) (Training, error) {
	var (
		sets []string
		args = []any{id}
	)
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.PlayerID.Set {
		add("player_id", p.PlayerID.Value)
	}
	if p.Title.Set {
		add("title", p.Title.Value)
	}
	if p.Exercise.Set {
		add("exercise", p.Exercise.Value)
	}
	if p.Notes.Set {
		add("notes", p.Notes.Value)
	}
	if len(sets) > 0 {
		_, err := s.pool.Exec(ctx, "UPDATE trainings SET "+strings.Join(sets, ", ")+" WHERE id = $1", args...)
		if isForeignKeyViolation(err) {
			return Training{}, fmt.Errorf("игрок: %w", ErrNotFound)
		}
		if err != nil {
			return Training{}, err
		}
	}
	return s.GetTraining(ctx, id)
}
