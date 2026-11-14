package ingest

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		facts ShotFacts
		want  Verdict
	}{
		{
			name:  "забит прицельный",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_01", Pocketed: []string{"red_01"}},
			want:  Verdict{Outcome: OutcomePot, Success: true, ObjectPotted: 1},
		},
		{
			name:  "касание без результата",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_01"},
			want:  Verdict{Outcome: OutcomeMiss},
		},
		{
			name:  "биток никого не задел",
			facts: ShotFacts{CueBall: "white"},
			want:  Verdict{Outcome: OutcomeNoContact},
		},
		{
			name:  "биток упал вместе с прицельным",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_01", Pocketed: []string{"red_01", "white"}},
			want:  Verdict{Outcome: OutcomeInOff, CuePotted: true, ObjectPotted: 1},
		},
		{
			name:  "биток нестандартного набора",
			facts: ShotFacts{CueBall: "white_02", FirstContact: "blue", Pocketed: []string{"blue"}},
			want:  Verdict{Outcome: OutcomePot, Success: true, ObjectPotted: 1},
		},
		{
			name:  "удар кием не замечен: битком считается белый",
			facts: ShotFacts{FirstContact: "red_01", Pocketed: []string{"white_01"}},
			want:  Verdict{Outcome: OutcomeInOff, CuePotted: true},
		},
		{
			name: "заказ выполнен",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_03", Pocketed: []string{"red_03"},
				TargetBall: "red_03", TargetPocket: "tr", PocketOf: map[string]string{"red_03": "tr"}},
			want: Verdict{Outcome: OutcomePot, Success: true, ObjectPotted: 1},
		},
		{
			name: "заказанный шар упал не в ту лузу",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_03", Pocketed: []string{"red_03"},
				TargetBall: "red_03", TargetPocket: "tr", PocketOf: map[string]string{"red_03": "br"}},
			want: Verdict{Outcome: OutcomeMiss, ObjectPotted: 1},
		},
		{
			name: "упал не заказанный шар",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_03", Pocketed: []string{"red_05"},
				TargetBall: "red_03"},
			want: Verdict{Outcome: OutcomeMiss, ObjectPotted: 1},
		},
		{
			name: "заказана только луза",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_05", Pocketed: []string{"red_05"},
				TargetPocket: "bm", PocketOf: map[string]string{"red_05": "bm"}},
			want: Verdict{Outcome: OutcomePot, Success: true, ObjectPotted: 1},
		},
		{
			name: "луза заказанного шара неизвестна: верим списку упавших",
			facts: ShotFacts{CueBall: "white", FirstContact: "red_03", Pocketed: []string{"red_03"},
				TargetBall: "red_03", TargetPocket: "tr", PocketOf: map[string]string{}},
			want: Verdict{Outcome: OutcomePot, Success: true, ObjectPotted: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.facts); got != tc.want {
				t.Fatalf("получено %+v, ожидалось %+v", got, tc.want)
			}
		})
	}
}

func TestCueBallFor(t *testing.T) {
	cases := []struct{ game, struck, want string }{
		{"snooker", "white", "white"},
		{"snooker", "white_02", "white_02"},
		{"snooker", "red_03", ""},
		{"snooker", "", ""},
		{"", "yellow", "yellow"},
		{"pyramid", "red_01", "red_01"},
	}
	for _, tc := range cases {
		if got := CueBallFor(tc.game, tc.struck); got != tc.want {
			t.Errorf("CueBallFor(%q, %q) = %q, ожидалось %q", tc.game, tc.struck, got, tc.want)
		}
	}
	// Удар открыт по красному, а упал белый: в снукере это in-off, а не забитие.
	v := Classify(ShotFacts{CueBall: CueBallFor("snooker", "red"), Pocketed: []string{"white"}})
	if v.Outcome != OutcomeInOff || !v.CuePotted || v.ObjectPotted != 0 {
		t.Errorf("белый в лузе после cue_strike по красному: %+v", v)
	}
}
