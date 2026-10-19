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
	"factorio-bot/internal/layout"
)

// Owned — своя постройка из реестра: зачем поставлена (роль) и что внутри.
type Owned struct {
	Unit   int                       `json:"unit"`
	Name   string                    `json:"name"`
	Pos    bridge.Pos                `json:"pos"`
	Role   string                    `json:"role"`
	Inv    map[string]map[string]int `json:"-"` // инвентари: fuel, crafter_input, crafter_output, chest
	Status string                    `json:"-"`
	Dir    *int                      `json:"-"` // направление в игре (nil — без направления)
	Recipe string                    `json:"-"` // рецепт сборщика в игре
	UGType string                    `json:"-"` // подземка в игре: input/output
	// Network — электросеть в игре (0 — не подключена к столбам; в симуляции -1 — неизвестно)
	Network int `json:"-"`
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
	// Stores — сундуки рядом вне реестра (поставленные руками, обломки) с содержимым: из них
	// только забирать — прежде чем добывать или крафтить.
	Stores []*Owned

	// FindSpot — где поставить постройку роли role (рядом с остальной группой роли, у
	// нужного ресурса); avoid — места, где уже не получилось.
	FindSpot func(s *State, entity, role string, avoid []bridge.Pos) (bridge.Pos, bool)
	// Minable — можно ли добыть предмет с открытой карты (руда, дерево, камень).
	Minable func(item string) bool
	// Exhausted — у раскладки точки старта руда под буром кончилась: точка закрыта.
	Exhausted func(tag string) bool
	// Researching — что исследуется сейчас и стоит в очереди
	Researching map[string]bool
	// ResearchStalled — сколько секунд исследование не продвигается (0 — идёт или нет его)
	ResearchStalled int
	// Design — раскладка под задачу (kind: "research" — станция + лаборатории + столбы);
	// одна и та же между циклами (роли построек стабильны). nil — не умеем (симуляция).
	Design func(s *State, kind string) (Built, bool)
	// Clusters — база кластерами (стадия 9): станция, уголь и ленты — в кластерах; старые
	// исправления (блоки power-N, шахта угля, лента угля к кормушкам) не строятся
	Clusters bool
	// Designs — все уже спроектированные раскладки (для расчёта мощности); nil — нет
	Designs func() [][]layout.Placement
	// Done — выполненные разовые этапы (Primitive.Mark): сохраняются между запусками
	Done map[string]bool
	// Produced — произведено за игру (статистика производства; trigger-технологии)
	Produced map[string]float64
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
	for _, o := range s.Stores {
		if o.Role == role {
			return o
		}
	}
	return nil
}

// StoreRole — роль сундука вне реестра (по месту).
func StoreRole(pos bridge.Pos) string { return fmt.Sprintf("store@%.1f,%.1f", pos.X, pos.Y) }

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
	c.Owned, c.Stores = cloneOwned(s.Owned), cloneOwned(s.Stores)
	c.Researching = map[string]bool{}
	for k, v := range s.Researching {
		c.Researching[k] = v
	}
	c.Done = map[string]bool{}
	for k, v := range s.Done {
		c.Done[k] = v
	}
	c.Produced = map[string]float64{}
	for k, v := range s.Produced {
		c.Produced[k] = v
	}
	if s.Av != nil {
		av := *s.Av
		av.Recipes, av.Researched = map[string]bool{}, map[string]bool{}
		for k, v := range s.Av.Recipes {
			av.Recipes[k] = v
		}
		for k, v := range s.Av.Researched {
			av.Researched[k] = v
		}
		c.Av = &av
	}
	return &c
}

func cloneOwned(os []*Owned) []*Owned {
	var res []*Owned
	for _, o := range os {
		oo := *o
		oo.Inv = map[string]map[string]int{}
		for k, inv := range o.Inv {
			oo.Inv[k] = map[string]int{}
			for it, n := range inv {
				oo.Inv[k][it] = n
			}
		}
		res = append(res, &oo)
	}
	return res
}

// Primitive — один шаг для исполнителя (руки — act.Hands).
type Primitive struct {
	Kind   string     `json:"kind"` // gather | craft | place | insert | take | wait
	Item   string     `json:"item,omitempty"`
	Recipe string     `json:"recipe,omitempty"`
	N      int        `json:"n,omitempty"`
	Role   string     `json:"role,omitempty"` // постройка, с которой работаем
	Pos    bridge.Pos `json:"pos,omitempty"`
	// gather: откуда ("tree", "rock"; пусто — откуда угодно)
	Source string `json:"source,omitempty"`
	// Mark — после успеха отметить этап выполненным (State.Done)
	Mark string `json:"mark,omitempty"`
	// build: все места одного предмета с одним направлением и их роли
	Dir   int          `json:"dir,omitempty"`
	Spots []bridge.Pos `json:"spots,omitempty"`
	Roles []string     `json:"roles,omitempty"`
	// Dirs — своё направление у каждого места (лента с поворотами — одним проходом)
	Dirs []int `json:"dirs,omitempty"`
	// Items — свой предмет у каждого места (Item "*": кластер разом с мест стояния)
	Items []string `json:"items,omitempty"`
	// refuel: сколько угля в каждую из Roles; Full — доливать до предела (Ctrl+клик:
	// кладётся максимум, ограничит вместимость)
	Ns   []int  `json:"ns,omitempty"`
	Full []bool `json:"full,omitempty"`
	// Area — вся площадь раскладки: персонажу не стоять внутри при стройке (замурует себя)
	Area *bridge.Box `json:"area,omitempty"`
	Why  []string    `json:"why"` // цепочка целей сверху вниз
}

// Key — идентичность шага для учёта отказов и банов.
func (p *Primitive) Key() string {
	switch p.Kind {
	case "place":
		return fmt.Sprintf("place:%s@%.1f,%.1f", p.Item, p.Pos.X, p.Pos.Y)
	case "gather":
		return "gather:" + p.Item + p.Source
	case "craft":
		return "craft:" + p.Recipe
	case "build", "rebuild":
		return fmt.Sprintf("%s:%s:%d@%.1f,%.1f", p.Kind, p.Item, p.Dir, p.Spots[0].X, p.Spots[0].Y)
	case "collect", "refuel":
		return p.Kind + ":" + p.Item + ":" + strings.Join(p.Roles, ",")
	}
	return p.Kind + ":" + p.Role + ":" + p.Item
}

func (p *Primitive) String() string {
	var s string
	switch p.Kind {
	case "gather":
		s = fmt.Sprintf("добыть %s ×%d", p.Item, p.N)
		if p.Source != "" {
			s += " (" + p.Source + ")"
		}
	case "craft":
		s = fmt.Sprintf("скрафтить %s ×%d", p.Recipe, p.N)
	case "place":
		s = fmt.Sprintf("поставить %s в (%.1f, %.1f) как %s", p.Item, p.Pos.X, p.Pos.Y, p.Role)
	case "rotate":
		s = fmt.Sprintf("повернуть %s направлением %d", p.Role, p.Dir)
	case "recipe":
		s = fmt.Sprintf("рецепт %s в %s", p.Recipe, p.Role)
	case "collect":
		s = fmt.Sprintf("собрать %s с %d построек (%s…)", p.Item, len(p.Roles), p.Roles[0])
	case "refuel":
		total := 0
		for _, n := range p.Ns {
			total += n
		}
		s = fmt.Sprintf("разнести %s ×%d по %d постройкам (%s…)", p.Item, total, len(p.Roles), p.Roles[0])
	case "rebuild":
		s = fmt.Sprintf("пересобрать %s (%s): поднять и поставить заново", p.Item, p.Roles[0])
	case "build":
		s = fmt.Sprintf("построить %s ×%d (%s…)", p.Item, len(p.Spots), p.Roles[0])
		if p.Item == "*" {
			s = fmt.Sprintf("построить разом %d построек (%s…)", len(p.Spots), p.Roles[0])
		}
		if p.Dir != 0 {
			s += fmt.Sprintf(" направление %d", p.Dir)
		}
	case "insert":
		s = fmt.Sprintf("положить %s ×%d в %s", p.Item, p.N, p.Role)
	case "take":
		s = fmt.Sprintf("забрать из %s", p.Role)
	case "wait":
		s = fmt.Sprintf("ждать %s ×%d в %s", p.Item, p.N, p.Role)
	case "explore":
		s = fmt.Sprintf("разведать карту: искать %s", p.Item)
	case "idle":
		s = fmt.Sprintf("подождать %d с (%s: всё оставшееся временно не ставится)", p.N, p.Item)
	case "research":
		s = "запустить исследование " + p.Item
	case "await":
		s = "ждать исследования " + p.Item
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

// smeltProduct — что получится из ore в печи.
func (s *State) smeltProduct(ore string) string {
	for name, rec := range s.Protos.Recipes {
		if rec.Category == "smelting" && len(rec.Ingredients) == 1 && rec.Ingredients[0].Name == ore && len(rec.Products) > 0 && !strings.HasPrefix(name, "parameter") {
			return rec.Products[0].Name
		}
	}
	return ""
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
