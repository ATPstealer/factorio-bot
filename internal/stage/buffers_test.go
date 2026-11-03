package stage

import (
	"testing"

	"factorio-bot/internal/knowledge"
)

// Нужное на стройку стадии из буферов — крафтом до продуктов буферов, максимум по стадиям:
// инсертер — схема, шестерня (2 пластины) и пластина; лента — (шестерня + пластина) на две.
func TestBuildNeeds(t *testing.T) {
	p, err := knowledge.Load("../knowledge/testdata/prototypes.json")
	if err != nil {
		t.Fatal(err)
	}
	need := buildNeeds(p, map[int]map[string]float64{
		11: {"inserter": 10},
		12: {"inserter": 5, "transport-belt": 20},
	}, map[string]bool{"electronic-circuit": true, "iron-plate": true})
	if need["electronic-circuit"] != 10 || need["iron-plate"] != 45 {
		t.Errorf("нужно %v, ждал схем 10 (стадия 11) и пластин 45 (стадия 12: 15 + 30)", need)
	}
	// спутник (стадия 67) — как постройка: 100 аккумуляторов — 500 батарей, 100 панелей — 500
	// стали, процессоры и ЛПС — по 100, ракетное топливо — 50
	need = buildNeeds(p, map[int]map[string]float64{satelliteStage: {"satellite": 1}},
		map[string]bool{"battery": true, "steel-plate": true, "processing-unit": true, "low-density-structure": true, "rocket-fuel": true})
	if need["battery"] != 500 || need["steel-plate"] != 500 || need["processing-unit"] != 100 ||
		need["low-density-structure"] != 100 || need["rocket-fuel"] != 50 {
		t.Errorf("спутник: нужно %v", need)
	}
}
