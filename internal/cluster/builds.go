package cluster

import "factorio-bot/internal/knowledge"

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
	{Stage: 38, Kind: Make, Product: "engine-unit", Recipe: "engine-unit", Subs: []string{"iron-gear-wheel", "pipe"}, N: 4},
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
