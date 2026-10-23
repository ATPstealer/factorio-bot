package cluster

import (
	"sync"
	"testing"

	"factorio-bot/internal/calc"
	"factorio-bot/internal/knowledge"
)

var (
	protos *knowledge.Prototypes
	once   sync.Once
)

func pt(t *testing.T) *knowledge.Prototypes {
	t.Helper()
	once.Do(func() {
		var err error
		protos, err = knowledge.Load("../knowledge/testdata/prototypes.json")
		if err != nil {
			t.Fatal(err)
		}
	})
	return protos
}

func redPlan(t *testing.T, rate float64) *calc.Plan {
	t.Helper()
	plan, err := calc.Solve(pt(t), map[string]float64{"automation-science-pack": rate}, calc.Options{All: true,
		Machines: map[string]string{"crafting": "assembling-machine-1", "smelting": "stone-furnace"}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// Красная наука 0.1/с: добыча железа, меди, угля; плавка; наука (шестерни внутри); станция.
func TestSplitRed(t *testing.T) {
	cs, err := Split(pt(t), redPlan(t, 0.1), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cs, err = AddPower(pt(t), cs, 5, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]*Cluster{}
	for _, c := range cs {
		got[string(c.Kind)+":"+c.Product] = c
		t.Log(c)
	}
	for _, k := range []string{"mine:iron-ore", "mine:copper-ore", "mine:coal", "smelt:iron-plate", "smelt:copper-plate", "make:automation-science-pack", "power:power"} {
		if got[k] == nil {
			t.Errorf("нет кластера %s", k)
		}
	}
	if c := got["make:automation-science-pack"]; c != nil {
		// шестерни — парой прямой подачи внутри: на входе железо и медь
		if c.Sub != "iron-gear-wheel" || !hasIn(c, "iron-plate") || !hasIn(c, "copper-plate") {
			t.Errorf("пара науки: %v (sub %q)", c, c.Sub)
		}
		for _, f := range c.Out {
			if f.Item == "automation-science-pack" && f.Rate < 0.1*Margin-1e-6 {
				t.Errorf("out без запаса: %v", f)
			}
		}
	}
	if c := got["smelt:iron-plate"]; c != nil && !hasIn(c, "coal") {
		t.Errorf("печам нужен уголь: %v", c)
	}
	if c := got["mine:coal"]; c != nil && c.Count < 1 {
		t.Errorf("уголь: %v", c)
	}
}

func hasIn(c *Cluster, item string) bool {
	for _, f := range c.In {
		if f.Item == item {
			return true
		}
	}
	return false
}
