package detector

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Vbif322/cue-vision/internal/config"
	"github.com/Vbif322/cue-vision/internal/ingest"
)

type recorder struct {
	mu     sync.Mutex
	events []string
	fail   int // сколько первых вызовов завершить ошибкой
}

func (r *recorder) Ingest(_ context.Context, req ingest.Request) (ingest.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		return ingest.Result{}, context.DeadlineExceeded
	}
	if !req.Realtime {
		panic("события с WebSocket должны идти как поток реального времени")
	}
	for _, ev := range req.Events {
		r.events = append(r.events, req.Stream+":"+ev.Type)
	}
	return ingest.Result{Received: len(req.Events)}, nil
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Сервер ведёт себя как snooker_CV --ws: шлёт события, принимает команды и после
// первой команды рвёт соединение — клиент обязан подключиться снова.
func TestManagerReadsReconnectsAndSendsCommands(t *testing.T) {
	var (
		mu          sync.Mutex
		connections int
		commands    []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		mu.Lock()
		connections++
		n := connections
		mu.Unlock()

		ctx := r.Context()
		send := func(msg string) { _ = conn.Write(ctx, websocket.MessageText, []byte(msg)) }
		if n == 1 {
			send(`{"t":0,"frame":0,"type":"session_start","delivery":"immediate"}`)
			send(`не json`)
			send(`{"t":1,"frame":25,"type":"shot_start","shot":1}`)
		} else {
			send(`{"t":2,"frame":50,"type":"shot_end","shot":1}`)
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		mu.Lock()
		commands = append(commands, string(data))
		mu.Unlock()
	}))
	defer srv.Close()

	sink := &recorder{fail: 1}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	m := New([]config.Detector{{Stream: "t1", URL: url}}, sink, 30*time.Second, log)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	if err := m.Send(ctx, "nope", []byte(`{}`)); err != ErrUnknownStream {
		t.Fatalf("неизвестный поток: %v", err)
	}

	// Первая попытка записи падает — пачка повторяется и доходит.
	waitFor(t, "события первого подключения", func() bool { return len(sink.snapshot()) == 2 })
	if got := sink.snapshot(); got[0] != "t1:session_start" || got[1] != "t1:shot_start" {
		t.Fatalf("события: %v", got)
	}
	st := m.Statuses()
	if len(st) != 1 || !st[0].Connected || st[0].Events != 2 {
		t.Fatalf("состояние: %+v", st)
	}

	if err := m.Send(ctx, "t1", []byte(`{"cmd":"set_target","ball":"red_03","pocket":"tr"}`)); err != nil {
		t.Fatalf("отправка команды: %v", err)
	}
	// Сервер закрыл соединение после команды; клиент переподключается сам.
	waitFor(t, "событие после переподключения", func() bool { return len(sink.snapshot()) == 3 })
	if got := sink.snapshot(); got[2] != "t1:shot_end" {
		t.Fatalf("события: %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 1 || commands[0] != `{"cmd":"set_target","ball":"red_03","pocket":"tr"}` {
		t.Fatalf("команды: %v", commands)
	}
}
