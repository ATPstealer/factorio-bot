package knowledge

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

const sample = `{
 "game_version": "2.0.77",
 "recipes": {
  "iron-gear-wheel": {"category": "crafting", "energy": 0.5, "ingredients": [{"type": "item", "name": "iron-plate", "amount": 2}], "products": [{"type": "item", "name": "iron-gear-wheel", "amount": 1}], "enabled": true},
  "iron-plate": {"category": "smelting", "energy": 3.2, "ingredients": [{"name": "iron-ore", "amount": 1}], "products": [{"name": "iron-plate", "amount": 1}], "enabled": true},
  "assembling-machine-1": {"category": "crafting", "energy": 0.5, "ingredients": [], "products": [{"name": "assembling-machine-1", "amount": 1}]},
  "secret": {"products": [{"name": "iron-plate", "amount": 1}], "hidden": true}
 },
 "technologies": {
  "automation": {"prerequisites": ["automation-science-pack"], "unit_count": 10, "unit_time": 10, "unit_ingredients": [{"name": "automation-science-pack", "amount": 1}], "unlocks": ["assembling-machine-1"]},
  "electronics": {"unit_count": 1, "unit_ingredients": {}, "trigger": {"type": "craft-item", "item": "copper-plate", "count": 10}}
 }
}`

func load(t *testing.T) *Prototypes {
	t.Helper()
	var p Prototypes
	if err := json.Unmarshal([]byte(sample), &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestRecipesFor(t *testing.T) {
	p := load(t)
	if got := p.RecipesFor("iron-plate"); !reflect.DeepEqual(got, []string{"iron-plate"}) {
		t.Errorf("RecipesFor(iron-plate) = %v (скрытые рецепты не нужны)", got)
	}
	if got := p.RecipesFor("nothing"); len(got) != 0 {
		t.Errorf("RecipesFor(nothing) = %v", got)
	}
}

func TestTechFor(t *testing.T) {
	p := load(t)
	if got := p.TechFor("assembling-machine-1"); got != "automation" {
		t.Errorf("TechFor = %q", got)
	}
	if got := p.TechFor("iron-gear-wheel"); got != "" {
		t.Errorf("TechFor(доступен изначально) = %q", got)
	}
}

func TestTriggerTech(t *testing.T) {
	e := load(t).Technologies["electronics"]
	if e.Trigger == nil || e.Trigger.Item != "copper-plate" || e.Trigger.Count != 10 {
		t.Errorf("trigger = %+v", e.Trigger)
	}
	if len(e.UnitIngredients) != 0 {
		t.Errorf("unit_ingredients = %v", e.UnitIngredients)
	}
}

func TestAmountMean(t *testing.T) {
	cases := map[Amount]float64{
		{Amount: 2}:                    2,
		{AmountMin: 24, AmountMax: 50}: 37,
		{Amount: 4, Probability: 0.5}:  2,
	}
	for a, want := range cases {
		if got := a.Mean(); got != want {
			t.Errorf("%+v.Mean() = %v, ждали %v", a, got, want)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	p := load(t)
	path := CachePath(t.TempDir(), p.GameVersion)
	if filepath.Base(path) != "prototypes-2.0.77.json" {
		t.Fatalf("path = %s", path)
	}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	q, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, q) {
		t.Error("после Save/Load справочник отличается")
	}
}
