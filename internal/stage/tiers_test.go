package stage

import (
	"slices"
	"strings"
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Уровни построек плана по стадиям (stages.md, стадии 22 и 42): в новом плане — по стадии
// постройки (инсертер стадии 30 — быстрый, сборщик стадии 30 — первого уровня, сборщик стадии 44 —
// второго); после стадии 42 — всё прошлое второго уровня; длинный инсертер — как есть.
func TestPlanTiers(t *testing.T) {
	pl := func(name, tag string, x float64) layout.Placement {
		return layout.Placement{Name: name, Pos: bridge.Pos{X: x, Y: 0.5}, Tag: tag}
	}
	plan := &ClusterPlan{
		Clusters: []*cluster.Cluster{
			{Name: "red", Kind: cluster.Make, Stage: 20},
			{Name: "belt", Kind: cluster.Make, Stage: 30},
			{Name: "rail", Kind: cluster.Make, Stage: 44},
		},
		Layout: []layout.Placement{
			pl("inserter", "c/red", 0.5),
			pl("inserter", "c/belt", 1.5),
			pl("assembling-machine-1", "c/belt", 3.5),
			pl("assembling-machine-1", "c/rail", 7.5),
			pl("long-handed-inserter", "c/belt", 9.5),
		},
	}
	names := func() []string {
		var res []string
		for _, p := range plan.Layout {
			res = append(res, p.Name)
		}
		return res
	}
	want := func(step string, w ...string) {
		t.Helper()
		got := names()
		for i := range w {
			if got[i] != w[i] {
				t.Errorf("%s: %v, ждал %v", step, got, w)
				return
			}
		}
	}
	planTiers(plan, 0)
	want("новый план", "inserter", "fast-inserter", "assembling-machine-1", "assembling-machine-2", "long-handed-inserter")
	planTiers(plan, cluster.FastFrom)
	want("после 22", "fast-inserter", "fast-inserter", "assembling-machine-1", "assembling-machine-2", "long-handed-inserter")
	planTiers(plan, cluster.Tier2From)
	want("после 42", "fast-inserter", "fast-inserter", "assembling-machine-2", "assembling-machine-2", "long-handed-inserter")
}

// Ряд шины, которому жёлтой мало (cluster.md §5): медь bus-2 — 25/с (две плавки по 12.5) —
// лента, подземка и делители ряда — быстрые после стадии 42; железо 6.25/с, чужая лента поперёк
// ряда меди и делитель вне ряда — как есть.
func TestPlanTiersFastRows(t *testing.T) {
	smelt := func(name, item string, rate float64) *cluster.Cluster {
		return &cluster.Cluster{Name: name, Kind: cluster.Smelt, Product: item,
			Out: []cluster.Flow{{Item: item, Rate: rate}}, Ports: []cluster.Port{{Item: item}}}
	}
	pl := func(name, tag, recipe string, x, y float64, dir int) layout.Placement {
		return layout.Placement{Name: name, Pos: bridge.Pos{X: x, Y: y}, Dir: dir, Recipe: recipe, Tag: tag}
	}
	plan := &ClusterPlan{
		Clusters: []*cluster.Cluster{smelt("copper-1", "copper-plate", 12.5), smelt("copper-2", "copper-plate", 12.5),
			smelt("iron-1", "iron-plate", 6.25)},
		Layout: []layout.Placement{
			pl("transport-belt", "c/bus-2", "copper-plate", 0.5, 10.5, layout.E),
			pl("underground-belt", "c/bus-2", "input", 1.5, 10.5, layout.E),
			pl("transport-belt", "c/logistics/circuit-1", "", 2.5, 10.5, layout.S), // поперёк над подземкой
			pl("underground-belt", "c/bus-2", "output", 3.5, 10.5, layout.E),
			pl("splitter", "c/logistics/circuit-1", "", 4.5, 10, layout.E), // отвод: ряды 9 и 10
			pl("transport-belt", "c/bus-2", "copper-plate", 5.5, 10.5, layout.E),
			pl("transport-belt", "c/bus-2", "iron-plate", 0.5, 12.5, layout.E),
			pl("splitter", "c/logistics/gear-1", "", 3.5, 13, layout.E), // отвод железа: ряды 12 и 13
		},
	}
	names := func() []string {
		var res []string
		for _, p := range plan.Layout {
			res = append(res, p.Name)
		}
		return res
	}
	planTiers(plan, 0)
	if got := names(); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "fast-") }) {
		t.Errorf("до 42: %v", got)
	}
	planTiers(plan, cluster.Tier2From)
	want := []string{"fast-transport-belt", "fast-underground-belt", "transport-belt", "fast-underground-belt",
		"fast-splitter", "fast-transport-belt", "transport-belt", "splitter"}
	if got := names(); !slices.Equal(got, want) {
		t.Errorf("после 42: %v, ждал %v", got, want)
	}
}
