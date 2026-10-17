// Package plan — цели, HTN-разложение до примитивов и симулятор.
//
// Планировщик реактивный: на каждом шаге цель раскладывается от текущего состояния до
// одного следующего примитива (с цепочкой «почему»). Состояние меняется — следующий шаг
// учитывает это сам; полный план (bot plan, тесты) — прогон того же разложения на симуляторе.
package plan

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
)

// Owned — своя постройка из реестра: зачем поставлена (роль) и что внутри.
type Owned struct {
	Unit   int                       `json:"unit"`
	Name   string                    `json:"name"`
	Pos    bridge.Pos                `json:"pos"`
	Role   string                    `json:"role"`
	Inv    map[string]map[string]int `json:"-"` // инвентари: fuel, crafter_input, crafter_output, chest
	Status string                    `json:"-"`
}

// Count — сколько предмета во всех инвентарях постройки.
func (o *Owned) Count(item string) int {
	n := 0
	for _, inv := range o.Inv {
		n += inv[item]
	}
	return n
}

// In — сколько предмета в инвентаре inv.
func (o *Owned) In(inv, item string) int { return o.Inv[inv][item] }

// Output — что можно забрать: результат печи/сборщика и содержимое сундука.
func (o *Owned) Output(item string) int { return o.In("crafter_output", item) + o.In("chest", item) }

// State — что планировщику известно. Заполняется из игры (exec) или симулятором (тесты).
type State struct {
	Protos    *knowledge.Prototypes
	Av        *knowledge.Availability
	Inventory map[string]int
	PlayerPos bridge.Pos
	Owned     []*Owned

	// FindSpot — где поставить постройку роли role (рядом с остальной группой роли, у
	// нужного ресурса); avoid — места, где уже не получилось.
	FindSpot func(s *State, entity, role string, avoid []bridge.Pos) (bridge.Pos, bool)
	// Minable — можно ли добыть предмет с открытой карты (руда, дерево, камень).
	Minable func(item string) bool
	// Found — найдено разведкой (в симуляции: после шага explore предмет считается видимым).
	Found map[string]bool
}

// Explorable — предмет вообще добывается с карты (ресурс, дерево, камень) — его есть
// смысл искать разведкой, если на открытой карте его нет.
func (s *State) Explorable(item string) bool {
	for _, e := range s.Protos.Entities {
		switch e.Type {
		case "resource", "tree", "simple-entity":
		default:
			continue
		}
		if e.Mineable == nil || e.Mineable.RequiredFluid != "" {
			continue
		}
		for _, p := range e.Mineable.Products {
			if p.Name == item {
				return true
			}
		}
	}
	return false
}

func (s *State) Role(role string) *Owned {
	for _, o := range s.Owned {
		if o.Role == role {
			return o
		}
	}
	return nil
}

// Clone — копия для симуляции (функции общие).
func (s *State) Clone() *State {
	c := *s
	c.Inventory = map[string]int{}
	for k, v := range s.Inventory {
		c.Inventory[k] = v
	}
	c.Found = map[string]bool{}
	for k, v := range s.Found {
		c.Found[k] = v
	}
	c.Owned = nil
	for _, o := range s.Owned {
		oo := *o
		oo.Inv = map[string]map[string]int{}
		for k, inv := range o.Inv {
			oo.Inv[k] = map[string]int{}
			for it, n := range inv {
				oo.Inv[k][it] = n
			}
		}
		c.Owned = append(c.Owned, &oo)
	}
	return &c
}

// Primitive — один шаг для исполнителя (руки — act.Hands).
type Primitive struct {
	Kind   string     `json:"kind"` // gather | craft | place | insert | take | wait
	Item   string     `json:"item,omitempty"`
	Recipe string     `json:"recipe,omitempty"`
	N      int        `json:"n,omitempty"`
	Role   string     `json:"role,omitempty"` // постройка, с которой работаем
	Pos    bridge.Pos `json:"pos,omitempty"`
	Why    []string   `json:"why"` // цепочка целей сверху вниз
}

// Key — идентичность шага для учёта отказов и банов.
func (p *Primitive) Key() string {
	switch p.Kind {
	case "place":
		return fmt.Sprintf("place:%s@%.1f,%.1f", p.Item, p.Pos.X, p.Pos.Y)
	case "gather":
		return "gather:" + p.Item
	case "craft":
		return "craft:" + p.Recipe
	}
	return p.Kind + ":" + p.Role + ":" + p.Item
}

func (p *Primitive) String() string {
	var s string
	switch p.Kind {
	case "gather":
		s = fmt.Sprintf("добыть %s ×%d", p.Item, p.N)
	case "craft":
		s = fmt.Sprintf("скрафтить %s ×%d", p.Recipe, p.N)
	case "place":
		s = fmt.Sprintf("поставить %s в (%.1f, %.1f) как %s", p.Item, p.Pos.X, p.Pos.Y, p.Role)
	case "insert":
		s = fmt.Sprintf("положить %s ×%d в %s", p.Item, p.N, p.Role)
	case "take":
		s = fmt.Sprintf("забрать из %s", p.Role)
	case "wait":
		s = fmt.Sprintf("ждать %s ×%d в %s", p.Item, p.N, p.Role)
	case "explore":
		s = fmt.Sprintf("разведать карту: искать %s", p.Item)
	default:
		s = p.Kind
	}
	return s
}

// Reason — «почему»: цепочка целей снизу вверх.
func (p *Primitive) Reason() string {
	parts := make([]string, 0, len(p.Why))
	for i := len(p.Why) - 1; i >= 0; i-- {
		parts = append(parts, p.Why[i])
	}
	return strings.Join(parts, " ← ")
}

// craftRecipe — рецепт ручного крафта предмета (доступный, категория персонажа).
func (s *State) craftRecipe(item string) (string, bool) {
	cats := map[string]bool{}
	if ch, ok := s.Protos.Entities["character"]; ok {
		for _, c := range ch.CraftingCategories {
			cats[c] = true
		}
	}
	recs := s.Protos.RecipesFor(item)
	sort.Strings(recs)
	for _, r := range recs {
		rec := s.Protos.Recipes[r]
		if s.Protos.Usable(r) && s.Av.Recipe(r) && cats[rec.Category] {
			return r, true
		}
	}
	return "", false
}

// smeltRecipe — рецепт плавки предмета: один ингредиент, категория smelting.
func (s *State) smeltRecipe(item string) (rec string, ore string, ok bool) {
	recs := s.Protos.RecipesFor(item)
	sort.Strings(recs)
	for _, r := range recs {
		x := s.Protos.Recipes[r]
		if x.Category == "smelting" && s.Protos.Usable(r) && s.Av.Recipe(r) && len(x.Ingredients) == 1 {
			return r, x.Ingredients[0].Name, true
		}
	}
	return "", "", false
}

func productAmount(r knowledge.Recipe, item string) float64 {
	n := 0.0
	for _, p := range r.Products {
		if p.Name == item {
			n += p.Mean()
		}
	}
	return n
}

func ceilDiv(a int, b float64) int { return int(math.Ceil(float64(a)/b - 1e-9)) }
