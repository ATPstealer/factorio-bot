package knowledge

import (
	"sort"
	"strings"
)

// Граф: что что производит, где крафтится, что доступно игроку.

// Availability — что открыто силе игрока: рецепты (изначальные + открытые техами) и техи.
type Availability struct {
	Recipes    map[string]bool
	Researched map[string]bool
	All        bool // считать всё доступным (план «на будущее»)
}

// NewAvailability — доступность по списку изученных технологий.
func (p *Prototypes) NewAvailability(researched []string, all bool) *Availability {
	a := &Availability{Recipes: map[string]bool{}, Researched: map[string]bool{}, All: all}
	for _, t := range researched {
		a.Researched[t] = true
	}
	for name, r := range p.Recipes {
		if r.Enabled {
			a.Recipes[name] = true
		}
	}
	for t := range a.Researched {
		for _, r := range p.Technologies[t].Unlocks {
			a.Recipes[r] = true
		}
	}
	return a
}

func (a *Availability) Recipe(name string) bool { return a.All || a.Recipes[name] }

// Usable — рецепт годится для расчётов: не скрыт, не служебный, не бочки (они только
// перекладывают жидкость и создают циклы).
func (p *Prototypes) Usable(name string) bool {
	r, ok := p.Recipes[name]
	if !ok || r.Hidden || r.Category == "parameters" || len(r.Products) == 0 {
		return false
	}
	return !strings.Contains(r.Subgroup, "barrel")
}

// IsFluid — жидкость (а не предмет).
func (p *Prototypes) IsFluid(name string) bool {
	_, ok := p.Fluids[name]
	return ok
}

// Resource — ресурс на карте, дающий item (руда, нефть): имя сущности ресурса.
func (p *Prototypes) Resource(item string) (string, bool) {
	names := make([]string, 0)
	for name, e := range p.Entities {
		if e.Type != "resource" || e.Mineable == nil {
			continue
		}
		for _, pr := range e.Mineable.Products {
			if pr.Name == item {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	return names[0], true
}

// EntityAvailable — можно ли получить постройку: рецепт её предмета доступен.
func (p *Prototypes) EntityAvailable(entity string, a *Availability) bool {
	if a.All {
		return true
	}
	e, ok := p.Entities[entity]
	if !ok {
		return false
	}
	for _, it := range e.ItemsToPlaceThis {
		for _, r := range p.RecipesFor(it) {
			if a.Recipe(r) {
				return true
			}
		}
	}
	return false
}

// Machine — где делается рецепт: сущность и её скорость.
type Machine struct {
	Name  string
	Speed float64
}

// BestCrafter — самая быстрая доступная машина категории рецепта; руки персонажа — если
// машин нет, а категория ручная. ok=false — сделать негде.
func (p *Prototypes) BestCrafter(category string, a *Availability, force string) (Machine, bool) {
	if force != "" {
		if e, ok := p.Entities[force]; ok && contains(e.CraftingCategories, category) {
			return Machine{force, e.CraftingSpeed}, true
		}
	}
	best := Machine{}
	for name, e := range p.Entities {
		if name == "character" || e.CraftingSpeed == 0 || !contains(e.CraftingCategories, category) {
			continue
		}
		if !p.EntityAvailable(name, a) {
			continue
		}
		if e.CraftingSpeed > best.Speed || (e.CraftingSpeed == best.Speed && name < best.Name) {
			best = Machine{name, e.CraftingSpeed}
		}
	}
	if best.Name != "" {
		return best, true
	}
	if ch, ok := p.Entities["character"]; ok && contains(ch.CraftingCategories, category) {
		return Machine{"character", ch.CraftingSpeed}, true
	}
	return best, false
}

// BestDrill — самый быстрый доступный бур (насос) для категории ресурса.
func (p *Prototypes) BestDrill(resourceCategory string, a *Availability) (Machine, bool) {
	best := Machine{}
	for name, e := range p.Entities {
		if e.Type != "mining-drill" || !contains(e.ResourceCategories, resourceCategory) || !p.EntityAvailable(name, a) {
			continue
		}
		if e.MiningSpeed > best.Speed || (e.MiningSpeed == best.Speed && name < best.Name) {
			best = Machine{name, e.MiningSpeed}
		}
	}
	return best, best.Name != ""
}

// BestBelt — самая быстрая доступная лента: имя и пропускная способность (предметов/с).
func (p *Prototypes) BestBelt(a *Availability) (string, float64) {
	name, speed := "", 0.0
	for n, e := range p.Entities {
		if e.Type != "transport-belt" || !p.EntityAvailable(n, a) {
			continue
		}
		if e.BeltSpeed > speed {
			name, speed = n, e.BeltSpeed
		}
	}
	// belt_speed — тайлов за тик; на ленте 8 предметов на тайл (2 полосы × 4)
	return name, speed * 60 * 8
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
