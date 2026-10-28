package stage

import (
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стройка по участкам (stages.md, общее): участок — чанк, порядок внутри — как в списке;
// подземная пара, пересекающая границу чанка, — целиком в участке входа.
func TestBuildRegions(t *testing.T) {
	pl := func(name string, x, y float64, dir int, recipe string) layout.Placement {
		return layout.Placement{Name: name, Pos: bridge.Pos{X: x, Y: y}, Dir: dir, Recipe: recipe}
	}
	list := []layout.Placement{
		pl("inserter", 1.5, 1.5, 0, ""),
		pl("inserter", 40.5, 1.5, 0, ""),
		pl("transport-belt", 2.5, 1.5, 4, ""),
		pl("underground-belt", 30.5, 5.5, layout.E, "input"), // вход в чанке 0
		pl("underground-belt", 34.5, 5.5, layout.E, "output"), // выход — уже в чанке 1
		pl("transport-belt", 41.5, 1.5, 4, ""),
	}
	regions, keys := buildRegions(list)
	if len(keys) != 2 {
		t.Fatalf("участков %d, ждали 2: %v", len(keys), keys)
	}
	a, b := regions[world.Tile{X: 0, Y: 0}], regions[world.Tile{X: 1, Y: 0}]
	if len(a) != 4 || len(b) != 2 {
		t.Fatalf("в участках %d и %d построек, ждали 4 и 2", len(a), len(b))
	}
	if a[0].Name != "inserter" || a[1].Name != "transport-belt" {
		t.Fatalf("порядок в участке нарушен: %v", a)
	}
	ug := 0
	for _, p := range a {
		if p.Name == "underground-belt" {
			ug++
		}
	}
	if ug != 2 {
		t.Fatalf("подземная пара разорвана: в участке входа %d из 2", ug)
	}
}
