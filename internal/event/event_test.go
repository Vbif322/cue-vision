package event

import (
	"bytes"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	ev, err := Parse([]byte(` {"t": 41.28, "frame": 2064, "type": "ball_collision", "conf": 0.9, "delivery": "immediate"} `))
	if err != nil {
		t.Fatal(err)
	}
	if ev.T != 41.28 || ev.Frame != 2064 || ev.Type != "ball_collision" || !ev.Immediate {
		t.Fatalf("общие поля разобраны неверно: %+v", ev)
	}
	if ev.Conf == nil || *ev.Conf != 0.9 {
		t.Fatalf("conf = %v", ev.Conf)
	}
	if want := `{"t":41.28,"frame":2064,"type":"ball_collision","conf":0.9,"delivery":"immediate"}`; string(ev.Raw) != want {
		t.Fatalf("Raw = %s", ev.Raw)
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string]string{
		"не объект":         `[1,2]`,
		"битый JSON":        `{"t":1,`,
		"нет type":          `{"t":1,"frame":0}`,
		"пустой type":       `{"t":1,"type":""}`,
		"нет t":             `{"type":"state"}`,
		"отрицательное t":   `{"t":-1,"type":"state"}`,
		"нулевой символ":    `{"t":1,"type":"state","note":"a\u0000b"}`,
		"t не число":        `{"t":"1","type":"state"}`,
		"пустая строка":     ``,
		"хвост за объектом": `{"t":1,"type":"state"} {"t":2,"type":"state"}`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: ожидалась ошибка для %q", name, in)
		}
	}
}

func TestHashIgnoresFormatting(t *testing.T) {
	a, _ := Parse([]byte(`{"t":1.50,"frame":3,"type":"ball_stop","ball":"white","x":0.1}`))
	b, _ := Parse([]byte(`{"type": "ball_stop", "ball": "white", "x": 0.1, "frame": 3, "t": 1.50}`))
	c, _ := Parse([]byte(`{"t":1.50,"frame":3,"type":"ball_stop","ball":"white","x":0.2}`))
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	hc, _ := c.Hash()
	if !bytes.Equal(ha, hb) {
		t.Error("порядок ключей и пробелы не должны менять отпечаток")
	}
	if bytes.Equal(ha, hc) {
		t.Error("разное содержимое должно давать разный отпечаток")
	}
}

func TestReadAllLines(t *testing.T) {
	in := "\xEF\xBB\xBF" + `{"t":0,"frame":0,"type":"session_start"}

not json
{"t":1,"frame":25,"type":"state","balls":[]}
{"frame":1,"type":"state"}
`
	events, bad, err := ReadAll(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != SessionStart || events[1].Line != 4 {
		t.Fatalf("события: %+v", events)
	}
	if len(bad) != 2 || bad[0].Line != 3 || bad[1].Line != 5 {
		t.Fatalf("плохие строки: %+v", bad)
	}
}

func TestReadAllArray(t *testing.T) {
	events, bad, err := ReadAll(strings.NewReader(` [{"t":0,"type":"a"}, {"type":"b"}, {"t":2,"type":"c"}] `))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != "c" || events[1].Line != 3 {
		t.Fatalf("события: %+v", events)
	}
	if len(bad) != 1 || bad[0].Line != 2 {
		t.Fatalf("плохие элементы: %+v", bad)
	}
}

func TestReadAllEmpty(t *testing.T) {
	events, bad, err := ReadAll(strings.NewReader("  \n\n"))
	if err != nil || len(events) != 0 || len(bad) != 0 {
		t.Fatalf("пустой вход: %v %v %v", events, bad, err)
	}
}

func TestDecodeShotEnd(t *testing.T) {
	ev, err := Parse([]byte(`{"t":44.02,"frame":2201,"type":"shot_end","shot":17,"duration":3.04,
		"first_contact":"red_07","contacts":["white-red_07"],"pocketed":["red_07"],"cushions":2,
		"geometry":{"cue_to_object":0.318,"object_to_pocket":0.204,"cut_angle":34.7,"obstructed":false},
		"near_miss":{"ball":"red_07","pocket":"tr","distance":0.021,"side":"right","conf":0.72},
		"declared_target":{"ball":"red_07","pocket":"br"},"future_field":{"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	var d ShotEndData
	if err := ev.Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d.Shot != 17 || d.FirstContact != "red_07" || d.Cushions != 2 || len(d.Pocketed) != 1 {
		t.Fatalf("сводка: %+v", d)
	}
	if d.Geometry == nil || *d.Geometry.CutAngle != 34.7 || *d.Geometry.Obstructed {
		t.Fatalf("geometry: %+v", d.Geometry)
	}
	if d.NearMiss == nil || d.NearMiss.Side != "right" || d.DeclaredTarget.Pocket != "br" {
		t.Fatalf("near_miss/target: %+v %+v", d.NearMiss, d.DeclaredTarget)
	}
}
