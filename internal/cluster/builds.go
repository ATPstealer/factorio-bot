package cluster

import (
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

// Что ставит каждая стадия стройки (stages.md, строки «Ставит»): одна таблица на всю игру. План
// всей базы (стадия 2) берёт числа отсюда; калькулятор только проверяет, что их хватает
// (builds_test.go: наука 0.1/с плюс стройка не больше выхода).

// Build — кластер плана: стадия, что его строит, вид, продукт и сколько машин.
type Build struct {
	Stage   int
	Kind    Kind
	Product string // продукт (у добычи — ресурс, у станции — power, у склада — жидкость)
	// Recipe — основной рецепт; Subs — промежуточные внутри, парами (тройками) прямой подачи:
	// сборщики Subs → инсертер → основной, по одному на основной (cluster.md §2а «Сборка»)
	Recipe string
	Subs   []string
	// N — основных машин (у пар — пар; у станции — бойлеров, у склада — цистерн, у нефти — скважин
	// не меньше: месторождения целиком)
	N int
	// Ore — добыча: руды под бурами (cluster.md §2а «Добыча»)
	Ore int
	// ToPower — добыча угля станции: своя лента к станции, мимо шины
	ToPower bool
	// SubChest — промежуточное ещё и в сундук бота (трубы двигателей, stages.md — стадия 38)
	SubChest string
}

// Tier2From — стадия замены печей и сборщиков на второй уровень (stages.md, стадия 50): до неё на
// местах плана — первый уровень того же размера.
const Tier2From = 50

// Builds — кластеры плана по стадиям (stages.md, строки «Ставит»).
var Builds = []Build{
	{Stage: 11, Kind: Power, Product: "power", N: 15},
	{Stage: 13, Kind: Storage, Product: "storage", N: 8},
	{Stage: 16, Kind: Mine, Product: "coal", Ore: 1_000_000, ToPower: true},
	{Stage: 16, Kind: Mine, Product: "coal", Ore: 500_000},
	{Stage: 17, Kind: Mine, Product: "iron-ore", Ore: 500_000},
	{Stage: 17, Kind: Mine, Product: "copper-ore", Ore: 500_000},
	{Stage: 17, Kind: Smelt, Product: "copper-plate", Recipe: "copper-plate", N: 20},
	{Stage: 17, Kind: Smelt, Product: "iron-plate", Recipe: "iron-plate", N: 20},
	{Stage: 17, Kind: Smelt, Product: "iron-plate", Recipe: "iron-plate", N: 10},
	{Stage: 20, Kind: Make, Product: "automation-science-pack", Recipe: "automation-science-pack", Subs: []string{"iron-gear-wheel"}, N: 2},
	{Stage: 21, Kind: Lab, Product: "lab", N: 10},
	{Stage: 23, Kind: Make, Product: "electronic-circuit", Recipe: "electronic-circuit", Subs: []string{"copper-cable"}, N: 10},
	{Stage: 24, Kind: Make, Product: "inserter", Recipe: "inserter", Subs: []string{"iron-gear-wheel"}, N: 3},
	{Stage: 25, Kind: Make, Product: "transport-belt", Recipe: "transport-belt", Subs: []string{"iron-gear-wheel"}, N: 7},
	{Stage: 26, Kind: Make, Product: "logistic-science-pack", Recipe: "logistic-science-pack", N: 2},
	{Stage: 30, Kind: Mine, Product: "iron-ore", Ore: 2_000_000},
	{Stage: 30, Kind: Mine, Product: "copper-ore", Ore: 2_000_000},
	{Stage: 30, Kind: Mine, Product: "stone", Ore: 500_000},
	{Stage: 30, Kind: Mine, Product: "coal", Ore: 2_000_000},
	{Stage: 30, Kind: Smelt, Product: "stone-brick", Recipe: "stone-brick", N: 2},
	{Stage: 31, Kind: Smelt, Product: "steel-plate", Recipe: "steel-plate", Subs: []string{"iron-plate"}, N: 15},
	{Stage: 32, Kind: Make, Product: "fast-transport-belt", Recipe: "fast-transport-belt", Subs: []string{"iron-gear-wheel"}, N: 3},
	{Stage: 33, Kind: Tank, Product: "crude-oil", N: 30},
	{Stage: 34, Kind: Mine, Product: "crude-oil", N: 30},
	{Stage: 36, Kind: Make, Product: "petroleum-gas", Recipe: "basic-oil-processing", N: 3},
	{Stage: 37, Kind: Make, Product: "plastic-bar", Recipe: "plastic-bar", N: 3},
	{Stage: 37, Kind: Make, Product: "sulfur", Recipe: "sulfur", N: 3},
	{Stage: 38, Kind: Make, Product: "engine-unit", Recipe: "engine-unit", Subs: []string{"pipe", "iron-gear-wheel"}, N: 4, SubChest: "pipe"},
	{Stage: 39, Kind: Make, Product: "advanced-circuit", Recipe: "advanced-circuit", Subs: []string{"copper-cable"}, N: 8},
	{Stage: 40, Kind: Make, Product: "chemical-science-pack", Recipe: "chemical-science-pack", N: 3},
	{Stage: 44, Kind: Make, Product: "electric-furnace", Recipe: "electric-furnace", N: 3},
	{Stage: 45, Kind: Make, Product: "productivity-module", Recipe: "productivity-module", N: 3},
	{Stage: 46, Kind: Make, Product: "rail", Recipe: "rail", Subs: []string{"iron-stick"}, N: 3},
	{Stage: 47, Kind: Make, Product: "production-science-pack", Recipe: "production-science-pack", N: 4},
	{Stage: 51, Kind: Make, Product: "heavy-oil", Recipe: "advanced-oil-processing", N: 2},
	{Stage: 52, Kind: Make, Product: "light-oil", Recipe: "heavy-oil-cracking", N: 2},
	{Stage: 52, Kind: Make, Product: "petroleum-gas", Recipe: "light-oil-cracking", N: 2},
	{Stage: 53, Kind: Make, Product: "lubricant", Recipe: "lubricant", N: 2},
	{Stage: 54, Kind: Make, Product: "sulfuric-acid", Recipe: "sulfuric-acid", N: 2},
	{Stage: 55, Kind: Make, Product: "battery", Recipe: "battery", N: 2},
	{Stage: 56, Kind: Make, Product: "electric-engine-unit", Recipe: "electric-engine-unit", N: 2},
	{Stage: 57, Kind: Make, Product: "flying-robot-frame", Recipe: "flying-robot-frame", N: 3},
	{Stage: 58, Kind: Make, Product: "low-density-structure", Recipe: "low-density-structure", N: 5},
	{Stage: 59, Kind: Make, Product: "processing-unit", Recipe: "processing-unit", N: 5},
	{Stage: 60, Kind: Make, Product: "utility-science-pack", Recipe: "utility-science-pack", N: 4},
}

// BuildMachine — машина рецепта кластера: печь или сборщик второго уровня, если t2 (после стадии
// Tier2From), иначе первого; химзавод, НПЗ — какие есть.
func BuildMachine(p *knowledge.Prototypes, recipe string, t2 bool) string {
	switch p.Recipes[recipe].Category {
	case "smelting":
		if t2 {
			return "steel-furnace"
		}
		return "stone-furnace"
	case "chemistry":
		return "chemical-plant"
	case "oil-processing":
		return "oil-refinery"
	case "crafting-with-fluid":
		return "assembling-machine-2" // сборщик 1 жидкость не берёт
	}
	if t2 {
		return "assembling-machine-2"
	}
	return "assembling-machine-1"
}

// Capacity — выход кластера b на полном ходу, шт/с (жидкости — ед/с): продукты основного рецепта
// (у пар — сколько успевают промежуточные; их продукт — внутри и в выход не идёт). t2 — машины
// второго уровня (BuildMachine).
func (b Build) Capacity(p *knowledge.Prototypes, t2 bool) map[string]float64 {
	if b.Recipe == "" {
		return nil
	}
	rate := func(recipe string) float64 { // крафтов/с у b.N машин
		r := p.Recipes[recipe]
		return float64(b.N) * p.Entities[BuildMachine(p, recipe, t2)].CraftingSpeed / r.Energy
	}
	main := p.Recipes[b.Recipe]
	crafts := rate(b.Recipe)
	for _, s := range b.Subs { // промежуточного не хватает — основной ждёт
		sub := p.Recipes[s]
		need := amount(main.Ingredients, product(sub))
		if need > 0 {
			crafts = min(crafts, rate(s)*amount(sub.Products, product(sub))/need)
		}
	}
	out := map[string]float64{}
	for _, o := range main.Products {
		out[o.Name] += amountOf(o) * crafts
	}
	return out
}

// labPackSec — секунд исследования на пакет (среднее; лабораторий — числом из таблицы, это только
// для потоков и электричества).
const labPackSec = 30

// Cluster — кластер плана по строке таблицы: ровно N машин (пар), машины — первого уровня до
// стадии Tier2From (той же площади), потоки — на полном ходу второго уровня. Добыча — образец
// (ресурс, бур, цель Ore): блоки на месторождениях ставит план; склад нефти — без места (Near
// задаёт план).
func (b Build) Cluster(p *knowledge.Prototypes) *Cluster {
	switch b.Kind {
	case Power:
		return Station(p, b.N)
	case Storage:
		return StorageCluster()
	case Tank:
		const rows = 5 // блок 6×5 (cluster.md §2а «Нефть»)
		return Tanks(b.Product, (b.N+rows-1)/rows, rows, world.Tile{})
	case Lab:
		packs := map[string]float64{}
		for _, s := range []string{packRed, packGreen, packBlue, packPurple, packYellow} {
			packs[s] = 0.1
		}
		c := Labs(p, packs, labPackSec, 1)
		c.Count, c.Room, c.Need = b.N, b.N, b.N
		return c
	case Mine:
		drill := "electric-mining-drill"
		if b.Product == "crude-oil" {
			drill = "pumpjack"
		}
		c := &Cluster{Kind: Mine, Product: b.Product, Reserve: 1, Ore: b.Ore,
			Units: []Unit{{Recipe: "mine:" + b.Product, Machine: drill, Machines: float64(b.N)}}}
		c.Cross, c.Pitch, c.Head = geometry(Mine)
		c.Count, c.Room, c.Need = b.N, b.N, b.N
		return c
	}
	r := p.Recipes[b.Recipe]
	switch {
	case r.Category == "oil-processing" && b.Recipe == "advanced-oil-processing":
		return RefineryAdv(p, b.N)
	case r.Category == "oil-processing":
		return Refinery(p, b.N)
	case r.Category == "chemistry":
		if _, fluid := p.Fluids[b.Product]; fluid {
			return ChemFluid(p, b.Recipe, b.N)
		}
		return Chem(p, b.Recipe, b.N)
	case b.Product == "steel-plate":
		return Steel(p, b.N)
	}
	// плавка и сборка: основной рецепт и промежуточные пар (тройки) — по одному на основной
	t2 := b.Stage >= Tier2From
	c := &Cluster{Kind: b.Kind, Product: b.Product, Reserve: 1, SubChest: b.SubChest}
	crafts := 0.0
	for _, o := range r.Products {
		if v := b.Capacity(p, true)[o.Name]; v > 0 && o.Name == b.Product {
			crafts = v / amountOf(o)
		}
	}
	in, out := map[string]float64{}, map[string]float64{}
	internal := map[string]float64{}
	subs := map[string]string{} // продукт → рецепт промежуточного
	for _, s := range b.Subs {
		subs[product(p.Recipes[s])] = s
	}
	power := 0.0
	addUnit := func(recipe string, n int) {
		m := BuildMachine(p, recipe, t2)
		c.Units = append(c.Units, Unit{Recipe: recipe, Machine: m, Machines: float64(n)})
		e := p.Entities[m]
		if e.Burner != nil { // печи — на угле
			in["coal"] += float64(n) * e.EnergyUsage * 60 / coalJ
		} else {
			power += float64(n) * e.EnergyUsage * 60
		}
	}
	addUnit(b.Recipe, b.N)
	for _, i := range r.Ingredients {
		need := amountOf(i) * crafts
		s, ok := subs[i.Name]
		if !ok {
			in[i.Name] += need
			continue
		}
		internal[i.Name] += need
		sr := p.Recipes[s]
		sc := need / amount(sr.Products, i.Name)
		for _, si := range sr.Ingredients {
			in[si.Name] += amountOf(si) * sc
		}
	}
	for _, s := range b.Subs {
		addUnit(s, b.N)
	}
	for _, o := range r.Products {
		out[o.Name] += amountOf(o) * crafts
	}
	c.In, c.Out, c.Internal, c.PowerW = toFlows(in), toFlows(out), toFlows(internal), power
	if len(b.Subs) > 0 {
		c.Main, c.Sub, c.SubPer = b.Recipe, b.Subs[0], len(b.Subs)
		if len(b.Subs) > 1 {
			c.Sub2 = b.Subs[1]
		}
	}
	shape(p, c)
	if c.IsFluidMake(p) {
		c.Cross, c.Pitch, c.Head = fluidMakeCross, 4, 1
	}
	c.Count, c.Room, c.Need = b.N, b.N, b.N
	return c
}

// product — главный продукт рецепта.
func product(r knowledge.Recipe) string {
	if r.MainProduct != "" {
		return r.MainProduct
	}
	return r.Products[0].Name
}

// amount — сколько предмета item в списке (ингредиенты или продукты).
func amount(list knowledge.List[knowledge.Amount], item string) float64 {
	n := 0.0
	for _, a := range list {
		if a.Name == item {
			n += amountOf(a)
		}
	}
	return n
}

// amountOf — среднее количество (с разбросом и вероятностью).
func amountOf(a knowledge.Amount) float64 {
	n := a.Amount
	if n == 0 {
		n = (a.AmountMin + a.AmountMax) / 2
	}
	if a.Probability > 0 {
		n *= a.Probability
	}
	return n
}
