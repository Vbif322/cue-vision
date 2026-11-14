// Package detector держит WebSocket-подключения к экземплярам snooker_CV.
//
// Детектор, запущенный с --ws, сам является сервером: рассылает события всем
// клиентам и принимает от них команды (EVENTS.md §7.1). Сервис подключается
// клиентом, переподключается после обрыва и передаёт события в обработчик.
package detector

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Vbif322/cue-vision/internal/config"
	"github.com/Vbif322/cue-vision/internal/event"
	"github.com/Vbif322/cue-vision/internal/ingest"
)

// Ошибки отправки команды.
var (
	ErrUnknownStream = errors.New("детектор для потока не настроен")
	ErrNotConnected  = errors.New("детектор не подключён")
)

// Ingester принимает пачку событий. Реализуется ingest.Processor.
type Ingester interface {
	Ingest(ctx context.Context, req ingest.Request) (ingest.Result, error)
}

const (
	dialTimeout   = 10 * time.Second
	writeTimeout  = 5 * time.Second
	readLimit     = 8 << 20
	batchSize     = 200
	batchInterval = 200 * time.Millisecond
	queueSize     = 2048
	ingestRetries = 3
	backoffMin    = time.Second
	backoffMax    = 30 * time.Second
)

// Status — состояние подключения к детектору.
type Status struct {
	Stream      string     `json:"stream"`
	URL         string     `json:"url"`
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
	Events      int64      `json:"events"`
	Dropped     int64      `json:"dropped"`
	LastError   string     `json:"last_error,omitempty"`
}

// Manager ведёт по одному подключению на настроенный поток.
type Manager struct {
	clients map[string]*client
}

// New создаёт менеджер для детекторов из конфигурации.
func New(detectors []config.Detector, sink Ingester, idle time.Duration, log *slog.Logger) *Manager {
	m := &Manager{clients: map[string]*client{}}
	for _, d := range detectors {
		m.clients[d.Stream] = &client{
			sink: sink,
			idle: idle,
			log:  log.With("stream", d.Stream, "url", d.URL),
			st:   Status{Stream: d.Stream, URL: d.URL},
		}
	}
	return m
}

// Run держит подключения до отмены контекста.
func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, c := range m.clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.loop(ctx)
		}()
	}
	wg.Wait()
}

// Statuses возвращает состояние всех подключений по имени потока.
func (m *Manager) Statuses() []Status {
	out := make([]Status, 0, len(m.clients))
	for _, c := range m.clients {
		out = append(out, c.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stream < out[j].Stream })
	return out
}

// Send отправляет детектору потока управляющую команду.
func (m *Manager) Send(ctx context.Context, stream string, payload []byte) error {
	c, ok := m.clients[stream]
	if !ok {
		return ErrUnknownStream
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, payload)
}

type client struct {
	sink Ingester
	idle time.Duration
	log  *slog.Logger

	mu   sync.Mutex
	conn *websocket.Conn
	st   Status
}

func (c *client) status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

func (c *client) loop(ctx context.Context) {
	backoff := backoffMin
	for ctx.Err() == nil {
		connected, err := c.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if err != nil {
			c.st.LastError = err.Error()
		}
		c.mu.Unlock()
		// Подключение состоялось — детектор был на месте: отсчёт задержки заново.
		if connected {
			backoff = backoffMin
		}
		c.log.Warn("детектор недоступен, повтор", "error", err, "retry_in", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// connect держит одно подключение и возвращается с причиной его разрыва;
// connected сообщает, удалось ли подключиться вообще.
func (c *client) connect(ctx context.Context) (connected bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dialCtx, c.st.URL, nil)
	cancel()
	if err != nil {
		return false, err
	}
	// Сервер детектора не отвечает на закрывающее рукопожатие — рвём соединение сразу.
	defer conn.CloseNow()
	conn.SetReadLimit(readLimit)

	now := time.Now().UTC()
	c.mu.Lock()
	c.conn = conn
	c.st.Connected, c.st.ConnectedAt, c.st.LastError = true, &now, ""
	c.mu.Unlock()
	c.log.Info("детектор подключён")
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.st.Connected = false
		c.mu.Unlock()
	}()

	ctx, cancel = context.WithCancel(ctx)
	defer cancel()
	queue := make(chan event.Event, queueSize)
	readErr := make(chan error, 1)
	go func() {
		defer close(queue)
		readErr <- c.read(ctx, conn, queue)
	}()

	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()
	batch := make([]event.Event, 0, batchSize)
	flush := func() {
		if len(batch) > 0 {
			c.ingest(ctx, batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case ev, ok := <-queue:
			if !ok {
				flush()
				return true, <-readErr
			}
			batch = append(batch, ev)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// read читает сообщения до ошибки. Очередь ограничена: если база не успевает,
// чтение останавливается, и уже детектор вытесняет старейшие события у себя,
// сообщая об этом warning frames_dropped.
func (c *client) read(ctx context.Context, conn *websocket.Conn, queue chan<- event.Event) error {
	for {
		// Живой детектор шлёт state раз в секунду; долгая тишина означает, что
		// соединение умерло без уведомления.
		readCtx, cancel := context.WithTimeout(ctx, c.idle)
		typ, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		ev, err := event.Parse(data)
		if err != nil {
			c.log.Warn("сообщение детектора отброшено", "error", err)
			continue
		}
		select {
		case queue <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ingest передаёт пачку обработчику. Приём идемпотентен, поэтому при ошибке пачку
// можно повторить; после нескольких неудач она отбрасывается, чтобы поток не встал.
func (c *client) ingest(ctx context.Context, batch []event.Event) {
	req := ingest.Request{Stream: c.st.Stream, Realtime: true, Events: batch}
	var err error
	for attempt := 1; attempt <= ingestRetries; attempt++ {
		// Отмена контекста при остановке не должна обрывать запись уже принятого.
		ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_, err = c.sink.Ingest(ictx, req)
		cancel()
		if err == nil {
			now := time.Now().UTC()
			c.mu.Lock()
			c.st.Events += int64(len(batch))
			c.st.LastEventAt = &now
			c.mu.Unlock()
			return
		}
		c.log.Error("пачка событий не записана", "error", err, "attempt", attempt, "events", len(batch))
		if attempt < ingestRetries {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	c.mu.Lock()
	c.st.Dropped += int64(len(batch))
	c.st.LastError = "события отброшены: " + err.Error()
	c.mu.Unlock()
}
