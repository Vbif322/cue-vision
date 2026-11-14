package store

import (
	"context"
	"fmt"
	"time"
)

// Shot — удар, собранный из shot_start, cue_strike и shot_end.
type Shot struct {
	ID         int64  `json:"id" db:"id"`
	SessionID  int64  `json:"session_id" db:"session_id"`
	TrainingID *int64 `json:"training_id,omitempty" db:"training_id"`
	ShotNo     int    `json:"shot_no" db:"shot_no"`
	Status     string `json:"status" db:"status"`
	// RejectReason — почему удар не признан игровым (status = rejected).
	RejectReason     *string   `json:"reject_reason,omitempty" db:"reject_reason"`
	TStart           float64   `json:"t_start" db:"t_start"`
	TEnd             *float64  `json:"t_end,omitempty" db:"t_end"`
	StartedAt        time.Time `json:"started_at" db:"started_at"`
	Duration         *float64  `json:"duration,omitempty" db:"duration"`
	CueBall          *string   `json:"cue_ball,omitempty" db:"cue_ball"`
	CueX             *float64  `json:"cue_x,omitempty" db:"cue_x"`
	CueY             *float64  `json:"cue_y,omitempty" db:"cue_y"`
	CueDirection     *float64  `json:"cue_direction,omitempty" db:"cue_direction"`
	CueSpeed         *float64  `json:"cue_speed,omitempty" db:"cue_speed"`
	CueSource        *string   `json:"cue_source,omitempty" db:"cue_source"`
	FirstContact     *string   `json:"first_contact,omitempty" db:"first_contact"`
	Contacts         []string  `json:"contacts" db:"contacts"`
	Cushions         int       `json:"cushions" db:"cushions"`
	Pocketed         []string  `json:"pocketed" db:"pocketed"`
	ObjectPotted     int       `json:"object_potted" db:"object_potted"`
	CuePotted        bool      `json:"cue_potted" db:"cue_potted"`
	CutAngle         *float64  `json:"cut_angle,omitempty" db:"cut_angle"`
	CueToObject      *float64  `json:"cue_to_object,omitempty" db:"cue_to_object"`
	ObjectToPocket   *float64  `json:"object_to_pocket,omitempty" db:"object_to_pocket"`
	Obstructed       *bool     `json:"obstructed,omitempty" db:"obstructed"`
	NearMissBall     *string   `json:"near_miss_ball,omitempty" db:"near_miss_ball"`
	NearMissPocket   *string   `json:"near_miss_pocket,omitempty" db:"near_miss_pocket"`
	NearMissDistance *float64  `json:"near_miss_distance,omitempty" db:"near_miss_distance"`
	NearMissSide     *string   `json:"near_miss_side,omitempty" db:"near_miss_side"`
	NearMissConf     *float64  `json:"near_miss_conf,omitempty" db:"near_miss_conf"`
	TargetBall       *string   `json:"target_ball,omitempty" db:"target_ball"`
	TargetPocket     *string   `json:"target_pocket,omitempty" db:"target_pocket"`
	Outcome          *string   `json:"outcome,omitempty" db:"outcome"`
	Success          *bool     `json:"success,omitempty" db:"success"`
	Conf             *float64  `json:"conf,omitempty" db:"conf"`
}

const shotSelect = `
SELECT id, session_id, training_id, shot_no, status, reject_reason, t_start, t_end, started_at, duration,
       cue_ball, cue_x, cue_y, cue_direction, cue_speed, cue_source,
       first_contact, contacts, cushions, pocketed, object_potted, cue_potted,
       cut_angle, cue_to_object, object_to_pocket, obstructed,
       near_miss_ball, near_miss_pocket, near_miss_distance, near_miss_side, near_miss_conf,
       target_ball, target_pocket, outcome, success, conf
FROM shots`

// ListSessionShots возвращает удары сессии по порядку.
func (s *Store) ListSessionShots(ctx context.Context, sessionID int64, page Page) ([]Shot, error) {
	return s.listShots(ctx, "session_id", sessionID, page)
}

// ListTrainingShots возвращает удары тренировки по порядку.
func (s *Store) ListTrainingShots(ctx context.Context, trainingID int64, page Page) ([]Shot, error) {
	return s.listShots(ctx, "training_id", trainingID, page)
}

func (s *Store) listShots(ctx context.Context, column string, id int64, page Page) ([]Shot, error) {
	q := fmt.Sprintf("%s WHERE %s = $1 ORDER BY started_at, id LIMIT $2 OFFSET $3", shotSelect, column)
	return many[Shot](s.pool.Query(ctx, q, id, page.Limit, page.Offset))
}

// Pot — падение шара в лузу.
type Pot struct {
	Ball       string    `json:"ball" db:"ball"`
	Pocket     *string   `json:"pocket,omitempty" db:"pocket"`
	EntrySpeed *float64  `json:"entry_speed,omitempty" db:"entry_speed"`
	IsCue      bool      `json:"is_cue" db:"is_cue"`
	T          float64   `json:"t" db:"t"`
	OccurredAt time.Time `json:"occurred_at" db:"occurred_at"`
}

// ShotDetail — удар вместе с падениями и событиями, из которых он сложился.
type ShotDetail struct {
	Shot
	Pots   []Pot         `json:"pots"`
	Events []StoredEvent `json:"events"`
}

// GetShot возвращает удар с его событиями в порядке времени.
func (s *Store) GetShot(ctx context.Context, id int64) (ShotDetail, error) {
	shot, err := one[Shot](s.pool.Query(ctx, shotSelect+" WHERE id = $1", id))
	if err != nil {
		return ShotDetail{}, err
	}
	pots, err := many[Pot](s.pool.Query(ctx,
		`SELECT ball, pocket, entry_speed, is_cue, t, occurred_at FROM pots WHERE shot_id = $1 ORDER BY t, id`, id))
	if err != nil {
		return ShotDetail{}, err
	}
	events, err := many[StoredEvent](s.pool.Query(ctx, eventSelect+" WHERE shot_id = $1 ORDER BY t, id", id))
	if err != nil {
		return ShotDetail{}, err
	}
	return ShotDetail{Shot: shot, Pots: pots, Events: events}, nil
}
