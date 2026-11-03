package cluster

import (
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// inserterRate — захватов в секунду (скорость поворота прототипов 2.1: обычный 0.014, быстрый
// 0.04 оборота за тик — полоборота туда и обратно).
var inserterRate = map[string]float64{"inserter": 0.84, "fast-inserter": 2.4}

// InserterHand — предметов за захват с бонусом inserter-capacity-bonus-2 (stages.md, общее: в
// очереди исследований в начале стадий).
const InserterHand = 2

// recipeItem — что делает рецепт (основной продукт).
func recipeItem(p *knowledge.Prototypes, recipe string) string {
	r := p.Recipes[recipe]
	if r.MainProduct != "" {
		return r.MainProduct
	}
	if len(r.Products) > 0 {
		return r.Products[0].Name
	}
	return recipe
}

// pairNeed — сколько item в секунду берёт сборщик mach (на нём — второго уровня) с рецептом main.
func pairNeed(p *knowledge.Prototypes, mach, main, item string) float64 {
	if t2, ok := tier2[mach]; ok {
		mach = t2
	}
	r := p.Recipes[main]
	if r.Energy <= 0 {
		return 0
	}
	for _, in := range r.Ingredients {
		if in.Name == item {
			return in.Mean() * p.Entities[mach].CraftingSpeed / r.Energy
		}
	}
	return 0
}

// PairInserter — инсертер пары из сборщика рецепта sub в основной main (cluster.md «Сборка»): ins,
// если носит нужное с бонусом захвата, иначе быстрый (провод → схема — 4.5/с: обычный держал
// кластер схем на 29 % плана, стадия 60 на 2448886157).
func PairInserter(p *knowledge.Prototypes, ins, mach, main, sub string) string {
	if pairNeed(p, mach, main, recipeItem(p, sub)) > inserterRate[ins]*InserterHand {
		return "fast-inserter"
	}
	return ins
}

// FastPairs — правило PairInserter к сохранённому плану list (посчитан до правила): обычный
// инсертер из сборщика в сборщик, которому не хватает, — быстрый. Сколько заменено.
func FastPairs(p *knowledge.Prototypes, list []layout.Placement) int {
	machines := map[world.Tile]layout.Placement{}
	for _, pl := range list {
		if p.Entities[pl.Name].Type == "assembling-machine" && pl.Recipe != "" {
			for _, t := range layout.Tiles(p, pl) {
				machines[t] = pl
			}
		}
	}
	n := 0
	for i, pl := range list {
		if pl.Name != "inserter" {
			continue
		}
		pick, drop := layout.InserterEnds(p, pl)
		from, ok1 := machines[pick]
		to, ok2 := machines[drop]
		if !ok1 || !ok2 {
			continue
		}
		if name := PairInserter(p, pl.Name, to.Name, to.Recipe, from.Recipe); name != pl.Name {
			list[i].Name = name
			n++
		}
	}
	return n
}
