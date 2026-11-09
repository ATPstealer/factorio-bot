package stage

import (
	"slices"
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
)

// Чего не хватает химзаводу серной кислоты (стадия 54 на 24996062: «по факту не работает —
// item_ingredient_shortage», а чего — железа или серы — не узнать): серы на крафт есть, железа
// нет, воды меньше крафта.
func TestShortOf(t *testing.T) {
	r := knowledge.Recipe{Ingredients: knowledge.List[knowledge.Amount]{
		{Type: "item", Name: "sulfur", Amount: 5},
		{Type: "item", Name: "iron-plate", Amount: 1},
		{Type: "fluid", Name: "water", Amount: 100},
	}}
	st := &bridge.EntityState{
		Inventories: map[string]map[string]int{"crafter_input": {"sulfur": 5}},
		Fluids:      map[string]float64{"water": 20},
	}
	if got, want := shortOf(r, st), []string{"iron-plate", "water"}; !slices.Equal(got, want) {
		t.Errorf("не хватает %v, ждал %v", got, want)
	}
}
