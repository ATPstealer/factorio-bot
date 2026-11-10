package stage

import (
	"slices"
	"strings"
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
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

// Ряд шины начинается подземкой (голова ряда — вход левее первой ленты ряда, под подводами):
// быстрые — и вход, и выход пары (стадия 42 на 778631482: вход остался жёлтым, быстрый выход не
// сцепился и встал входом — «конец input, в плане output»).
func TestPlanTiersFastRowsHeadUnderground(t *testing.T) {
	smelt := func(name string) *cluster.Cluster {
		return &cluster.Cluster{Name: name, Kind: cluster.Smelt, Product: "copper-plate",
			Out: []cluster.Flow{{Item: "copper-plate", Rate: 12.5}}, Ports: []cluster.Port{{Item: "copper-plate"}}}
	}
	pl := func(name, tag, recipe string, x float64, dir int) layout.Placement {
		return layout.Placement{Name: name, Pos: bridge.Pos{X: x, Y: 10.5}, Dir: dir, Recipe: recipe, Tag: tag}
	}
	plan := &ClusterPlan{
		Clusters: []*cluster.Cluster{smelt("copper-1"), smelt("copper-2")},
		Layout: []layout.Placement{
			pl("underground-belt", "c/bus-2", "input", 0.5, layout.E),
			pl("transport-belt", "c/logistics/copper-1", "", 1.5, layout.S), // подвод поперёк
			pl("underground-belt", "c/bus-2", "output", 3.5, layout.E),
			pl("transport-belt", "c/bus-2", "copper-plate", 4.5, layout.E),
			pl("transport-belt", "c/bus-2", "copper-plate", 5.5, layout.E),
		},
	}
	planTiers(plan, cluster.Tier2From)
	var got []string
	for _, p := range plan.Layout {
		got = append(got, p.Name)
	}
	want := []string{"fast-underground-belt", "transport-belt", "fast-underground-belt", "fast-transport-belt", "fast-transport-belt"}
	if !slices.Equal(got, want) {
		t.Errorf("после 42: %v, ждал %v", got, want)
	}
}

// Делитель отвода потребителя стадии 55 в ряду, которому жёлтой мало, — жёлтый до 42 (ставится
// раньше своей стадии, с лентой ряда: стадия 14 на 366255770 — «рецепт fast-splitter не
// открыт»), быстрый — после 42.
func TestPlanTiersFastRowsLater(t *testing.T) {
	smelt := func(name string) *cluster.Cluster {
		return &cluster.Cluster{Name: name, Kind: cluster.Smelt, Product: "copper-plate", Stage: 17,
			Out: []cluster.Flow{{Item: "copper-plate", Rate: 12.5}}, Ports: []cluster.Port{{Item: "copper-plate"}}}
	}
	plan := &ClusterPlan{
		Clusters: []*cluster.Cluster{smelt("copper-1"), smelt("copper-2"), {Name: "battery-1", Kind: cluster.Make, Stage: 55}},
		Layout: []layout.Placement{
			{Name: "transport-belt", Pos: bridge.Pos{X: 0.5, Y: 10.5}, Dir: layout.E, Recipe: "copper-plate", Tag: "c/bus-2"},
			{Name: "splitter", Pos: bridge.Pos{X: 1.5, Y: 10}, Dir: layout.E, Tag: "c/logistics/battery-1"},
			{Name: "transport-belt", Pos: bridge.Pos{X: 2.5, Y: 10.5}, Dir: layout.E, Recipe: "copper-plate", Tag: "c/bus-2"},
		},
	}
	planTiers(plan, 0)
	if got := plan.Layout[1].Name; got != "splitter" {
		t.Errorf("до 42: делитель %s, ждал splitter", got)
	}
	planTiers(plan, cluster.Tier2From)
	if got := plan.Layout[1].Name; got != "fast-splitter" {
		t.Errorf("после 42: делитель %s, ждал fast-splitter", got)
	}
}

// Выход добычи больше 15/с (cluster.md §5): блок меди 18/с с одним выходом — лента сбора, подвод с
// подземкой и ряд bus-1 — быстрые после 42, лента ряда буров — как есть; блок угля 10/с — как есть.
func TestPlanTiersFastMine(t *testing.T) {
	const drill = "electric-mining-drill"
	mine := func(name, item string, rate float64, port world.Tile) *cluster.Cluster {
		return &cluster.Cluster{Name: name, Kind: cluster.Mine, Product: item, Units: []cluster.Unit{{Machine: drill}},
			Out: []cluster.Flow{{Item: item, Rate: rate}}, Ports: []cluster.Port{{T: port, Dir: layout.N, Item: item}}}
	}
	pl := func(name, tag, recipe string, x, y int, dir int) layout.Placement {
		return layout.Placement{Name: name, Pos: world.Tile{X: x, Y: y}.Center(), Dir: dir, Recipe: recipe, Tag: tag}
	}
	feed := cluster.LogisticsTag + "/bus-1/copper-ore-1"
	plan := &ClusterPlan{
		Clusters: []*cluster.Cluster{mine("copper-ore-1", "copper-ore", 18, world.Tile{X: 5, Y: -1}),
			mine("coal-1", "coal", 10, world.Tile{X: 25, Y: -1})},
		Layout: []layout.Placement{
			// блок меди: сбор по x = 5 к выходу (5, −1), ряд y = 3 к сбору, буры над и под рядом
			pl("transport-belt", "c/copper-ore-1", "", 5, 3, layout.N),
			pl("transport-belt", "c/copper-ore-1", "", 5, 0, layout.N),
			pl("transport-belt", "c/copper-ore-1", "", 6, 3, layout.W),
			{Name: drill, Pos: bridge.Pos{X: 7.5, Y: 1.5}, Dir: layout.S, Tag: "c/copper-ore-1"},
			{Name: drill, Pos: bridge.Pos{X: 7.5, Y: 5.5}, Dir: layout.N, Tag: "c/copper-ore-1"},
			// подвод: вверх, вправо, подземкой — в голову ряда bus-1
			pl("transport-belt", feed, "", 5, -1, layout.N),
			pl("transport-belt", feed, "", 5, -2, layout.E),
			pl("underground-belt", feed, "input", 6, -2, layout.E),
			pl("underground-belt", feed, "output", 8, -2, layout.E),
			pl("transport-belt", feed, "", 9, -2, layout.E),
			pl("transport-belt", "c/bus-1", "copper-ore", 10, -2, layout.E),
			pl("transport-belt", "c/bus-1", "copper-ore", 11, -2, layout.E),
			// блок угля: сбор и подвод в свой ряд
			pl("transport-belt", "c/coal-1", "", 25, 0, layout.N),
			{Name: drill, Pos: bridge.Pos{X: 26.5, Y: 1.5}, Dir: layout.S, Tag: "c/coal-1"},
			pl("transport-belt", cluster.LogisticsTag+"/bus-1/coal-1", "", 25, -1, layout.E),
			pl("transport-belt", "c/bus-1", "coal", 26, -1, layout.E),
		},
	}
	planTiers(plan, cluster.Tier2From)
	var got []string
	for _, p := range plan.Layout {
		got = append(got, p.Name)
	}
	f, y := "fast-transport-belt", "transport-belt"
	want := []string{f, f, y, drill, drill, f, f, "fast-underground-belt", "fast-underground-belt", f, f, f, y, drill, y, y}
	if !slices.Equal(got, want) {
		t.Errorf("после 42: %v, ждал %v", got, want)
	}
}
