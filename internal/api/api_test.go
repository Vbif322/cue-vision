package api_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Vbif322/cue-vision/internal/api"
	"github.com/Vbif322/cue-vision/internal/config"
	"github.com/Vbif322/cue-vision/internal/detector"
	"github.com/Vbif322/cue-vision/internal/event"
	"github.com/Vbif322/cue-vision/internal/ingest"
	"github.com/Vbif322/cue-vision/internal/testdb"
)

const sessionFile = "../../testdata/session_100s.jsonl"

type client struct {
	t     *testing.T
	base  string
	token string
}

func newServer(t *testing.T, token string) *client {
	t.Helper()
	st := testdb.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{APIToken: token, MaxBodyBytes: 1 << 20}
	processor := ingest.New(st.Pool(), ingest.Options{
		AutoTraining: true,
		SkipTypes:    map[string]bool{event.Positions: true},
	}, log)
	detectors := detector.New(nil, processor, time.Minute, log)
	srv := httptest.NewServer(api.New(cfg, st, processor, detectors, log).Handler())
	t.Cleanup(srv.Close)
	return &client{t: t, base: srv.URL, token: token}
}

// do выполняет запрос и разбирает JSON-ответ в out (если out не nil).
func (c *client) do(method, path string, body any, headers map[string]string, out any) int {
	c.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(b)
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: ответ %d не разобран: %v\n%s", method, path, resp.StatusCode, err, raw)
		}
	}
	return resp.StatusCode
}

func (c *client) expect(want int, method, path string, body any, out any) {
	c.t.Helper()
	var raw json.RawMessage
	got := c.do(method, path, body, nil, &raw)
	if got != want {
		c.t.Fatalf("%s %s: код %d, ожидался %d\n%s", method, path, got, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
}

type idOnly struct {
	ID int64 `json:"id"`
}

type statsEnvelope struct {
	Duration float64 `json:"duration"`
	Stats    struct {
		Shots      int64            `json:"shots"`
		Pots       int64            `json:"pots"`
		PotRate    *float64         `json:"pot_rate"`
		BestStreak int64            `json:"best_streak"`
		Warnings   map[string]int64 `json:"warnings"`
		ByCutAngle []struct {
			Label string `json:"label"`
		} `json:"by_cut_angle"`
	} `json:"stats"`
	ByDay []struct {
		Day   string `json:"day"`
		Shots int64  `json:"shots"`
	} `json:"by_day"`
}

func TestUploadAndStats(t *testing.T) {
	c := newServer(t, "")
	file, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}

	var res ingest.Result
	c.expect(http.StatusOK, "POST", "/api/v1/events?stream=club1-t1", file, &res)
	if res.Stored != 162 || res.Invalid != 0 || len(res.Sessions) != 1 {
		t.Fatalf("итог загрузки: %+v", res)
	}

	// Тот же файл, сжатый gzip: всё уже принято.
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write(file)
	_ = zw.Close()
	var again ingest.Result
	if code := c.do("POST", "/api/v1/events?stream=club1-t1", zipped.Bytes(),
		map[string]string{"Content-Encoding": "gzip"}, &again); code != http.StatusOK {
		t.Fatalf("gzip: код %d", code)
	}
	if again.Stored != 0 || again.Duplicates != 162 {
		t.Fatalf("повторная загрузка: %+v", again)
	}

	var sessions struct {
		Items []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
			Hall   string `json:"hall"`
			Shots  int64  `json:"shots"`
		} `json:"items"`
	}
	c.expect(http.StatusOK, "GET", "/api/v1/sessions?stream=club1-t1", nil, &sessions)
	if len(sessions.Items) != 1 || sessions.Items[0].Status != "ended" || sessions.Items[0].Shots != 5 {
		t.Fatalf("сессии: %+v", sessions)
	}
	sid := itoa(sessions.Items[0].ID)

	var st statsEnvelope
	c.expect(http.StatusOK, "GET", "/api/v1/sessions/"+sid+"/stats", nil, &st)
	// Удар 5 — белый забивает жёлтый (детектор принял битком жёлтый): 3 забития из 5.
	if st.Stats.Shots != 5 || st.Stats.Pots != 3 || *st.Stats.PotRate != 0.6 || len(st.Stats.ByCutAngle) != 5 {
		t.Fatalf("статистика сессии: %+v", st.Stats)
	}

	var shots struct {
		Items []struct {
			ID      int64  `json:"id"`
			Outcome string `json:"outcome"`
		} `json:"items"`
	}
	c.expect(http.StatusOK, "GET", "/api/v1/sessions/"+sid+"/shots", nil, &shots)
	if len(shots.Items) != 5 || shots.Items[1].Outcome != "pot" || shots.Items[4].Outcome != "pot" {
		t.Fatalf("удары: %+v", shots.Items)
	}
	var shot struct {
		Events []json.RawMessage `json:"events"`
		Pots   []json.RawMessage `json:"pots"`
	}
	c.expect(http.StatusOK, "GET", "/api/v1/shots/"+itoa(shots.Items[1].ID), nil, &shot)
	if len(shot.Events) == 0 || len(shot.Pots) != 1 {
		t.Fatalf("удар: событий %d, падений %d", len(shot.Events), len(shot.Pots))
	}

	var events struct {
		Items []struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
	}
	c.expect(http.StatusOK, "GET", "/api/v1/sessions/"+sid+"/events?type=ball_pocketed,shot_end&limit=3", nil, &events)
	if len(events.Items) != 3 {
		t.Fatalf("события с отбором по типу: %+v", events.Items)
	}

	// Автоматическую тренировку можно приписать игроку — и получить его статистику.
	var player idOnly
	c.expect(http.StatusCreated, "POST", "/api/v1/players", map[string]string{"name": "Иван Петров"}, &player)
	c.expect(http.StatusConflict, "POST", "/api/v1/players", map[string]string{"name": "иван петров"}, nil)

	var trainings struct {
		Items []struct {
			ID     int64  `json:"id"`
			Origin string `json:"origin"`
			Shots  int64  `json:"shots"`
		} `json:"items"`
	}
	c.expect(http.StatusOK, "GET", "/api/v1/trainings?stream=club1-t1", nil, &trainings)
	if len(trainings.Items) != 1 || trainings.Items[0].Origin != "auto" || trainings.Items[0].Shots != 5 {
		t.Fatalf("тренировки: %+v", trainings.Items)
	}
	tid := itoa(trainings.Items[0].ID)
	var patched struct {
		PlayerName string `json:"player_name"`
		Title      string `json:"title"`
	}
	c.expect(http.StatusOK, "PATCH", "/api/v1/trainings/"+tid,
		map[string]any{"player_id": player.ID, "title": "Свободная игра"}, &patched)
	if patched.PlayerName != "Иван Петров" || patched.Title != "Свободная игра" {
		t.Fatalf("тренировка после правки: %+v", patched)
	}
	c.expect(http.StatusBadRequest, "PATCH", "/api/v1/trainings/"+tid, map[string]any{"stream": "x"}, nil)
	c.expect(http.StatusNotFound, "PATCH", "/api/v1/trainings/"+tid, map[string]any{"player_id": 999999}, nil)

	var tst statsEnvelope
	c.expect(http.StatusOK, "GET", "/api/v1/trainings/"+tid+"/stats", nil, &tst)
	if tst.Stats.Shots != 5 || tst.Stats.BestStreak != 2 || tst.Duration <= 90 || tst.Duration >= 100 {
		t.Fatalf("статистика тренировки: %+v, длительность %v", tst.Stats, tst.Duration)
	}

	var pst statsEnvelope
	c.expect(http.StatusOK, "GET", "/api/v1/players/"+itoa(player.ID)+"/stats?tz=Europe/Moscow&from=2026-11-01", nil, &pst)
	if pst.Stats.Shots != 5 || len(pst.ByDay) != 1 || pst.ByDay[0].Day != "2026-11-08" {
		t.Fatalf("статистика игрока: %+v по дням %+v", pst.Stats, pst.ByDay)
	}
	c.expect(http.StatusOK, "GET", "/api/v1/players/"+itoa(player.ID)+"/stats?to=2026-11-01", nil, &pst)
	if pst.Stats.Shots != 0 {
		t.Fatalf("статистика вне периода: %+v", pst.Stats)
	}
	c.expect(http.StatusBadRequest, "GET", "/api/v1/players/"+itoa(player.ID)+"/stats?tz=Mars/Olympus", nil, nil)
}

func TestTrainingLifecycle(t *testing.T) {
	c := newServer(t, "")

	var tr struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Stream string `json:"stream"`
	}
	sessionStart := time.Now().UTC().Add(-30 * time.Second)
	c.expect(http.StatusCreated, "POST", "/api/v1/trainings", map[string]any{
		"stream": "t2", "title": "Дальние удары", "exercise": "long-pots",
		"started_at": sessionStart.Add(-time.Minute),
	}, &tr)
	if tr.Status != "active" || tr.Stream != "t2" {
		t.Fatalf("новая тренировка: %+v", tr)
	}
	c.expect(http.StatusConflict, "POST", "/api/v1/trainings", map[string]any{"stream": "t2"}, nil)
	c.expect(http.StatusNotFound, "POST", "/api/v1/trainings", map[string]any{"stream": "t3", "player_id": 42}, nil)
	c.expect(http.StatusBadRequest, "POST", "/api/v1/trainings", map[string]any{"stream": "плохое имя"}, nil)

	// События, пришедшие во время тренировки, попадают в неё.
	lines := []string{
		`{"t":0,"frame":0,"type":"session_start","source_kind":"live","started_at":"` + sessionStart.Format(time.RFC3339Nano) + `","delivery":"immediate"}`,
		`{"t":1.0,"frame":25,"type":"shot_start","shot":1}`,
		`{"t":1.0,"frame":25,"type":"cue_strike","ball":"white","speed":1.5,"direction":10,"source":"cue"}`,
		`{"t":1.6,"frame":40,"type":"ball_pocketed","ball":"red_01","pocket":"tr","entry_speed":0.8}`,
		`{"t":3.0,"frame":75,"type":"shot_end","shot":1,"duration":2.0,"first_contact":"red_01","pocketed":["red_01"],"cushions":0}`,
	}
	var res ingest.Result
	c.expect(http.StatusOK, "POST", "/api/v1/events?stream=t2&realtime=true", strings.Join(lines, "\n"), &res)
	if res.Stored != 5 {
		t.Fatalf("итог приёма: %+v", res)
	}

	id := itoa(tr.ID)
	var finished struct {
		Status string `json:"status"`
		Shots  int64  `json:"shots"`
		Pots   int64  `json:"pots"`
	}
	c.expect(http.StatusOK, "POST", "/api/v1/trainings/"+id+"/finish", nil, &finished)
	if finished.Status != "finished" || finished.Shots != 1 || finished.Pots != 1 {
		t.Fatalf("законченная тренировка: %+v", finished)
	}
	// Повторное завершение ничего не меняет, новую тренировку открыть можно.
	c.expect(http.StatusOK, "POST", "/api/v1/trainings/"+id+"/finish", nil, nil)
	c.expect(http.StatusCreated, "POST", "/api/v1/trainings", map[string]any{"stream": "t2"}, nil)

	c.expect(http.StatusNotFound, "GET", "/api/v1/trainings/999999/stats", nil, nil)
	c.expect(http.StatusBadRequest, "GET", "/api/v1/trainings/abc", nil, nil)
}

func TestIngestRejectsGarbage(t *testing.T) {
	c := newServer(t, "")
	var bad struct {
		Error  string            `json:"error"`
		Errors []event.LineError `json:"errors"`
	}
	c.expect(http.StatusBadRequest, "POST", "/api/v1/events", "not json\n{\"type\":\"state\"}\n", &bad)
	if len(bad.Errors) != 2 || bad.Errors[1].Line != 2 {
		t.Fatalf("ответ на мусор: %+v", bad)
	}
	c.expect(http.StatusBadRequest, "POST", "/api/v1/events", "", nil)
	c.expect(http.StatusBadRequest, "POST", "/api/v1/events?stream=a/b", `{"t":0,"type":"state"}`, nil)

	// Плохая строка среди хороших не мешает принять остальные.
	var res ingest.Result
	c.expect(http.StatusOK, "POST", "/api/v1/events",
		"{\"t\":0,\"frame\":0,\"type\":\"state\",\"balls\":[]}\noops\n{\"t\":1,\"frame\":25,\"type\":\"positions\",\"balls\":[]}\n", &res)
	if res.Received != 3 || res.Stored != 1 || res.Skipped != 1 || res.Invalid != 1 || len(res.Errors) != 1 {
		t.Fatalf("частично пригодный поток: %+v", res)
	}

	// Тело чуть больше предела в 1 МиБ.
	big := strings.Repeat(`{"t":0,"frame":0,"type":"state","pad":"`+strings.Repeat("x", 1000)+`"}`+"\n", 1050)
	c.expect(http.StatusRequestEntityTooLarge, "POST", "/api/v1/events", big, nil)
}

func TestAuthAndDetectors(t *testing.T) {
	c := newServer(t, "secret")
	anonymous := &client{t: t, base: c.base}
	anonymous.expect(http.StatusUnauthorized, "GET", "/api/v1/sessions", nil, nil)
	anonymous.expect(http.StatusOK, "GET", "/healthz", nil, nil)
	anonymous.expect(http.StatusOK, "GET", "/readyz", nil, nil)
	c.expect(http.StatusOK, "GET", "/api/v1/sessions", nil, nil)

	var detectors struct {
		Items []json.RawMessage `json:"items"`
	}
	c.expect(http.StatusOK, "GET", "/api/v1/detectors", nil, &detectors)
	if len(detectors.Items) != 0 {
		t.Fatalf("детекторы: %s", detectors.Items)
	}
	c.expect(http.StatusNotFound, "POST", "/api/v1/detectors/t1/commands", map[string]string{"cmd": "recalibrate"}, nil)
	c.expect(http.StatusBadRequest, "POST", "/api/v1/detectors/t1/commands", map[string]string{"cmd": "self_destruct"}, nil)
	c.expect(http.StatusBadRequest, "POST", "/api/v1/detectors/t1/commands", map[string]string{"cmd": "set_target", "pocket": "zz"}, nil)
}

func itoa(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}
