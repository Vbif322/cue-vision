// Package event разбирает поток событий детектора snooker_CV (docs/EVENTS.md, схема 0.1).
//
// Контракт потока требует от потребителя терпимости: незнакомые типы и поля
// игнорируются, поэтому событие хранится целиком в Raw, а типизированные
// структуры описывают только то, что нужно статистике.
package event

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

// Типы событий, на которые сервис реагирует. Остальные сохраняются как есть.
const (
	SessionStart      = "session_start"
	SessionEnd        = "session_end"
	TableCalibrated   = "table_calibrated"
	TableRecalibrated = "table_recalibrated"
	BallsInitialized  = "balls_initialized"
	ShotStart         = "shot_start"
	ShotEnd           = "shot_end"
	CueStrike         = "cue_strike"
	BallStart         = "ball_start"
	BallStop          = "ball_stop"
	BallCollision     = "ball_collision"
	CushionHit        = "cushion_hit"
	BallPocketed      = "ball_pocketed"
	PocketRattle      = "pocket_rattle"
	Positions         = "positions"
	Warning           = "warning"
)

const deliveryImmediate = "immediate"

// Причины старта шара — поле cause в ball_start (EVENTS.md §4.2): что тронуло шар.
// Это наблюдение детектора, а не вывод об игре; детекторы без этого поля дают
// пустую строку, и она значит то же, что CauseUnknown.
const (
	CauseCue     = "cue"
	CauseHand    = "hand"
	CauseBall    = "ball"
	CauseUnknown = "unknown"
)

// Источник cue_strike: вектор удара снят с наблюдения самого кия.
const StrikeSourceCue = "cue"

// MaxLineBytes — предел длины одной строки JSONL. Самое крупное событие потока —
// positions на 22 шара — занимает единицы килобайт.
const MaxLineBytes = 4 << 20

// Event — событие с разобранными общими полями (EVENTS.md §2).
type Event struct {
	T     float64
	Frame int64
	Type  string
	Conf  *float64
	// Immediate — событие опубликовано без окна подтверждения и может обгонять
	// подтверждённые события с меньшим t (EVENTS.md §6.1).
	Immediate bool
	// Raw — исходный JSON-объект в компактной форме.
	Raw json.RawMessage
	// Line — номер строки (или элемента массива) во входе, с единицы; 0, если неизвестен.
	Line int
}

type envelope struct {
	T        *float64 `json:"t"`
	Frame    *float64 `json:"frame"`
	Type     *string  `json:"type"`
	Conf     *float64 `json:"conf"`
	Delivery string   `json:"delivery"`
}

// Parse разбирает одно событие. Ошибка означает, что событие нельзя принять:
// это не JSON-объект либо в нём нет type или t.
func Parse(data []byte) (Event, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return Event{}, errors.New("событие должно быть JSON-объектом")
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Event{}, fmt.Errorf("некорректный JSON: %w", err)
	}
	if env.Type == nil || *env.Type == "" {
		return Event{}, errors.New("нет поля type")
	}
	if env.T == nil {
		return Event{}, errors.New("нет поля t")
	}
	if math.IsNaN(*env.T) || math.IsInf(*env.T, 0) || *env.T < 0 {
		return Event{}, errors.New("поле t должно быть неотрицательным числом")
	}
	// jsonb не хранит \u0000, и одна такая строка сорвала бы запись всей пачки.
	if bytes.Contains(data, []byte(`\u0000`)) {
		return Event{}, errors.New("символ \\u0000 в событии не поддерживается")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return Event{}, fmt.Errorf("некорректный JSON: %w", err)
	}
	ev := Event{
		T:         *env.T,
		Type:      *env.Type,
		Conf:      env.Conf,
		Immediate: env.Delivery == deliveryImmediate,
		Raw:       compact.Bytes(),
	}
	if env.Frame != nil {
		ev.Frame = int64(*env.Frame)
	}
	return ev, nil
}

// Decode разбирает специфичные для типа поля события.
func (e Event) Decode(dst any) error {
	return json.Unmarshal(e.Raw, dst)
}

// Hash — отпечаток содержимого события, не зависящий от порядка ключей и пробелов.
// По нему повторно доставленное событие отличается от нового.
func (e Event) Hash() ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(e.Raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	// encoding/json пишет ключи отображений в отсортированном порядке.
	canonical, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return sum[:], nil
}

// LineError — строка входа, которую не удалось принять.
type LineError struct {
	Line  int    `json:"line"`
	Error string `json:"error"`
}

// ReadAll читает поток событий: JSON Lines (основной формат детектора) либо один
// JSON-массив событий. Плохие строки не прерывают чтение — они возвращаются
// отдельным списком, остальные события принимаются.
func ReadAll(r io.Reader) ([]Event, []LineError, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	if bom, err := br.Peek(3); err == nil && bytes.Equal(bom, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	first, err := peekNonSpace(br)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if first == '[' {
		return readArray(br)
	}
	return readLines(br)
}

func peekNonSpace(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return b, br.UnreadByte()
	}
}

func readLines(br *bufio.Reader) ([]Event, []LineError, error) {
	var (
		events []Event
		bad    []LineError
	)
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 0, 64<<10), MaxLineBytes)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		ev, err := Parse(raw)
		if err != nil {
			bad = append(bad, LineError{Line: line, Error: err.Error()})
			continue
		}
		ev.Line = line
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, nil, fmt.Errorf("строка %d длиннее %d байт", line+1, MaxLineBytes)
		}
		return nil, nil, err
	}
	return events, bad, nil
}

func readArray(br *bufio.Reader) ([]Event, []LineError, error) {
	var items []json.RawMessage
	dec := json.NewDecoder(br)
	if err := dec.Decode(&items); err != nil {
		return nil, nil, fmt.Errorf("некорректный JSON-массив: %w", err)
	}
	var (
		events []Event
		bad    []LineError
	)
	for i, item := range items {
		ev, err := Parse(item)
		if err != nil {
			bad = append(bad, LineError{Line: i + 1, Error: err.Error()})
			continue
		}
		ev.Line = i + 1
		events = append(events, ev)
	}
	return events, bad, nil
}

// --- поля, специфичные для типов (EVENTS.md §4) ---

// SessionStartData — шапка сессии.
type SessionStartData struct {
	Source     string   `json:"source"`
	SourceKind string   `json:"source_kind"`
	Hall       string   `json:"hall"`
	Table      string   `json:"table"`
	StartedAt  string   `json:"started_at"`
	FPS        *float64 `json:"fps"`
	Game       string   `json:"game"`
	Schema     string   `json:"schema"`
	Tool       string   `json:"tool"`
}

// BallsInitializedData — исходная раскладка.
type BallsInitializedData struct {
	Balls      []json.RawMessage `json:"balls"`
	Unresolved int               `json:"unresolved"`
}

// ShotStartData — начало удара.
type ShotStartData struct {
	Shot int `json:"shot"`
}

// CueStrikeData — удар кием.
type CueStrikeData struct {
	Ball      string   `json:"ball"`
	X         *float64 `json:"x"`
	Y         *float64 `json:"y"`
	Direction *float64 `json:"direction"`
	Speed     *float64 `json:"speed"`
	Source    string   `json:"source"`
}

// BallPocketedData — подтверждённое падение шара в лузу.
type BallPocketedData struct {
	Ball       string   `json:"ball"`
	Pocket     string   `json:"pocket"`
	EntrySpeed *float64 `json:"entry_speed"`
}

// ShotEndData — сводка удара.
type ShotEndData struct {
	Shot           int       `json:"shot"`
	Duration       *float64  `json:"duration"`
	FirstContact   string    `json:"first_contact"`
	Contacts       []string  `json:"contacts"`
	Pocketed       []string  `json:"pocketed"`
	Cushions       int       `json:"cushions"`
	Geometry       *Geometry `json:"geometry"`
	NearMiss       *NearMiss `json:"near_miss"`
	DeclaredTarget *Target   `json:"declared_target"`
}

// Geometry — условия удара; расстояния в нормированных единицах поля.
type Geometry struct {
	CueToObject    *float64 `json:"cue_to_object"`
	ObjectToPocket *float64 `json:"object_to_pocket"`
	CutAngle       *float64 `json:"cut_angle"`
	Obstructed     *bool    `json:"obstructed"`
}

// NearMiss — прицельный шар прошёл рядом с лузой, но не упал.
type NearMiss struct {
	Ball     string   `json:"ball"`
	Pocket   string   `json:"pocket"`
	Distance *float64 `json:"distance"`
	Side     string   `json:"side"`
	Conf     *float64 `json:"conf"`
}

// Target — заказ на удар, сообщённый детектору командой set_target.
type Target struct {
	Ball   string `json:"ball"`
	Pocket string `json:"pocket"`
}
