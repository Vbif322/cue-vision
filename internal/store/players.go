package store

import (
	"context"
	"strings"
	"time"
)

// Player — игрок, которому приписываются тренировки.
type Player struct {
	ID        int64     `json:"id" db:"id"`
	Name      string    `json:"name" db:"name"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// CreatePlayer заводит игрока. Имена уникальны без учёта регистра.
func (s *Store) CreatePlayer(ctx context.Context, name string) (Player, error) {
	p, err := one[Player](s.pool.Query(ctx,
		`INSERT INTO players (name) VALUES ($1) RETURNING id, name, created_at`, strings.TrimSpace(name)))
	if isUniqueViolation(err) {
		return Player{}, &ConflictError{Msg: "игрок с таким именем уже есть"}
	}
	return p, err
}

// ListPlayers возвращает игроков по алфавиту.
func (s *Store) ListPlayers(ctx context.Context, page Page) ([]Player, error) {
	return many[Player](s.pool.Query(ctx,
		`SELECT id, name, created_at FROM players ORDER BY lower(name), id LIMIT $1 OFFSET $2`,
		page.Limit, page.Offset))
}

// GetPlayer возвращает игрока по идентификатору.
func (s *Store) GetPlayer(ctx context.Context, id int64) (Player, error) {
	return one[Player](s.pool.Query(ctx, `SELECT id, name, created_at FROM players WHERE id = $1`, id))
}
