package ingest

import "strings"

// Исходы удара.
const (
	OutcomePot       = "pot"        // прицельный шар забит, биток на столе
	OutcomeMiss      = "miss"       // касание было, результата нет
	OutcomeNoContact = "no_contact" // биток не задел ни одного шара
	OutcomeInOff     = "in_off"     // биток упал в лузу
)

// ShotFacts — то, что известно об ударе к моменту shot_end.
type ShotFacts struct {
	// CueBall — шар, по которому пришёлся cue_strike; пусто, если удар кием не замечен.
	CueBall      string
	FirstContact string
	Pocketed     []string
	// TargetBall и TargetPocket — заказ (declared_target); любое поле может быть пустым.
	TargetBall   string
	TargetPocket string
	// PocketOf — в какую лузу упал шар, по событиям ball_pocketed.
	PocketOf map[string]string
}

// Verdict — оценка удара для статистики.
type Verdict struct {
	Outcome      string
	Success      bool
	CuePotted    bool
	ObjectPotted int
}

// GameSnooker — значение поля game в session_start для снукера.
const GameSnooker = "snooker"

// IsWhite отвечает, белый ли это шар: в стандартном наборе это white, в
// нестандартных — white_NN.
func IsWhite(id string) bool {
	return id == "white" || strings.HasPrefix(id, "white_")
}

// IsCueBall отвечает, является ли шар id битком в ударе по шару cueBall. Когда удар
// кием не замечен, битком считается белый.
func IsCueBall(id, cueBall string) bool {
	if cueBall != "" {
		return id == cueBall
	}
	return IsWhite(id)
}

// CueBallFor — каким шаром считать биток удара, если cue_strike пришёл по шару
// struck в игре game. В снукере биток всегда белый: cue_strike по цветному значит,
// что детектор принял за удар чужое движение (шар катнули рукой или кием
// помощника) или потерял белый в смазе. Тогда возвращается пустая строка, и
// битком считается белый (IsCueBall). В других играх, например в пирамиде,
// битком бывает любой шар, и детектору верим.
func CueBallFor(game, struck string) string {
	if game == GameSnooker && !IsWhite(struck) {
		return ""
	}
	return struck
}

// Classify оценивает удар. Без заказа результативным считается удар, в котором
// упал хотя бы один прицельный шар; с заказом — только если упал заказанный шар
// и, когда луза названа и известна, именно в неё. Упавший биток обнуляет
// результат в обоих случаях.
func Classify(f ShotFacts) Verdict {
	var v Verdict
	for _, id := range f.Pocketed {
		if IsCueBall(id, f.CueBall) {
			v.CuePotted = true
		} else {
			v.ObjectPotted++
		}
	}

	potted := v.ObjectPotted > 0
	switch {
	case f.TargetBall != "":
		potted = contains(f.Pocketed, f.TargetBall)
		if potted && f.TargetPocket != "" {
			// Лузы может не быть в данных: тогда верим списку упавших.
			if pocket, known := f.PocketOf[f.TargetBall]; known {
				potted = pocket == f.TargetPocket
			}
		}
	case f.TargetPocket != "":
		potted = false
		for _, id := range f.Pocketed {
			if !IsCueBall(id, f.CueBall) && f.PocketOf[id] == f.TargetPocket {
				potted = true
				break
			}
		}
	}
	v.Success = potted && !v.CuePotted

	switch {
	case v.CuePotted:
		v.Outcome = OutcomeInOff
	case v.Success:
		v.Outcome = OutcomePot
	case f.FirstContact == "" && v.ObjectPotted == 0:
		v.Outcome = OutcomeNoContact
	default:
		v.Outcome = OutcomeMiss
	}
	return v
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
