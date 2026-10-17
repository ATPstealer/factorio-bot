package calc

import (
	"sort"

	"factorio-bot/internal/knowledge"
)

// Hand — ручной крафт: что нужно иметь (то, что руками не крафтится: пластины, камень,
// дерево…) и сколько секунд крафта.
type Hand struct {
	Base    map[string]float64
	Crafts  map[string]float64 // рецепт → крафтов
	Seconds float64
}

// Handcraft — раскладка n предметов item на ручные крафты (доступные рецепты категорий
// персонажа) вплоть до того, что руками не делается.
func Handcraft(p *knowledge.Prototypes, av *knowledge.Availability, item string, n float64) Hand {
	h := Hand{Base: map[string]float64{}, Crafts: map[string]float64{}}
	cats := map[string]bool{}
	if ch, ok := p.Entities["character"]; ok {
		for _, c := range ch.CraftingCategories {
			cats[c] = true
		}
	}
	var expand func(it string, k float64, depth int)
	expand = func(it string, k float64, depth int) {
		if depth > 30 {
			h.Base[it] += k
			return
		}
		recs := p.RecipesFor(it)
		sort.Strings(recs)
		for _, r := range recs {
			rec := p.Recipes[r]
			if !p.Usable(r) || !av.Recipe(r) || !cats[rec.Category] {
				continue
			}
			out := 0.0
			for _, pr := range rec.Products {
				if pr.Name == it {
					out += pr.Mean()
				}
			}
			if out <= 0 {
				continue
			}
			crafts := k / out
			h.Crafts[r] += crafts
			h.Seconds += crafts * rec.Energy
			for _, in := range rec.Ingredients {
				expand(in.Name, crafts*in.Mean(), depth+1)
			}
			return
		}
		h.Base[it] += k
	}
	expand(item, n, 0)
	return h
}
