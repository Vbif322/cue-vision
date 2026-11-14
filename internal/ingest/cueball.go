package ingest

import (
	"context"
	"encoding/json"
)

// whiteMotion — что известно о движении белого внутри удара, который детектор
// открыл по другому шару.
type whiteMotion struct {
	// Ball — имя белого в потоке: white или white_NN.
	Ball string
	// X, Y, Speed — первый ball_start белого, если он был до первого столкновения
	// белого: тогда это собственный старт битка, а не отскок от чужого шара.
	X, Y, Speed *float64
	// FirstContact — другой шар в первом столкновении белого.
	FirstContact string
}

// ballFields — поля событий о шарах, нужные для поиска белого.
type ballFields struct {
	Ball  string   `json:"ball"`
	Balls []string `json:"balls"`
	X     *float64 `json:"x"`
	Y     *float64 `json:"y"`
	Speed *float64 `json:"speed"`
}

// findWhiteMotion ищет среди событий удара движение белого. Возвращает nil, если
// белый в ударе не двигался: ни старта, ни столкновения, ни борта, ни лузы.
func (r *run) findWhiteMotion(ctx context.Context, shotID int64) (*whiteMotion, error) {
	rows, err := r.tx.Query(ctx, `
		SELECT type, payload::text FROM events
		 WHERE shot_id = $1 AND type IN ('cue_strike', 'ball_start', 'ball_collision', 'cushion_hit', 'ball_pocketed')
		 ORDER BY t, id`, shotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		m        *whiteMotion
		collided bool
	)
	moved := func(ball string) {
		if m == nil {
			m = &whiteMotion{Ball: ball}
		}
	}
	for rows.Next() {
		var typ, payload string
		if err := rows.Scan(&typ, &payload); err != nil {
			return nil, err
		}
		var f ballFields
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			continue // событие уже сохранено, разбор полей здесь не обязателен
		}
		switch typ {
		case "ball_collision":
			for i, ball := range f.Balls {
				if !IsWhite(ball) || len(f.Balls) != 2 {
					continue
				}
				moved(ball)
				if !collided {
					collided = true
					m.FirstContact = f.Balls[1-i]
				}
				break
			}
		case "ball_start", "cue_strike":
			if !IsWhite(f.Ball) {
				continue
			}
			moved(f.Ball)
			if typ == "ball_start" && !collided && m.Speed == nil {
				m.X, m.Y, m.Speed = f.X, f.Y, f.Speed
			}
		default: // cushion_hit, ball_pocketed
			if IsWhite(f.Ball) {
				moved(f.Ball)
			}
		}
	}
	return m, rows.Err()
}
