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
}
