package plan

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"factorio-bot/internal/world"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
)

// Built — раскладка стоит в игре: каждое размещение — своя постройка в реестре
// (роль BuildRole(Tag, i)).
type Built struct {
	Tag  string
	List []layout.Placement
	// Groups — строить только размещения с этими тегами (кластер и его входящие ленты);
	// пусто — все. Роли — по индексу в полном List.
	Groups []string
	// Order — очередь групп (этап кластерами): строить по одной — кластер начинает работать,
	// не дожидаясь материалов на весь этап
	Order [][]string
}

// in — размещение i входит в строимое.
func (g Built) in(i int) bool {
	if len(g.Groups) == 0 {
		return true
	}
	for _, t := range g.Groups {
		if g.List[i].Tag == t {
			return true
		}
	}
	return false
}

// BuildRole — роль i-го размещения раскладки tag.
func BuildRole(tag string, i int) string { return fmt.Sprintf("%s-%d", tag, i+1) }

func (g Built) Key() string { return "built:" + g.Tag + ":" + strings.Join(g.Groups, ",") }
func (g Built) String() string {
	if len(g.Groups) > 0 {
		n := 0
		for i := range g.List {
			if !g.in(i) {
				continue
			}
			if g.in(i) {
				n++
			}
		}
		return fmt.Sprintf("построить %s (%d построек)", strings.TrimPrefix(g.Groups[0], "c/"), n)
	}
	return fmt.Sprintf("построить %s (%d построек)", g.Tag, len(g.List))
}
func (g Built) Satisfied(s *State) bool {
	if len(g.missing(s)) > 0 || len(g.unfueled(s)) > 0 || len(g.empty(s)) > 0 {
		return false
	}
	for i, p := range g.List {
		if !g.in(i) {
			continue
		}
		o := s.Role(BuildRole(g.Tag, i))
		if o.Dir != nil && !sameDir(s, p.Name, *o.Dir, p.Dir) && rotatable(s, p.Name) {
			return false
		}
		if p.Recipe != "" && o.Recipe != p.Recipe && s.Protos.Entities[p.Name].Type == "assembling-machine" {
			return false
		}
		if p.Name == "underground-belt" && (o.UGType != "" && o.UGType != p.Recipe || o.Dir != nil && *o.Dir != p.Dir) {
			return false
		}
	}
	return true
}

// fuelFor — сколько угля кладём в burner-постройку за раз: печам и бойлерам — с запасом
// (жгут постоянно), бурам — на ~4 минуты (уголь — 27 с работы бура), инсертерам — немного.
func fuelFor(s *State, entity string) int {
	switch s.Protos.Entities[entity].Type {
	case "furnace", "boiler":
		return 20
	case "mining-drill":
		return 10
	case "container": // сундук-кормушка с углём-сырьём (пластик: 9 угля/мин — ≈45 мин)
		return 400
	}
	return 5
}

// unfueled — поставленные burner-постройки без топлива (заправить; прогорело — снова).
func (g Built) unfueled(s *State) []string {
	var res []string
	for i, p := range g.List {
		if !g.in(i) {
			continue
		}
		role := BuildRole(g.Tag, i)
		// уголь — сырьё рецепта (пластик): пустой сундук-кормушка — наполнить
		if eatsCoal(s, p.Recipe) {
			if o := s.Role(role); o != nil && o.Status != "working" {
				if ch := s.Role(feeders(s)[role]); ch != nil && ch.In("chest", "coal") == 0 {
					res = append(res, ch.Role)
				}
			}
			continue
		}
		if s.Protos.Entities[p.Name].Burner == nil {
			continue
		}
		// кластер: печи и бойлеры — уголь лентой (руками не носить); burner-инсертеру —
		// начальный уголь (без топлива не возьмёт и с ленты)
		if strings.HasPrefix(p.Tag, "c/") && s.Protos.Entities[p.Name].Type != "inserter" {
			continue
		}
		if o := s.Role(role); o != nil && len(o.Inv["fuel"]) == 0 && o.Status != "working" && !g.selfFed(s, o) {
			if ch := s.Role(feeders(s)[role]); ch != nil && ch.In("chest", "coal") > 0 {
				continue // кормится из сундука
			}
			res = append(res, role)
		}
	}
	return res
}

func (g Built) missing(s *State) []int {
	var res []int
	for i := range g.List {
		if !g.in(i) {
			continue
		}
		if s.Role(BuildRole(g.Tag, i)) == nil {
			res = append(res, i)
		}
	}
	return res
}

// layer — порядок стройки: машины → инсертеры → ленты и трубы → столбы (ленты после
// инсертеров: инсертер, поставленный рядом с лентой, не мешает; столбы — последними,
// чтобы не занять место машин).
func layer(p *knowledge.Prototypes, entity string) int {
	switch p.Entities[entity].Type {
	case "inserter":
		return 1
	case "transport-belt", "underground-belt", "splitter", "pipe", "pipe-to-ground":
		return 2
	case "electric-pole":
		return 3
	}
	return 0
}

// sameDir — направления совпадают; у симметричных (паровой двигатель) d и d+8 — одно.
func sameDir(s *State, entity string, a, b int) bool {
	if s.Protos.Entities[entity].Type == "generator" {
		return a%8 == b%8
	}
	return a == b
}

// rotatable — направление важно и меняется клавишей R (помпу игра ставит к воде сама).
func rotatable(s *State, entity string) bool {
	switch s.Protos.Entities[entity].Type {
	case "mining-drill", "inserter", "transport-belt", "splitter", "boiler", "generator", "pipe-to-ground":
		return true
	}
	return false
}

// chestFeedTarget — сколько угля держать в сундуке-кормушке (бойлер на малой нагрузке
// жжёт мало — хватает надолго).
const chestFeedTarget = 300

// feeders — постройки, которые кормятся углём из сундука через инсертер (топливо или
// сырьё рецепта — пластик): роль постройки → роль сундука (по геометрии инсертеров).
func feeders(s *State) map[string]string {
	res := map[string]string{}
	at := func(t world.Tile) *Owned {
		for _, o := range s.Owned {
			d := 0
			if o.Dir != nil {
				d = *o.Dir
			}
			for _, x := range layout.Tiles(s.Protos, layout.Placement{Name: o.Name, Pos: o.Pos, Dir: d}) {
				if x == t {
					return o
				}
			}
		}
		return nil
	}
	for _, o := range s.Owned {
		if s.Protos.Entities[o.Name].Type != "inserter" || o.Dir == nil {
			continue
		}
		pick, drop := layout.InserterEnds(s.Protos, layout.Placement{Name: o.Name, Pos: o.Pos, Dir: *o.Dir})
		src, dst := at(pick), at(drop)
		if src != nil && dst != nil && src.Name == Chest && (s.Protos.Entities[dst.Name].Burner != nil || eatsCoal(s, dst.Recipe)) {
			res[dst.Role] = src.Role
		}
	}
	return res
}

// supplyTarget — сколько держать в сундуке снабжения (Placement.Recipe — предмет): его
// наполняет бот (пластик, сера, сталь для синей науки), подливает, когда меньше четверти.
const supplyTarget = 100

// empty — сундуки снабжения раскладки, где предмета нет совсем.
func (g Built) empty(s *State) []string {
	var res []string
	for i, p := range g.List {
		if !g.in(i) {
			continue
		}
		if p.Name != Chest || p.Recipe == "" {
			continue
		}
		if o := s.Role(BuildRole(g.Tag, i)); o != nil && o.In("chest", p.Recipe) == 0 {
			res = append(res, o.Role)
		}
	}
	return res
}

// supply — подлить в сундуки снабжения, где меньше четверти.
func (g Built) supply(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	for i, p := range g.List {
		if !g.in(i) {
			continue
		}
		if p.Name != Chest || p.Recipe == "" {
			continue
		}
		o := s.Role(BuildRole(g.Tag, i))
		if o == nil || o.In("chest", p.Recipe) >= supplyTarget/4 {
			continue
		}
		if pr, err := sub(pl, s, c, Supplied{o.Role, p.Recipe, supplyTarget}); pr != nil || err != nil {
			return pr, err
		}
	}
	return nil, nil
}

// eatsCoal — рецепт берёт уголь как сырьё.
func eatsCoal(s *State, recipe string) bool {
	for _, in := range s.Protos.Recipes[recipe].Ingredients {
		if in.Name == "coal" {
			return true
		}
	}
	return false
}

// selfFed — бур угольной пары: заправляет его сосед, пока у пары есть уголь.
func (g Built) selfFed(s *State, o *Owned) bool {
	if _, ore, ok := moduleOf(o.Role); !ok || ore != "coal" {
		return false
	}
	for i := range g.List {
		if !g.in(i) {
			continue
		}
		if p := s.Role(BuildRole(g.Tag, i)); p != nil && (len(p.Inv["fuel"]) > 0 || p.Status == "working") {
			return true
		}
	}
	return false
}

func (g Built) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	miss := g.missing(s)
	if len(miss) == 0 {
		// стоит, но повёрнуто не так (поворот мог не дойти) — довернуть
		for i, p := range g.List {
			if !g.in(i) {
				continue
			}
			role := BuildRole(g.Tag, i)
			if o := s.Role(role); o != nil && o.Dir != nil && !sameDir(s, p.Name, *o.Dir, p.Dir) && rotatable(s, p.Name) {
				pr := c.prim(Primitive{Kind: "rotate", Role: role, Dir: p.Dir, Pos: p.Pos, Item: s.Protos.ItemFor(p.Name)})
				if !pl.banned(pr.Key()) {
					return pr, nil
				}
			}
		}
		// подземка не того типа или не туда (спарилась не с той) — пересобрать; сначала
		// входы: выход встаёт парой только к правильному входу
		for pass := 0; pass < 2; pass++ {
			for i, p := range g.List {
				if !g.in(i) {
					continue
				}
				if p.Name != "underground-belt" || (p.Recipe == "input") != (pass == 0) {
					continue
				}
				role := BuildRole(g.Tag, i)
				if o := s.Role(role); o != nil && (o.UGType != "" && o.UGType != p.Recipe || o.Dir != nil && *o.Dir != p.Dir) {
					pr := c.prim(Primitive{Kind: "rebuild", Item: s.Protos.ItemFor(p.Name), Dir: p.Dir, Spots: []bridge.Pos{p.Pos}, Roles: []string{role}})
					if !pl.banned(pr.Key()) {
						return pr, nil
					}
				}
			}
		}
		// сборщик без нужного рецепта — задать
		for i, p := range g.List {
			if !g.in(i) {
				continue
			}
			role := BuildRole(g.Tag, i)
			if o := s.Role(role); o != nil && p.Recipe != "" && o.Recipe != p.Recipe && s.Protos.Entities[p.Name].Type == "assembling-machine" {
				if !s.Av.Recipe(p.Recipe) { // рецепт ещё закрыт — сначала технология
					if t := s.Protos.TechFor(p.Recipe); t != "" {
						return sub(pl, s, c, Researched{Tech: t})
					}
				}
				pr := c.prim(Primitive{Kind: "recipe", Role: role, Recipe: p.Recipe})
				if !pl.banned(pr.Key()) {
					return pr, nil
				}
			}
		}
		if p, err := g.supply(pl, s, c); p != nil || err != nil {
			return p, err
		}
		return g.fuel(pl, s, c)
	}
	// материалы на всё недостроенное разом
	need := map[string]int{}
	for _, i := range miss {
		it := s.Protos.ItemFor(g.List[i].Name)
		if it == "" {
			return nil, fmt.Errorf("plan: %s не из чего ставить", g.List[i].Name)
		}
		need[it]++
	}
	items := make([]string, 0, len(need))
	for it := range need {
		items = append(items, it)
	}
	sort.Strings(items)
	for _, it := range items {
		if s.Inventory[it] < need[it] {
			return sub(pl, s, c, HaveItem{it, need[it]})
		}
	}
	// группа «предмет + направление» нижнего слоя; обычные ленты — без направления (трасса
	// с поворотами — одним проходом, рука доворачивается); забаненные — пропустить
	type key struct {
		layer, dir int
		name       string
	}
	groups := map[key][]int{}
	var keys []key
	for _, i := range miss {
		p := g.List[i]
		k := key{layer(s.Protos, p.Name), p.Dir, p.Name}
		if s.Protos.Entities[p.Name].Type == "transport-belt" {
			k.dir = -1
		}
		if groups[k] == nil {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], i)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].layer != keys[b].layer {
			return keys[a].layer < keys[b].layer
		}
		if keys[a].name != keys[b].name {
			return keys[a].name < keys[b].name
		}
		return keys[a].dir < keys[b].dir
	})
	// кластер (группа этапа): всё разом с мест стояния — встал, поставил всё, что достаёт
	// (машины → инсертеры → ленты → столбы), дальше; без обхода кластера по слою на каждый
	// предмет. Подземки — парами, как ниже
	if len(g.Groups) > 0 {
		p := c.prim(Primitive{Kind: "build", Item: "*", Area: g.area(s)})
		for _, k := range keys {
			if k.name == "underground-belt" {
				continue
			}
			for _, i := range g.order(groups[k], s.PlayerPos) {
				p.Spots = append(p.Spots, g.List[i].Pos)
				p.Roles = append(p.Roles, BuildRole(g.Tag, i))
				p.Dirs = append(p.Dirs, g.List[i].Dir)
				p.Items = append(p.Items, s.Protos.ItemFor(g.List[i].Name))
			}
		}
		if len(p.Spots) > 0 && !pl.banned(p.Key()) {
			return p, nil
		}
	}
	for _, k := range keys {
		idx := g.order(groups[k], s.PlayerPos)
		// подземки — парами «вход, его выход» отдельными шагами: игра делает выходом
		// подземку после непарного входа в пределах дальности — чужой вход рядом спутает
		chunks := [][]int{idx}
		if k.name == "underground-belt" {
			chunks = nil
			for a := 0; a < len(idx); a += 2 {
				chunks = append(chunks, idx[a:min(a+2, len(idx))])
			}
		}
		for _, ch := range chunks {
			p := c.prim(Primitive{Kind: "build", Item: s.Protos.ItemFor(k.name), Dir: max(k.dir, 0), Area: g.area(s)})
			for _, i := range ch {
				p.Spots = append(p.Spots, g.List[i].Pos)
				p.Roles = append(p.Roles, BuildRole(g.Tag, i))
				if k.dir < 0 {
					p.Dirs = append(p.Dirs, g.List[i].Dir)
				}
			}
			if k.dir < 0 {
				p.Dir = p.Dirs[0]
			}
			if !pl.banned(p.Key()) {
				return p, nil
			}
		}
	}
	// всё оставшееся временно забанено (место было занято) — подождать, пока баны истекут
	return c.prim(Primitive{Kind: "idle", N: 30, Item: g.Tag}), nil
}

// fuel — уголь во все незаправленные: набрать на всех разом, потом раскладывать.
func (g Built) fuel(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	roles := g.unfueled(s)
	if len(roles) == 0 {
		return nil, nil
	}
	// сначала набрать на всех (много — бур на угле: немного руками в бур, остальное из его
	// сундука), только потом раскладывать — иначе уголь для бура уйдёт в постройки
	need := 0
	for _, r := range roles {
		need += fuelFor(s, s.Role(r).Name)
	}
	if s.Inventory["coal"] < need {
		return sub(pl, s, c, HaveItem{"coal", need})
	}
	// угля много — с запасом: поровну на всех незаправленных, до полного слота
	ns, caps := make([]int, len(roles)), make([]int, len(roles))
	for i, r := range roles {
		ns[i], caps[i] = fuelFor(s, s.Role(r).Name), fuelCap(s, s.Role(r).Name)
	}
	spreadExtra(ns, caps, s.Inventory["coal"]-need)
	return sub(pl, s, c, Supplied{roles[0], "coal", ns[0]})
}

// order — порядок постройки в группе: обход ближайшего соседа от бота (меньше ходьбы); подземки —
// парами «вход, его выход»: игра делает выходом подземку, поставленную после
// непарного входа в пределах дальности.
func (g Built) order(idx []int, from bridge.Pos) []int {
	if len(idx) == 0 {
		return idx
	}
	if g.List[idx[0]].Name == "underground-belt" {
		var res []int
		used := map[int]bool{}
		for _, i := range idx {
			if g.List[i].Recipe != "input" {
				continue
			}
			res = append(res, i)
			in := g.List[i]
			best, bd := -1, math.Inf(1)
			for _, j := range idx {
				o := g.List[j]
				if o.Recipe != "output" || used[j] {
					continue
				}
				dx, dy := o.Pos.X-in.Pos.X, o.Pos.Y-in.Pos.Y
				if ahead(in.Dir, dx, dy) && math.Hypot(dx, dy) < bd {
					best, bd = j, math.Hypot(dx, dy)
				}
			}
			if best >= 0 {
				used[best] = true
				res = append(res, best)
			}
		}
		return res
	}
	// от бота: первая — ближайшая к нему, дальше — ближайший сосед
	var res []int
	left := append([]int(nil), idx...)
	for len(left) > 0 {
		last := from
		if len(res) > 0 {
			last = g.List[res[len(res)-1]].Pos
		}
		bi := 0
		for k, i := range left {
			if dist(g.List[i].Pos, last) < dist(g.List[left[bi]].Pos, last) {
				bi = k
			}
		}
		res = append(res, left[bi])
		left = append(left[:bi], left[bi+1:]...)
	}
	return res
}

// area — рамка всей раскладки.
func (g Built) area(s *State) *bridge.Box {
	if len(g.List) == 0 {
		return nil
	}
	b := bridge.Box{LeftTop: bridge.Pos{X: math.Inf(1), Y: math.Inf(1)}, RightBottom: bridge.Pos{X: math.Inf(-1), Y: math.Inf(-1)}}
	for _, p := range g.List {
		for _, t := range layout.Tiles(s.Protos, p) {
			b.LeftTop.X, b.LeftTop.Y = math.Min(b.LeftTop.X, float64(t.X)), math.Min(b.LeftTop.Y, float64(t.Y))
			b.RightBottom.X, b.RightBottom.Y = math.Max(b.RightBottom.X, float64(t.X+1)), math.Max(b.RightBottom.Y, float64(t.Y+1))
		}
	}
	// вытянутая раскладка (лента на сотни тайлов, линия столбов): «выйти с площадки» —
	// уйти за край карты; там замуровать себя нечем — не выходить
	if b.RightBottom.X-b.LeftTop.X > 60 || b.RightBottom.Y-b.LeftTop.Y > 60 {
		return nil
	}
	return &b
}

// ahead — смещение (dx, dy) строго по направлению d.
func ahead(d int, dx, dy float64) bool {
	switch d {
	case layout.N:
		return dx == 0 && dy < 0
	case layout.E:
		return dy == 0 && dx > 0
	case layout.S:
		return dx == 0 && dy > 0
	}
	return dy == 0 && dx < 0
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// Site — раскладка задачи kind (State.Design: "research" — станция и лаборатории,
// "science" — фабрика красной науки) построена и заправлена.
type Site struct{ Kind string }

func (g Site) Key() string    { return "site:" + g.Kind }
func (g Site) String() string { return "построить " + g.Kind }
func (g Site) Satisfied(s *State) bool {
	if s.Design == nil {
		return false
	}
	b, ok := s.Design(s, g.Kind)
	return ok && b.Satisfied(s)
}

// siteResource — раскладка ставится на ресурс, которого может не быть на открытой карте.
var siteResource = map[string]string{"oil": "crude-oil", "oil-jacks": "crude-oil"}

func (g Site) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	if s.Design == nil {
		return nil, fmt.Errorf("plan: %s: негде проектировать", g.Kind)
	}
	if r := siteResource[g.Kind]; r != "" && s.Minable != nil && !s.Minable(r) {
		return c.prim(Primitive{Kind: "explore", Item: r}), nil
	}
	// снабжение стройки работает — нулевой этап разобрать (точки на руде больше не нужны)
	if strings.HasPrefix(g.Kind, "stage-") && s.StartRemovable && !(Dismantled{}).Satisfied(s) {
		if p, err := sub(pl, s, c, Dismantled{}); p != nil || err != nil {
			return p, err
		}
	}
	// этап кластерами: без подземных лент ленты не пересекаются — кольцо из лент запирает
	// выход кластера; logistics — до проектирования (наукой руками, как электробуры)
	if strings.HasPrefix(g.Kind, "stage-") && !s.Av.All && !s.Av.Researched["logistics"] {
		if _, ok := s.Protos.Technologies["logistics"]; ok {
			return sub(pl, s, c, Researched{Tech: "logistics"})
		}
	}
	b, ok := s.Design(s, g.Kind)
	if !ok {
		return nil, fmt.Errorf("plan: %s: нет места", g.Kind)
	}
	// группа, где всё временно забанено (место занято), — не ждать: следующую
	var wait *Primitive
	for _, grp := range b.Order {
		gb := Built{Tag: b.Tag, List: b.List, Groups: grp}
		if gb.Satisfied(s) {
			continue
		}
		p, err := sub(pl, s, c, gb)
		if err != nil || p == nil || p.Kind != "idle" {
			return p, err
		}
		if wait == nil {
			wait = p
		}
	}
	if wait != nil {
		return wait, nil
	}
	return sub(pl, s, c, b)
}
