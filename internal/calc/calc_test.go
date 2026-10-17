package calc

import (
	"math"
	"slices"
	"sync"
	"testing"

	"factorio-bot/internal/knowledge"
)

var (
	protoOnce sync.Once
	protos    *knowledge.Prototypes
)

// Снимок справочника 2.0.77 (internal/knowledge/testdata) — расчёты сверяются с известными
// числами игры.
func pt(t *testing.T) *knowledge.Prototypes {
	t.Helper()
	protoOnce.Do(func() {
		var err error
		protos, err = knowledge.Load("../knowledge/testdata/prototypes.json")
		if err != nil {
			t.Fatal(err)
		}
	})
	return protos
}

func node(t *testing.T, pl *Plan, recipe string) *Node {
	t.Helper()
	for _, n := range pl.Nodes {
		if n.Recipe == recipe {
			return n
		}
	}
	t.Fatalf("в плане нет %s; узлы: %v", recipe, pl.Nodes)
	return nil
}

func approx(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %.6g, ждали %.6g", what, got, want)
	}
}

func all(machines map[string]string) Options { return Options{All: true, Machines: machines} }

// Шестерня 60/мин в сборщике-1 (скорость 0.5): 1 машина, 120 железных пластин/мин.
func TestGearAssembler1(t *testing.T) {
	pl, err := Solve(pt(t), map[string]float64{"iron-gear-wheel": 1}, all(map[string]string{"crafting": "assembling-machine-1"}))
	if err != nil {
		t.Fatal(err)
	}
	n := node(t, pl, "iron-gear-wheel")
	approx(t, "сборщиков", n.Machines, 1)
	approx(t, "железа/с", n.In["iron-plate"], 2)
	if n.Machine != "assembling-machine-1" {
		t.Errorf("машина %s", n.Machine)
	}
}

// Электросхема 1/с: 3 провода → 1.5 крафта провода/с, медь 1.5/с, железо 1/с.
func TestElectronicCircuit(t *testing.T) {
	pl, err := Solve(pt(t), map[string]float64{"electronic-circuit": 1}, all(nil))
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "крафтов провода", node(t, pl, "copper-cable").Crafts, 1.5)
	approx(t, "медь", node(t, pl, "copper-plate").Crafts, 1.5)
	approx(t, "железо", node(t, pl, "iron-plate").Crafts, 1)
	approx(t, "медная руда", pl.Raw["copper-ore"], 1.5)
	approx(t, "железная руда", pl.Raw["iron-ore"], 1)
}

// Красная наука 1/с в сборщике-2 (0.75): 5 с крафта → 6.67 машин.
func TestRedScienceAssembler2(t *testing.T) {
	pl, err := Solve(pt(t), map[string]float64{"automation-science-pack": 1}, all(map[string]string{"crafting": "assembling-machine-2"}))
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "сборщиков", node(t, pl, "automation-science-pack").Machines, 5/0.75)
}

// Каменная печь: железная пластина 3.2 с → 18.75 в минуту на печь; электробур 0.5 руды/с.
func TestFurnaceAndDrill(t *testing.T) {
	pl, err := Solve(pt(t), map[string]float64{"iron-plate": 1}, all(map[string]string{"smelting": "stone-furnace"}))
	if err != nil {
		t.Fatal(err)
	}
	f := node(t, pl, "iron-plate")
	approx(t, "печей", f.Machines, 3.2)
	approx(t, "пластин в минуту на печь", 60/f.Machines, 18.75)
	if f.Fuel <= 0 {
		t.Error("каменной печи нужно топливо")
	}
	d := node(t, pl, "mine:iron-ore")
	if d.Machine != "electric-mining-drill" {
		t.Errorf("бур %s", d.Machine)
	}
	approx(t, "буров на 1 руду/с", d.Machines, 2)
	if pl.PowerW <= 0 || pl.Steam.Engines <= 0 {
		t.Error("электробуру нужно электричество и паровая станция")
	}
}

// Паровая станция: 1 помпа : 20 бойлеров : 40 двигателей (36 МВт).
func TestSteamRatio(t *testing.T) {
	s := steamFor(pt(t), 36e6)
	approx(t, "двигатели", s.Engines, 40)
	approx(t, "бойлеры", s.Boilers, 20)
	approx(t, "помпы", s.Pumps, 1)
}

// Нефть: газ 10/с со всем изученным — продвинутая переработка + крекинг, без излишков.
func TestOilBalanced(t *testing.T) {
	pl, err := Solve(pt(t), map[string]float64{"petroleum-gas": 10}, all(nil))
	if err != nil {
		t.Fatal(err)
	}
	node(t, pl, "advanced-oil-processing")
	if len(pl.Surplus) > 0 {
		t.Errorf("излишки: %v", pl.Surplus)
	}
	for _, n := range pl.Nodes {
		if n.Crafts < 0 {
			t.Errorf("%s: отрицательная скорость", n.Recipe)
		}
	}
	if pl.Raw["crude-oil"] <= 0 || pl.Raw["water"] <= 0 {
		t.Errorf("сырьё %v", pl.Raw)
	}
}

// Без исследований: сборщиков нет — руками; железо — каменная печь.
func TestNothingResearched(t *testing.T) {
	pl, err := Solve(pt(t), map[string]float64{"iron-gear-wheel": 1}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m := node(t, pl, "iron-gear-wheel").Machine; m != "character" {
		t.Errorf("шестерня делается в %s, а сборщики не изучены", m)
	}
	if m := node(t, pl, "iron-plate").Machine; m != "stone-furnace" {
		t.Errorf("железо плавится в %s", m)
	}
	if len(pl.Techs) != 0 {
		t.Errorf("для доступного плана нужны техи: %v", pl.Techs)
	}
}

// Критерий стадии: ракетная часть 1/мин — дерево до сырья и список исследований.
func TestRocketPart(t *testing.T) {
	p := pt(t)
	pl, err := Solve(p, map[string]float64{"rocket-part": 1.0 / 60}, all(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"iron-ore", "copper-ore", "coal", "crude-oil", "water"} {
		if pl.Raw[r] <= 0 {
			t.Errorf("нет сырья %s: %v", r, pl.Raw)
		}
	}
	for _, rec := range []string{"rocket-part", "processing-unit", "low-density-structure", "rocket-fuel"} {
		node(t, pl, rec)
	}
	if !slices.Contains(pl.Techs, "rocket-silo") {
		t.Errorf("в исследованиях нет rocket-silo: %v", pl.Techs)
	}
	// каждая тех — после своих предпосылок
	pos := map[string]int{}
	for i, tc := range pl.Techs {
		pos[tc] = i
	}
	for _, tc := range pl.Techs {
		for _, pre := range p.Technologies[tc].Prerequisites {
			if j, ok := pos[pre]; ok && j > pos[tc] {
				t.Errorf("%s раньше своей предпосылки %s", tc, pre)
			}
		}
	}
	if len(pl.Cost.Packs) < 5 {
		t.Errorf("цена в пакетах: %v", pl.Cost.Packs)
	}
	if !slices.ContainsFunc(pl.Cost.Triggers, func(s string) bool {
		return slices.Contains([]string{"automation-science-pack: craft-item lab ×1"}, s)
	}) {
		t.Errorf("нет trigger-действия «скрафтить лабораторию»: %v", pl.Cost.Triggers)
	}
	t.Logf("узлов %d, тех %d, МВт %.1f, сырьё %v", len(pl.Nodes), len(pl.Techs), pl.PowerW/1e6, pl.Raw)
}

// Ручной крафт: бур = 3 шестерни + печь + 3 пластины → 9 железа и 5 камня; секунды —
// сумма времён крафтов.
func TestHandcraftDrill(t *testing.T) {
	p := pt(t)
	h := Handcraft(p, p.NewAvailability(nil, false), "burner-mining-drill", 1)
	if h.Base["iron-plate"] != 9 || h.Base["stone"] != 5 || len(h.Base) != 2 {
		t.Errorf("сырьё %v", h.Base)
	}
	want := p.Recipes["burner-mining-drill"].Energy + 3*p.Recipes["iron-gear-wheel"].Energy + p.Recipes["stone-furnace"].Energy
	approx(t, "секунд", h.Seconds, want)
}

// Закрытый рецепт: красная наука без исследований — руками, но в плане trigger-тех
// «скрафтить лабораторию»; а не «предмет без рецепта».
func TestLockedRecipeNeedsTech(t *testing.T) {
	pl, err := Solve(pt(t), map[string]float64{"automation-science-pack": 0.5}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Sources) > 0 {
		t.Errorf("закрытый рецепт посчитан как «без рецепта»: %v", pl.Sources)
	}
	if n := node(t, pl, "automation-science-pack"); n.Machine != "character" {
		t.Errorf("машина %s, а сборщики не изучены", n.Machine)
	}
	if !slices.Contains(pl.Techs, "automation-science-pack") {
		t.Errorf("техи %v", pl.Techs)
	}
}
