package ingest

import (
	"context"
	"encoding/json"

	"github.com/Vbif322/cue-vision/internal/event"
)

// whiteMotion — что известно о движении белого внутри удара.
type whiteMotion struct {
	// Ball — имя белого в потоке: white или white_NN.
	Ball string
	// X, Y, Speed — собственный старт битка: первый ball_start белого, который
	// тронула не рука и который не отскок от чужого шара (до него в той же фазе
	// движения белый ни с кем не сталкивался).
	X, Y, Speed *float64
	// FirstContact — другой шар в первом столкновении белого после собственного
	// старта; без собственного старта — в первом столкновении белого вообще.
	FirstContact string
	// HandOnly — белый трогался, и каждый его старт детектор приписал руке, а
	// кия у белого не видел ни в одном событии удара. Удар игрока так не выглядит:
	// это перестановка битка рукой.
	HandOnly bool
	// HandFirst — первый старт белого в ударе был от руки.
	HandFirst bool
}

// ballFields — поля событий о шарах, нужные для поиска белого.
type ballFields struct {
	Ball   string   `json:"ball"`
	Balls  []string `json:"balls"`
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	Speed  *float64 `json:"speed"`
	Source string   `json:"source"`
	Cause  string   `json:"cause"`
}

// findWhiteMotion ищет среди событий удара движение белого. Возвращает nil, если
// белый в ударе не двигался: ни старта, ни столкновения, ни борта, ни лузы.
//
// Причина старта (cause в ball_start) — наблюдение детектора; здесь решается,
// что она значит для игры. Старт от руки не удар. Отскок — старт сразу после
// столкновения, без остановки между ними, — продолжает уже начатое движение и
// своей причины не добавляет: белый, которого толкнули рукой в другой шар,
// остаётся толкнутым рукой. Причина cue у старта или источник cue у cue_strike
// белого — улика удара кием, и при ней удар не отвергается.
func (r *run) findWhiteMotion(ctx context.Context, shotID int64) (*whiteMotion, error) {
	rows, err := r.tx.Query(ctx, `
		SELECT type, payload::text FROM events
		 WHERE shot_id = $1 AND type IN ('cue_strike', 'ball_start', 'ball_stop', 'ball_collision', 'cushion_hit', 'ball_pocketed')
		 ORDER BY t, id`, shotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		m *whiteMotion
		// collided — белый сталкивался после своего последнего старта или остановки.
		collided  bool
		started   bool // собственный старт найден
		anyStart  bool
		hand, own int
		cueSeen   bool
		firstAny  string
		firstOwn  string
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
				other := f.Balls[1-i]
				if firstAny == "" {
					firstAny = other
				}
				if started && firstOwn == "" {
					firstOwn = other
				}
				collided = true
				break
			}
		case "ball_start":
			if !IsWhite(f.Ball) {
				continue
			}
			moved(f.Ball)
			rebound := collided && f.Cause != event.CauseCue
			switch {
			case f.Cause == event.CauseHand:
				hand++
				if !anyStart {
					m.HandFirst = true
				}
			case !rebound:
				own++
				if !started {
					started = true
					m.X, m.Y, m.Speed = f.X, f.Y, f.Speed
				}
			}
			if f.Cause == event.CauseCue {
				cueSeen = true
			}
			anyStart = true
			collided = false
		case "ball_stop":
			if IsWhite(f.Ball) {
				collided = false
			}
		case "cue_strike":
			if !IsWhite(f.Ball) {
				continue
			}
			moved(f.Ball)
			if f.Source == event.StrikeSourceCue {
				cueSeen = true
			}
		default: // cushion_hit, ball_pocketed
			if IsWhite(f.Ball) {
				moved(f.Ball)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	if started {
		m.FirstContact = firstOwn
	} else {
		m.FirstContact = firstAny
	}
	m.HandOnly = hand > 0 && own == 0 && !cueSeen
	return m, nil
}
