package store

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"
)

// Stats — статистика по набору завершённых ударов. Считается запросом по таблице
// ударов в момент обращения, поэтому всегда согласована с принятыми событиями.
//
// Расстояния и скорости — в нормированных единицах потока (доля длинной стороны
// поля и доля в секунду), углы — в градусах.
type Stats struct {
	Shots int64 `json:"shots"`
	// Исходы взаимно исключают друг друга: shots = pots + misses + no_contact + in_offs.
	Pots      int64 `json:"pots"`
	Misses    int64 `json:"misses"`
	NoContact int64 `json:"no_contact"`
	InOffs    int64 `json:"in_offs"`
	// PotRate — доля результативных ударов среди всех; null, когда ударов нет.
	PotRate     *float64 `json:"pot_rate"`
	BallsPotted int64    `json:"balls_potted"`
	// BestStreak — самая длинная серия результативных ударов подряд в одной тренировке.
	BestStreak    int64 `json:"best_streak"`
	DeclaredShots int64 `json:"declared_shots"`

	NearMisses          int64    `json:"near_misses"`
	NearMissLeft        int64    `json:"near_miss_left"`
	NearMissRight       int64    `json:"near_miss_right"`
	AvgNearMissDistance *float64 `json:"avg_near_miss_distance"`

	AvgCueSpeed       *float64 `json:"avg_cue_speed"`
	MaxCueSpeed       *float64 `json:"max_cue_speed"`
	AvgCutAngle       *float64 `json:"avg_cut_angle"`
	AvgCueToObject    *float64 `json:"avg_cue_to_object"`
	AvgObjectToPocket *float64 `json:"avg_object_to_pocket"`
	AvgShotDuration   *float64 `json:"avg_shot_duration"`
	AvgCushions       *float64 `json:"avg_cushions"`

	FirstShotAt *time.Time `json:"first_shot_at"`
	LastShotAt  *time.Time `json:"last_shot_at"`

	ByPocket   []PocketStat `json:"by_pocket"`
	ByBall     []BallStat   `json:"by_ball"`
	ByCutAngle []BucketStat `json:"by_cut_angle"`
	ByDistance []BucketStat `json:"by_distance"`

	// Warnings — предупреждения детектора по кодам; заполняется только для сессии.
	Warnings map[string]int64 `json:"warnings,omitempty"`
}

// PocketStat — падения и близкие промахи по одной лузе.
type PocketStat struct {
	Pocket        string `json:"pocket"`
	Pots          int64  `json:"pots"`
	NearMisses    int64  `json:"near_misses"`
	NearMissLeft  int64  `json:"near_miss_left"`
	NearMissRight int64  `json:"near_miss_right"`
}

// BallStat — забитые шары одного цвета.
type BallStat struct {
	Ball string `json:"ball"`
	Pots int64  `json:"pots"`
}

// BucketStat — результативность в диапазоне значения (угла резки, дистанции).
type BucketStat struct {
	Label   string   `json:"label"`
	From    float64  `json:"from"`
	To      float64  `json:"to"`
	Shots   int64    `json:"shots"`
	Pots    int64    `json:"pots"`
	PotRate *float64 `json:"pot_rate"`
}

// Scope — условие на удары (псевдоним s), по которым считается статистика.
type Scope struct {
	cond string
	args []any
}

// TrainingScope — удары одной тренировки.
func TrainingScope(id int64) Scope {
	return Scope{cond: "s.training_id = $1", args: []any{id}}
}

// SessionScope — удары одной сессии детектора.
func SessionScope(id int64) Scope {
	return Scope{cond: "s.session_id = $1", args: []any{id}}
}

// PlayerScope — удары всех тренировок игрока за период [from, to).
func PlayerScope(playerID int64, from, to *time.Time) Scope {
	return Scope{
		cond: `s.training_id IN (SELECT id FROM trainings WHERE player_id = $1)
		       AND ($2::timestamptz IS NULL OR s.started_at >= $2)
		       AND ($3::timestamptz IS NULL OR s.started_at < $3)`,
		args: []any{playerID, from, to},
	}
}

var (
	cutAngleBounds = []float64{0, 15, 30, 45, 60, 90}
	cutAngleLabels = []string{"0-15", "15-30", "30-45", "45-60", "60-90"}
	distanceBounds = []float64{0, 0.25, 0.5, 1.5}
	distanceLabels = []string{"short", "medium", "long"}
	pocketOrder    = map[string]int{"tl": 0, "tm": 1, "tr": 2, "bl": 3, "bm": 4, "br": 5}
)

// Stats считает статистику по ударам из области sc.
func (s *Store) Stats(ctx context.Context, sc Scope) (Stats, error) {
	var st Stats
	closed := "s.status = 'closed' AND " + sc.cond

	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE s.outcome = 'pot'),
		       count(*) FILTER (WHERE s.outcome = 'miss'),
		       count(*) FILTER (WHERE s.outcome = 'no_contact'),
		       count(*) FILTER (WHERE s.outcome = 'in_off'),
		       coalesce(sum(s.object_potted), 0)::bigint,
		       count(*) FILTER (WHERE s.target_ball IS NOT NULL OR s.target_pocket IS NOT NULL),
		       count(*) FILTER (WHERE s.near_miss_ball IS NOT NULL),
		       count(*) FILTER (WHERE s.near_miss_side = 'left'),
		       count(*) FILTER (WHERE s.near_miss_side = 'right'),
		       avg(s.near_miss_distance),
		       avg(s.cue_speed), max(s.cue_speed),
		       avg(s.cut_angle) FILTER (WHERE s.cut_angle BETWEEN 0 AND 90),
		       avg(s.cue_to_object), avg(s.object_to_pocket),
		       avg(s.duration), avg(s.cushions)::float8,
		       min(s.started_at), max(s.started_at)
		  FROM shots s WHERE `+closed, sc.args...).Scan(
		&st.Shots, &st.Pots, &st.Misses, &st.NoContact, &st.InOffs, &st.BallsPotted, &st.DeclaredShots,
		&st.NearMisses, &st.NearMissLeft, &st.NearMissRight, &st.AvgNearMissDistance,
		&st.AvgCueSpeed, &st.MaxCueSpeed, &st.AvgCutAngle, &st.AvgCueToObject, &st.AvgObjectToPocket,
		&st.AvgShotDuration, &st.AvgCushions, &st.FirstShotAt, &st.LastShotAt)
	if err != nil {
		return Stats{}, err
	}
	st.PotRate = ratio(st.Pots, st.Shots)
	for _, p := range []**float64{
		&st.AvgNearMissDistance, &st.AvgCueSpeed, &st.MaxCueSpeed, &st.AvgCutAngle,
		&st.AvgCueToObject, &st.AvgObjectToPocket, &st.AvgShotDuration, &st.AvgCushions,
	} {
		if *p != nil {
			v := round4(**p)
			*p = &v
		}
	}

	// Серии: разность двух нумераций одинакова у подряд идущих ударов с одним исходом.
	// Серия не переходит из тренировки в тренировку и из сессии в сессию.
	err = s.pool.QueryRow(ctx, `
		SELECT coalesce(max(len), 0) FROM (
		    SELECT count(*) AS len FROM (
		        SELECT s.session_id, s.training_id, s.success,
		               row_number() OVER (PARTITION BY s.session_id, s.training_id ORDER BY s.started_at, s.id)
		             - row_number() OVER (PARTITION BY s.session_id, s.training_id, s.success ORDER BY s.started_at, s.id) AS grp
		          FROM shots s WHERE `+closed+`
		    ) runs
		    WHERE success
		    GROUP BY session_id, training_id, grp
		) streaks`, sc.args...).Scan(&st.BestStreak)
	if err != nil {
		return Stats{}, err
	}

	if st.ByPocket, err = s.byPocket(ctx, closed, sc.args); err != nil {
		return Stats{}, err
	}
	if st.ByBall, err = s.byBall(ctx, closed, sc.args); err != nil {
		return Stats{}, err
	}
	if st.ByCutAngle, err = s.byBucket(ctx, "s.cut_angle", cutAngleBounds, cutAngleLabels, closed, sc.args); err != nil {
		return Stats{}, err
	}
	if st.ByDistance, err = s.byBucket(ctx, "s.cue_to_object", distanceBounds, distanceLabels, closed, sc.args); err != nil {
		return Stats{}, err
	}
	return st, nil
}

func (s *Store) byPocket(ctx context.Context, closed string, args []any) ([]PocketStat, error) {
	byName := map[string]*PocketStat{}
	get := func(name string) *PocketStat {
		if p, ok := byName[name]; ok {
			return p
		}
		p := &PocketStat{Pocket: name}
		byName[name] = p
		return p
	}

	rows, err := s.pool.Query(ctx, `
		SELECT p.pocket, count(*)
		  FROM pots p JOIN shots s ON s.id = p.shot_id
		 WHERE `+closed+` AND NOT p.is_cue AND p.pocket IS NOT NULL
		 GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			name string
			n    int64
		)
		if err := rows.Scan(&name, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(name).Pots = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT s.near_miss_pocket, count(*),
		       count(*) FILTER (WHERE s.near_miss_side = 'left'),
		       count(*) FILTER (WHERE s.near_miss_side = 'right')
		  FROM shots s
		 WHERE `+closed+` AND s.near_miss_pocket IS NOT NULL
		 GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			name           string
			n, left, right int64
		)
		if err := rows.Scan(&name, &n, &left, &right); err != nil {
			rows.Close()
			return nil, err
		}
		p := get(name)
		p.NearMisses, p.NearMissLeft, p.NearMissRight = n, left, right
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]PocketStat, 0, len(byName))
	for _, p := range byName {
		out = append(out, *p)
	}
	rank := func(name string) int {
		if r, ok := pocketOrder[name]; ok {
			return r
		}
		return len(pocketOrder)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rank(out[i].Pocket), rank(out[j].Pocket)
		if ri != rj {
			return ri < rj
		}
		return out[i].Pocket < out[j].Pocket
	})
	return out, nil
}

func (s *Store) byBall(ctx context.Context, closed string, args []any) ([]BallStat, error) {
	// Цвет — идентификатор без номера: red_03 и red_03b → red, цветные как есть.
	rows, err := s.pool.Query(ctx, `
		SELECT regexp_replace(p.ball, '_[0-9]+[a-z]*$', '') AS ball, count(*) AS pots
		  FROM pots p JOIN shots s ON s.id = p.shot_id
		 WHERE `+closed+` AND NOT p.is_cue
		 GROUP BY 1 ORDER BY 2 DESC, 1`, args...)
	if err != nil {
		return nil, err
	}
	out := []BallStat{}
	for rows.Next() {
		var b BallStat
		if err := rows.Scan(&b.Ball, &b.Pots); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// byBucket раскладывает удары по диапазонам значения expr. Диапазоны возвращаются
// все, включая пустые, чтобы форма ответа не зависела от данных.
func (s *Store) byBucket(ctx context.Context, expr string, bounds []float64, labels []string,
	closed string, args []any) ([]BucketStat, error) {
	out := make([]BucketStat, len(labels))
	for i := range labels {
		out[i] = BucketStat{Label: labels[i], From: bounds[i], To: bounds[i+1]}
	}
	inner := bounds[1 : len(bounds)-1]
	q := `SELECT width_bucket(` + expr + `, $` + strconv.Itoa(len(args)+1) + `::float8[]), count(*), count(*) FILTER (WHERE s.success)
	        FROM shots s
	       WHERE ` + closed + ` AND ` + expr + ` >= $` + strconv.Itoa(len(args)+2) + ` AND ` + expr + ` <= $` + strconv.Itoa(len(args)+3) + `
	       GROUP BY 1`
	rows, err := s.pool.Query(ctx, q, append(append([]any{}, args...), inner, bounds[0], bounds[len(bounds)-1])...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			idx         int
			shots, pots int64
		)
		if err := rows.Scan(&idx, &shots, &pots); err != nil {
			rows.Close()
			return nil, err
		}
		if idx >= 0 && idx < len(out) {
			out[idx].Shots, out[idx].Pots = shots, pots
			out[idx].PotRate = ratio(pots, shots)
		}
	}
	return out, rows.Err()
}

// SessionWarnings считает предупреждения детектора в сессии по кодам.
func (s *Store) SessionWarnings(ctx context.Context, sessionID int64) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT coalesce(payload->>'code', 'unknown'), count(*)
		  FROM events WHERE session_id = $1 AND type = 'warning' GROUP BY 1`, sessionID)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for rows.Next() {
		var (
			code string
			n    int64
		)
		if err := rows.Scan(&code, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out[code] = n
	}
	return out, rows.Err()
}

// DayStat — итог одного календарного дня игрока.
type DayStat struct {
	Day         string   `json:"day"`
	Trainings   int64    `json:"trainings"`
	Shots       int64    `json:"shots"`
	Pots        int64    `json:"pots"`
	PotRate     *float64 `json:"pot_rate"`
	BallsPotted int64    `json:"balls_potted"`
}

// PlayerProgress возвращает результаты игрока по дням в часовом поясе tz.
func (s *Store) PlayerProgress(ctx context.Context, playerID int64, from, to *time.Time, tz string) ([]DayStat, error) {
	sc := PlayerScope(playerID, from, to)
	rows, err := s.pool.Query(ctx, `
		SELECT (s.started_at AT TIME ZONE $4)::date AS day,
		       count(DISTINCT s.training_id), count(*), count(*) FILTER (WHERE s.success),
		       coalesce(sum(s.object_potted), 0)::bigint
		  FROM shots s
		 WHERE s.status = 'closed' AND `+sc.cond+`
		 GROUP BY 1 ORDER BY 1`, append(sc.args, tz)...)
	if err != nil {
		return nil, err
	}
	out := []DayStat{}
	for rows.Next() {
		var (
			day time.Time
			d   DayStat
		)
		if err := rows.Scan(&day, &d.Trainings, &d.Shots, &d.Pots, &d.BallsPotted); err != nil {
			rows.Close()
			return nil, err
		}
		d.Day = day.Format(time.DateOnly)
		d.PotRate = ratio(d.Pots, d.Shots)
		out = append(out, d)
	}
	return out, rows.Err()
}

func ratio(part, total int64) *float64 {
	if total == 0 {
		return nil
	}
	v := round4(float64(part) / float64(total))
	return &v
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }
