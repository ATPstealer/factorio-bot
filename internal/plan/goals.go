package plan

import (
	"fmt"
	"time"
)

// Goal — проверяемое состояние; Next раскладывает его до следующего примитива.
type Goal interface {
	Key() string
	String() string
	Satisfied(s *State) bool
	next(p *Planner, s *State, c *ctx) (*Primitive, error)
}

// HaveItem — в инвентаре не меньше N.
type HaveItem struct {
	Item string
	N    int
}

// Placed — своя постройка Entity с ролью Role стоит.
type Placed struct {
	Entity, Role string
}

// Supplied — в постройке роли Role не меньше N предмета.
type Supplied struct {
	Role, Item string
	N          int
}

// SmeltingLine — N каменных печей у патча Ore, в каждой топливо Fuel и руда (или уже
// готовые пластины). Роли печей: smelt-<ore>-1..N.
type SmeltingLine struct {
	Ore, Fuel string
	N         int
}

// Порции пополнения линии.
const (
	lineFuel = 5
	lineOre  = 10
	// Furnace — во что плавим (стадия 5: только каменные печи).
	Furnace = "stone-furnace"
)

func (g HaveItem) Key() string     { return fmt.Sprintf("have:%s:%d", g.Item, g.N) }
func (g HaveItem) String() string  { return fmt.Sprintf("иметь %s ×%d", g.Item, g.N) }
func (g Placed) Key() string       { return "placed:" + g.Role }
func (g Placed) String() string    { return fmt.Sprintf("поставить %s (%s)", g.Entity, g.Role) }
func (g Supplied) Key() string     { return fmt.Sprintf("supplied:%s:%s:%d", g.Role, g.Item, g.N) }
func (g Supplied) String() string  { return fmt.Sprintf("в %s %s ×%d", g.Role, g.Item, g.N) }
func (g SmeltingLine) Key() string { return fmt.Sprintf("smelt:%s:%d", g.Ore, g.N) }
func (g SmeltingLine) String() string {
	return fmt.Sprintf("линия плавки: %d печей на %s с %s", g.N, g.Ore, g.Fuel)
}

func (g HaveItem) Satisfied(s *State) bool { return s.Inventory[g.Item] >= g.N }
func (g Placed) Satisfied(s *State) bool   { return s.Role(g.Role) != nil }
func (g Supplied) Satisfied(s *State) bool {
	o := s.Role(g.Role)
	return o != nil && o.Count(g.Item) >= g.N
}

// LineRole — роль i-й печи линии.
func LineRole(ore string, i int) string { return fmt.Sprintf("smelt-%s-%d", ore, i) }

// furnaceOK — печь линии в порядке: есть топливо и есть что плавить (или уже наплавлено).
func (g SmeltingLine) furnaceOK(o *Owned) bool {
	if o == nil || o.In("fuel", g.Fuel) < 1 {
		return false
	}
	return o.In("crafter_input", g.Ore) > 0 || len(o.Inv["crafter_output"]) > 0
}

func (g SmeltingLine) Satisfied(s *State) bool {
	for i := 1; i <= g.N; i++ {
		if !g.furnaceOK(s.Role(LineRole(g.Ore, i))) {
			return false
		}
	}
	return true
}

// Ожидание выплавки: Wait-примитив с таймаутом.
func waitFor(n int) time.Duration { return time.Duration(float64(n)*3.5+10) * time.Second }

// ---------------------------------------------------------------------------------------
// Разложение

// ctx — текущая цепочка целей (для «почему» и защиты от циклов).
type ctx struct {
	why   []string
	stack map[string]bool
	depth int
}

func (c *ctx) push(g Goal) (*ctx, error) {
	if c.depth > 15 {
		return nil, fmt.Errorf("plan: слишком глубоко: %s", g)
	}
	if c.stack[g.Key()] {
		return nil, fmt.Errorf("plan: цикл целей на %s", g)
	}
	st := map[string]bool{g.Key(): true}
	for k := range c.stack {
		st[k] = true
	}
	return &ctx{why: append(append([]string(nil), c.why...), g.String()), stack: st, depth: c.depth + 1}, nil
}

func (c *ctx) prim(p Primitive) *Primitive {
	p.Why = append([]string(nil), c.why...)
	return &p
}

func (g HaveItem) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	have := s.Inventory[g.Item]
	if have >= g.N {
		return nil, nil
	}
	lack := g.N - have
	var tried []string

	// 1. забрать готовое из своих построек
	for _, o := range s.Owned {
		if o.Output(g.Item) > 0 {
			p := c.prim(Primitive{Kind: "take", Role: o.Role, Item: g.Item})
			if !pl.banned(p.Key()) {
				return p, nil
			}
		}
	}
	// 2. скрафтить руками
	if r, ok := s.craftRecipe(g.Item); ok {
		rec := s.Protos.Recipes[r]
		crafts := ceilDiv(lack, productAmount(rec, g.Item))
		for _, in := range rec.Ingredients {
			need := int(float64(crafts)*in.Mean() + 0.999)
			if s.Inventory[in.Name] < need {
				return sub(pl, s, c, HaveItem{in.Name, need})
			}
		}
		p := c.prim(Primitive{Kind: "craft", Recipe: r, Item: g.Item, N: crafts})
		if !pl.banned(p.Key()) {
			return p, nil
		}
		tried = append(tried, "крафт забанен")
	}
	// 3. переплавить в своей печи
	if _, ore, ok := s.smeltRecipe(g.Item); ok && !pl.banned("smelt:"+g.Item) {
		role := "smelter"
		if p, err := sub(pl, s, c, Placed{Furnace, role}); p != nil || err != nil {
			return p, err
		}
		o := s.Role(role)
		if o.In("fuel", "coal") < 1 {
			return sub(pl, s, c, Supplied{role, "coal", 2})
		}
		if have+o.Output(g.Item) < g.N {
			needOre := g.N - have - o.Output(g.Item)
			if o.In("crafter_input", ore) < needOre {
				return sub(pl, s, c, Supplied{role, ore, needOre})
			}
			return c.prim(Primitive{Kind: "wait", Role: role, Item: g.Item, N: needOre}), nil
		}
		return c.prim(Primitive{Kind: "take", Role: role, Item: g.Item}), nil
	}
	// 4. добыть с карты
	if s.Minable != nil && s.Minable(g.Item) {
		p := c.prim(Primitive{Kind: "gather", Item: g.Item, N: lack})
		if !pl.banned(p.Key()) {
			return p, nil
		}
		tried = append(tried, "добыча забанена")
	}
	return nil, fmt.Errorf("plan: не получить %s ×%d: нет способа %v", g.Item, lack, tried)
}

func (g Placed) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	if s.Role(g.Role) != nil {
		return nil, nil
	}
	e, ok := s.Protos.Entities[g.Entity]
	if !ok || len(e.ItemsToPlaceThis) == 0 {
		return nil, fmt.Errorf("plan: %s не ставится предметом", g.Entity)
	}
	item := e.ItemsToPlaceThis[0]
	if s.Inventory[item] < 1 {
		return sub(pl, s, c, HaveItem{item, 1})
	}
	spot, ok := s.FindSpot(s, g.Entity, g.Role, pl.badSpots())
	if !ok {
		return nil, fmt.Errorf("plan: нет места для %s (%s)", g.Entity, g.Role)
	}
	return c.prim(Primitive{Kind: "place", Item: item, Role: g.Role, Pos: spot}), nil
}

func (g Supplied) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	o := s.Role(g.Role)
	if o == nil {
		return nil, fmt.Errorf("plan: нет постройки %s", g.Role)
	}
	have := o.Count(g.Item)
	if have >= g.N {
		return nil, nil
	}
	lack := g.N - have
	if s.Inventory[g.Item] < lack {
		return sub(pl, s, c, HaveItem{g.Item, lack})
	}
	return c.prim(Primitive{Kind: "insert", Role: g.Role, Item: g.Item, N: lack}), nil
}

func (g SmeltingLine) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	// сначала собрать всё сразу на всю линию (печи, топливо, руда) — один поход за каждым,
	// а не по печке
	missing, fuel, ore := 0, 0, 0
	for i := 1; i <= g.N; i++ {
		o := s.Role(LineRole(g.Ore, i))
		if o == nil {
			missing++
			fuel += lineFuel
			ore += lineOre
			continue
		}
		if o.In("fuel", g.Fuel) < 1 {
			fuel += lineFuel - o.In("fuel", g.Fuel)
		}
		if !g.furnaceOK(o) {
			ore += lineOre - o.In("crafter_input", g.Ore)
		}
	}
	for _, need := range []HaveItem{{Furnace, missing}, {g.Fuel, fuel}, {g.Ore, ore}} {
		if need.N > 0 && s.Inventory[need.Item] < need.N {
			return sub(pl, s, c, need)
		}
	}
	for i := 1; i <= g.N; i++ {
		role := LineRole(g.Ore, i)
		o := s.Role(role)
		if o == nil {
			return sub(pl, s, c, Placed{Furnace, role})
		}
		if o.In("fuel", g.Fuel) < 1 {
			return sub(pl, s, c, Supplied{role, g.Fuel, lineFuel})
		}
		if !g.furnaceOK(o) {
			return sub(pl, s, c, Supplied{role, g.Ore, lineOre})
		}
	}
	return nil, nil
}

// sub — разложить подцель (с её звеном в цепочке «почему»).
func sub(pl *Planner, s *State, c *ctx, g Goal) (*Primitive, error) {
	cc, err := c.push(g)
	if err != nil {
		return nil, err
	}
	return g.next(pl, s, cc)
}
