package cluster

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Нефть (cluster.md §2а «Нефть», stages.md — стадия 7): месторождение — качалки на каждой
// скважине, трубы между ними, столбы; выход — конец трубы у ближней к базе качалки (оттуда —
// трасса на базу).

// OilFields — месторождений нефти на игру (решение пользователя: обычно 2).
const OilFields = 2

// IsOil — кластер добычи нефти.
func (c *Cluster) IsOil() bool { return c.Kind == Mine && c.Product == "crude-oil" }

// MoreOil — кластеры добычи нефти до OilFields на месторождениях в зонах добычи, ближние к
// голове bus-1 первыми (уже стоящие — в счёт); зон с нефтью мало — дописываются (MoreMineZones).
func (b *Base) MoreOil(zones []Zone) ([]Zone, []*Cluster, error) {
	b.grid()
	var head world.Tile
	for _, z := range zones {
		if z.Name == "bus-1" {
			head = z.Rect.Min
		}
	}
	have := 0
	for _, c := range b.Clusters {
		if c.IsOil() {
			have++
		}
	}
	inZone := func(p world.Patch) *Zone {
		for i, z := range zones {
			if z.Kind == ZoneMine && z.Resources["crude-oil"] > 0 && z.Rect.Overlaps(Rect{p.Min, world.Tile{X: p.Max.X + 1, Y: p.Max.Y + 1}}) {
				return &zones[i]
			}
		}
		return nil
	}
	pts := oilFields(b.W.Patches())
	oil := 0.0
	sort.Slice(pts, func(i, j int) bool {
		return math.Hypot(pts[i].Center.X-float64(head.X), pts[i].Center.Y-float64(head.Y)) <
			math.Hypot(pts[j].Center.X-float64(head.X), pts[j].Center.Y-float64(head.Y))
	})
	n := 0
	for _, p := range pts {
		if inZone(p) != nil {
			n++
			oil += float64(p.Amount)
		}
	}
	for k := 0; n < OilFields && k < len(pts); k++ { // зон мало — ещё одна на следующем пятне
		zs := b.MoreMineZones(zones, "crude-oil", oil+1)
		if len(zs) == len(zones) {
			break
		}
		zones = zs
		n, oil = 0, 0
		for _, p := range pts {
			if inZone(p) != nil {
				n++
				oil += float64(p.Amount)
			}
		}
	}
	var res []*Cluster
	for _, p := range pts {
		if have >= OilFields {
			break
		}
		z := inZone(p)
		if z == nil {
			continue
		}
		r := Rect{world.Tile{X: p.Min.X - 3, Y: p.Min.Y - 3}, world.Tile{X: p.Max.X + 4, Y: p.Max.Y + 4}}
		if b.taken(r) {
			continue // уже есть кластер на этом пятне
		}
		c := &Cluster{Kind: Mine, Product: "crude-oil", Reserve: 1, Area: r, Rect: r,
			Units: []Unit{{Recipe: "mine:crude-oil", Machine: "pumpjack", Machines: float64(len(p.Tiles))}}}
		zr := z.Rect
		c.Zone, c.Near = &zr, &head
		c.Count, c.Room = len(p.Tiles), len(p.Tiles)
		c.Name = b.name(c.Product, res)
		b.Clusters = append(b.Clusters, c)
		if b.g != nil {
			b.g.mark(b.occupied(c))
		}
		res = append(res, c)
		have++
	}
	if have < OilFields {
		return zones, res, fmt.Errorf("cluster: месторождений нефти %d из %d", have, OilFields)
	}
	return zones, res, nil
}

// oilFields — месторождения нефти: пятна, чьи рамки (+3 — качалка) задевают друг друга, — одно
// месторождение (кластер на них один, 238178543).
func oilFields(all []world.Patch) []world.Patch {
	var res []world.Patch
	grow := func(p world.Patch) Rect {
		return Rect{world.Tile{X: p.Min.X - 3, Y: p.Min.Y - 3}, world.Tile{X: p.Max.X + 4, Y: p.Max.Y + 4}}
	}
	for _, p := range all {
		if p.Name != "crude-oil" {
			continue
		}
		res = append(res, p)
	}
	for merged := true; merged; {
		merged = false
		for i := 0; i < len(res) && !merged; i++ {
			for j := i + 1; j < len(res); j++ {
				if !grow(res[i]).Overlaps(grow(res[j])) {
					continue
				}
				a, b := res[i], res[j]
				n := float64(len(a.Tiles) + len(b.Tiles))
				a.Center = bridge.Pos{X: (a.Center.X*float64(len(a.Tiles)) + b.Center.X*float64(len(b.Tiles))) / n,
					Y: (a.Center.Y*float64(len(a.Tiles)) + b.Center.Y*float64(len(b.Tiles))) / n}
				a.Tiles = append(slices.Clone(a.Tiles), b.Tiles...)
				a.Amount += b.Amount
				a.Min = world.Tile{X: min(a.Min.X, b.Min.X), Y: min(a.Min.Y, b.Min.Y)}
				a.Max = world.Tile{X: max(a.Max.X, b.Max.X), Y: max(a.Max.Y, b.Max.Y)}
				res[i] = a
				res = append(res[:j], res[j+1:]...)
				merged = true
				break
			}
		}
	}
	return res
}

// Tanks — склад жидкости fluid (cluster.md §2а «Нефть»): блок cols×rows цистерн 3×3, вне зон
// у near (у головы своей шины).
func Tanks(fluid string, cols, rows int, near world.Tile) *Cluster {
	c := &Cluster{Kind: Tank, Product: fluid, Reserve: 1, Near: &near,
		Units: []Unit{{Recipe: "store:" + fluid, Machine: "storage-tank", Machines: float64(cols * rows)}}}
	c.Cross, c.Pitch, c.Head = 3*rows, 3, 0
	c.Count, c.Room, c.Need = cols, cols, cols
	return c
}

// Refinery — НПЗ (cluster.md §2а «Нефть»): простая переработка (нефть → газ), n заводов в столбец
// этажа переработки: труба нефти с одной стороны, газа — с другой.
func Refinery(p *knowledge.Prototypes, n int) *Cluster {
	r := p.Recipes["basic-oil-processing"]
	speed := p.Entities["oil-refinery"].CraftingSpeed
	in, out := 0.0, 0.0
	for _, i := range r.Ingredients {
		in += i.Amount
	}
	for _, o := range r.Products {
		out += o.Amount
	}
	k := float64(n) * speed / r.Energy
	c := &Cluster{Kind: Make, Product: "petroleum-gas", Reserve: 1,
		Units: []Unit{{Recipe: "basic-oil-processing", Machine: "oil-refinery", Machines: float64(n)}},
		In:    []Flow{{"crude-oil", in * k}}, Out: []Flow{{"petroleum-gas", out * k}},
		PowerW: float64(n) * p.Entities["oil-refinery"].EnergyUsage * 60}
	c.Cross, c.Pitch, c.Head = 7, 5, 1
	c.Count, c.Room, c.Need = n, n, n
	return c
}

// IsRefinery — кластер НПЗ (свой шаблон).
func (c *Cluster) IsRefinery() bool { return c.Kind == Make && c.machine() == "oil-refinery" }

// refineryRow — НПЗ повёрнуты набок: входы — к трубе нефти (v = 0), выходы — к трубе газа (v = 6);
// вход нефти — снизу у головы, выход газа — в конце ряда (в шину над этажом).
func refineryRow(l *layout.Layout, f frame, c *Cluster, L int, tag string, add func(layout.Placement) error) error {
	for i := 0; i < c.Count; i++ {
		m := f.at(l, "oil-refinery", c.Head+i*c.Pitch, 1, layout.S, tag) // в кадре S — входы к −v
		m.Recipe = c.Units[0].Recipe
		if err := add(m); err != nil {
			return err
		}
	}
	for u := 0; u < c.Head+c.Count*c.Pitch; u++ {
		if err := add(layout.Placement{Name: "pipe", Pos: f.tile(u, 0).Center(), Tag: tag + "/crude-oil"}); err != nil {
			return err
		}
	}
	for u := c.Head; u < L; u++ {
		if err := add(layout.Placement{Name: "pipe", Pos: f.tile(u, 6).Center(), Tag: tag + "/petroleum-gas"}); err != nil {
			return err
		}
	}
	c.Ports = append(c.Ports,
		Port{T: f.tile(-1, 0), Dir: f.dir(layout.E), Item: "crude-oil", In: true},
		Port{T: f.tile(L, 6), Dir: f.dir(layout.E), Item: "petroleum-gas"})
	return nil
}

// PipeLaneTag — тег труб жидкости fluid на шине bus (cluster.md §5: жидкости на шине — трубами).
func PipeLaneTag(bus, fluid string) string { return Tag(bus) + "/" + fluid }

// PipeLane — труба жидкости fluid вдоль шины k от её левого края до end: в свободном ряду лент,
// соседи по паре — не трубы; занятое (подземки лент поперёк, чужие жидкостные соединения) —
// подземной парой. Возвращает тег трубы (к ней подключаются JoinPipe).
func (b *Base) PipeLane(l *layout.Layout, buses []*ZBus, k int, fluid string, end int) (string, error) {
	bus := buses[k]
	tag := PipeLaneTag(bus.Name, fluid)
	used := map[int]bool{}
	for _, ln := range bus.Lanes {
		used[ln.Y-bus.Rect.Min.Y] = true
	}
	pipeRow := func(v int) bool { // в ряду уже труба шины
		for x := bus.Rect.Min.X; x < bus.Rect.Min.X+80; x++ {
			if p := l.At(world.Tile{X: x, Y: bus.Rect.Min.Y + v}); p != nil && strings.HasPrefix(p.Tag, Tag(bus.Name)+"/") {
				return true
			}
		}
		return false
	}
	for j := 0; laneV(j) < bus.Rect.H(); j++ {
		v := laneV(j)
		if used[v] || pipeRow(v) || pipeRow(v-1) || pipeRow(v+1) {
			continue
		}
		// начало — у левого края; у головы тесно (подводы с добычи, переброски — 565937761) —
		// правее: к трубе подключаются в любом месте (JoinPipe)
		for _, dx := range []int{0, 12, 24, 36} {
			mark := l.Mark()
			if err := pipeAlong(l, bus.Rect.Min.X+dx, end, bus.Rect.Min.Y+v, tag); err == nil {
				return tag, nil
			}
			l.Rollback(mark)
		}
	}
	return "", fmt.Errorf("cluster: %s: нет ряда под трубу %s", bus.Name, fluid)
}

// pipeAlong — труба по ряду y от x0 до x1: обычная, где тайл свободен и рядом нет чужих
// жидкостных соединений; иначе — подземная пара через занятое (до дальности прототипа).
func pipeAlong(l *layout.Layout, x0, x1, y int, tag string) error {
	const pipe, ptg = "pipe", "pipe-to-ground"
	maxUG := l.P.Entities[ptg].MaxUndergroundDistance
	own := map[world.Tile]bool{}
	ok := func(t world.Tile) bool { return l.Free(t, layout.AnyLand) && !fluidNear(l, t, tag) }
	for x := x0; x <= x1; {
		t := world.Tile{X: x, Y: y}
		if ok(t) {
			l.Add(layout.Placement{Name: pipe, Pos: t.Center(), Tag: tag})
			own[t] = true
			x++
			continue
		}
		// занято: вход подземки — на прошлом тайле (он обычная труба → заменить), выход — за занятым
		prev := world.Tile{X: x - 1, Y: y}
		i, isOwn := l.Index(prev)
		if !isOwn || l.List[i].Tag != tag || l.List[i].Name != pipe {
			return fmt.Errorf("cluster: труба %s: не обойти (%d, %d)", tag, x, y)
		}
		// выход — где за ним ещё тайл свободен (там следующий вход подземки, если дальше снова
		// занято): «лента, просвет в тайл, лента…» у головы bus-1 — подводы с добычи, 260647970
		out := -1
		for k := 1; k <= maxUG && x+k <= x1+maxUG; k++ {
			if t := (world.Tile{X: x + k, Y: y}); ok(t) && (x+k >= x1 || ok(world.Tile{X: x + k + 1, Y: y})) {
				out = x + k
				break
			}
		}
		for k := 1; out < 0 && k <= maxUG && x+k <= x1+maxUG; k++ {
			if ok(world.Tile{X: x + k, Y: y}) {
				out = x + k
			}
		}
		if out < 0 {
			return fmt.Errorf("cluster: труба %s: не обойти (%d, %d)", tag, x, y)
		}
		l.Replace(i, layout.Placement{Name: ptg, Pos: prev.Center(), Dir: layout.W, Tag: tag}) // вход: наземное — назад
		l.Add(layout.Placement{Name: ptg, Pos: world.Tile{X: out, Y: y}.Center(), Dir: layout.E, Tag: tag})
		x = out + 1
	}
	return nil
}

// UnderLane — труба шины tag в ряду y — подземная везде, где к ней ничего не подходит (решение
// пользователя: иначе потом обходить): обычная труба остаётся на концах и у отводов (соседняя
// не своя по ряду труба или соединение), между ними — подземные пары (до дальности прототипа).
func UnderLane(l *layout.Layout, tag string, y int) {
	const pipe, ptg = "pipe", "pipe-to-ground"
	maxUG := l.P.Entities[ptg].MaxUndergroundDistance
	var xs []int
	for _, p := range l.List {
		if p.Tag == tag && world.TileOf(p.Pos).Y == y {
			xs = append(xs, world.TileOf(p.Pos).X)
		}
	}
	if len(xs) < 2 {
		return
	}
	sort.Ints(xs)
	keep := map[int]bool{xs[0]: true, xs[len(xs)-1]: true}
	for _, x := range xs {
		t := world.Tile{X: x, Y: y}
		if p := l.At(t); p != nil && p.Name != pipe {
			keep[x] = true // уже подземка (обход занятого) — на месте
		}
		for _, d := range []int{layout.N, layout.S} {
			n := layout.Step(t, d)
			if q := l.At(n); q != nil && (q.Name == pipe || q.Name == ptg || len(layout.Connections(l.P, *q)) > 0) {
				keep[x] = true // отвод
			}
		}
	}
	// подряд идущие обычные трубы между оставленными — подземными парами
	var drop []world.Tile
	var add []layout.Placement
	run := func(a, b int) { // a, b — оставленные; трубы (a, b) — свободны для замены
		for x := a + 1; x+1 < b; {
			out := min(x+maxUG, b-1)
			add = append(add,
				layout.Placement{Name: ptg, Pos: world.Tile{X: x, Y: y}.Center(), Dir: layout.W, Tag: tag},
				layout.Placement{Name: ptg, Pos: world.Tile{X: out, Y: y}.Center(), Dir: layout.E, Tag: tag})
			for k := x; k <= out; k++ {
				drop = append(drop, world.Tile{X: k, Y: y})
			}
			x = out + 1
		}
	}
	prev := xs[0]
	for i := 1; i < len(xs); i++ {
		x := xs[i]
		if x != xs[i-1]+1 { // разрыв (подземка обхода) — участок кончился
			prev = x
			continue
		}
		if keep[x] {
			if x-prev > 2 {
				run(prev, x)
			}
			prev = x
		}
	}
	if len(drop) == 0 {
		return
	}
	gone := map[world.Tile]bool{}
	for _, t := range drop {
		gone[t] = true
	}
	l.List = slices.DeleteFunc(l.List, func(p layout.Placement) bool {
		return p.Tag == tag && p.Name == pipe && gone[world.TileOf(p.Pos)]
	})
	l.Rollback(-1)
	for _, p := range add {
		l.Add(p)
	}
}

// UnderPipes — все трубы шин buses (ряды труб жидкостей) — UnderLane.
func UnderPipes(l *layout.Layout, buses []*ZBus) {
	// ряд трубы шины — где тайлов с её тегом больше всего (подходы отводов — тем же тегом, но
	// поперёк)
	rows := map[string]map[int]int{}
	for _, bus := range buses {
		prefix := Tag(bus.Name) + "/"
		for _, p := range l.List {
			if (p.Name == "pipe" || p.Name == "pipe-to-ground") && strings.HasPrefix(p.Tag, prefix) {
				if rows[p.Tag] == nil {
					rows[p.Tag] = map[int]int{}
				}
				rows[p.Tag][world.TileOf(p.Pos).Y]++
			}
		}
	}
	tags := slices.Sorted(maps.Keys(rows))
	for _, tag := range tags {
		best, n := 0, -1
		for y, k := range rows[tag] {
			if k > n || k == n && y < best {
				best, n = y, k
			}
		}
		UnderLane(l, tag, best)
	}
}

// fluidNear — рядом с t жидкостное соединение не своей трубы tag (обычная труба смешала бы).
func fluidNear(l *layout.Layout, t world.Tile, tag string) bool {
	for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
		n := layout.Step(t, d)
		p := l.At(n)
		if p == nil || p.Tag == tag {
			continue
		}
		if p.Name == "pipe" {
			return true
		}
		for _, c := range layout.Connections(l.P, *p) {
			if c.At == n && c.To == t {
				return true
			}
		}
	}
	return false
}

// JoinLane — порт жидкости p кластера c — трубой к трубе шины tag (JoinPipe): от тайла порта.
func JoinLane(l *layout.Layout, c *Cluster, p Port, tag string) error {
	at := layout.Step(p.T, (p.Dir+8)%16) // тайл трубы кластера у края
	if p.In {
		at = layout.Step(p.T, p.Dir)
	}
	if l.At(p.T) == nil {
		delete(l.Reserved, p.T) // порт закрыт до подключения (guardPorts) — жидкость лентой не открывается
	}
	from := layout.Conn{At: at, To: p.T, Dir: (p.Dir + 8) % 16}
	if !p.In {
		from.Dir = p.Dir
	}
	if _, err := l.JoinPipe(from, tag); err != nil {
		return &ConnectError{c, fmt.Errorf("труба %s ↔ %s: %w", c.Name, tag, err)}
	}
	return nil
}

// TankJoin — блок цистерн tank — трубой к трубе шины tag (с ближнего к ней свободного соединения).
func TankJoin(l *layout.Layout, tank *Cluster, tag string) error {
	for _, cn := range tankConns(l, tank) {
		if _, err := l.JoinPipe(cn, tag); err == nil {
			return nil
		}
	}
	return &ConnectError{tank, fmt.Errorf("цистерны %s → %s: трубу не провести", tank.Name, tag)}
}

// fluidPort — порт жидкости fluid кластера c.
func fluidPort(c *Cluster, fluid string, in bool) (Port, bool) {
	for _, p := range c.Ports {
		if p.Item == fluid && p.In == in {
			return p, true
		}
	}
	return Port{}, false
}

// RefineryPipes — трубы НПЗ ref: нефть — с трубы bus-1 (из склада tank), газ — в трубу bus-2
// (cluster.md §2а «Нефть»: всё через шину). end — докуда тянуть трубу газа (потребители).
func (b *Base) RefineryPipes(l *layout.Layout, buses []*ZBus, ref, tank *Cluster, gasEnd int) (string, error) {
	crude, err := b.PipeLane(l, buses, 0, "crude-oil", ref.Area.Max.X+10)
	if err != nil {
		return "", err
	}
	if err := TankJoin(l, tank, crude); err != nil {
		return "", err
	}
	in, _ := fluidPort(ref, "crude-oil", true)
	if err := JoinLane(l, ref, in, crude); err != nil {
		return "", err
	}
	if len(buses) < 2 {
		return "", fmt.Errorf("cluster: нет шины над переработкой")
	}
	gas, err := b.PipeLane(l, buses, 1, "petroleum-gas", max(gasEnd, ref.Area.Max.X+20))
	if err != nil {
		return "", err
	}
	out, _ := fluidPort(ref, "petroleum-gas", false)
	return gas, JoinLane(l, ref, out, gas)
}

// Chem — химия (cluster.md §2а «Нефть»): n химзаводов рецепта recipe (пластик — газ + уголь,
// сера — газ + вода) в столбец этажа переработки.
func Chem(p *knowledge.Prototypes, recipe string, n int) *Cluster {
	r := p.Recipes[recipe]
	k := float64(n) * p.Entities["chemical-plant"].CraftingSpeed / r.Energy
	c := &Cluster{Kind: Make, Product: r.Products[0].Name, Reserve: 1,
		Units:  []Unit{{Recipe: recipe, Machine: "chemical-plant", Machines: float64(n)}},
		PowerW: float64(n) * p.Entities["chemical-plant"].EnergyUsage * 60}
	for _, i := range r.Ingredients {
		c.In = append(c.In, Flow{i.Name, i.Amount * k})
	}
	for _, o := range r.Products {
		c.Out = append(c.Out, Flow{o.Name, o.Amount * k})
	}
	c.Cross, c.Pitch, c.Head = 10, 4, 1
	if c.fluids(p) == 2 {
		c.Cross = 12 // вторая жидкость — трубой под лентой выхода (chemRow)
	}
	c.Count, c.Room, c.Need = n, n, n
	return c
}

// IsChem — кластер химзаводов (свой шаблон).
func (c *Cluster) IsChem() bool { return c.Kind == Make && c.machine() == "chemical-plant" }

// fluids — сколько жидкостей на входе.
func (c *Cluster) fluids(p *knowledge.Prototypes) int {
	n := 0
	for _, f := range c.In {
		if _, ok := p.Fluids[f.Item]; ok {
			n++
		}
	}
	return n
}

// chemRow — химзаводы в ряд (шаг 4), повёрнуты входами жидкости к +v: поперёк — 2 лента входа
// (предмет), 3 инсертеры, 4–6 химзаводы, 7 — трубы к входам жидкости и длинный инсертер выхода
// (через ряд 8 — на ленту 9), 8 — труба газа (вход сверху, с bus-2), 9 — лента выхода. Две
// жидкости (сера): к входу воды — труба в ряду 8 (вода — снизу, с bus-1), к входу газа —
// подземная труба под рядами 8–9 в ряд 11 (газ сверху).
func chemRow(l *layout.Layout, f frame, c *Cluster, L int, tag string, add func(layout.Placement) error,
	row func(v, from, to, d int) error, deadEnd func(v, to int)) error {
	two := c.fluids(l.P) == 2
	rowTag := func(fluid string) string { return tag + "/" + fluid }
	gasV, waterV := 8, -1
	if two {
		gasV, waterV = 11, 8
	}
	recipe := c.Units[0].Recipe
	ings := l.P.Recipes[recipe].Ingredients
	for i := 0; i < c.Count; i++ {
		u := c.Head + i*c.Pitch
		m := f.at(l, "chemical-plant", u, 4, layout.S, tag)
		m.Recipe = recipe
		if err := add(m); err != nil {
			return err
		}
		// входы жидкости — по порядку ингредиентов (ячейки по порядку соединений)
		var ins []layout.Conn
		for _, cn := range layout.Connections(l.P, m) {
			if cn.Flow == "input" {
				ins = append(ins, cn)
			}
		}
		k := 0
		for _, ing := range ings {
			if _, fl := l.P.Fluids[ing.Name]; !fl || k >= len(ins) {
				continue
			}
			cn := ins[k]
			k++
			switch {
			case ing.Name == "water" && two, !two:
				if err := add(layout.Placement{Name: "pipe", Pos: cn.To.Center(), Tag: rowTag(ing.Name)}); err != nil {
					return err
				}
			default: // газ при двух жидкостях — подземкой под водой и лентой выхода
				if err := add(layout.Placement{Name: "pipe-to-ground", Pos: cn.To.Center(), Dir: (cn.Dir + 8) % 16, Tag: rowTag(ing.Name)}); err != nil {
					return err
				}
				exit := cn.To
				for j := 0; j < gasV-1-7; j++ {
					exit = layout.Step(exit, cn.Dir)
				}
				if err := add(layout.Placement{Name: "pipe-to-ground", Pos: exit.Center(), Dir: cn.Dir, Tag: rowTag(ing.Name)}); err != nil {
					return err
				}
			}
		}
		if err := add(f.inserter(l, "inserter", u+1, 3, 2, tag)); err != nil {
			return err
		}
		if err := add(f.inserter(l, "long-handed-inserter", u+1, 7, 5, tag)); err != nil {
			return err
		}
	}
	end := c.Head + c.Count*c.Pitch - 1
	pipeRow := func(v int, fluid string, from, to int) error {
		for u := from; u < to; u++ {
			t := f.tile(u, v)
			if l.At(t) != nil {
				continue
			}
			if err := add(layout.Placement{Name: "pipe", Pos: t.Center(), Tag: rowTag(fluid)}); err != nil {
				return err
			}
		}
		return nil
	}
	gas := "petroleum-gas"
	if err := pipeRow(gasV, gas, c.Head, L); err != nil {
		return err
	}
	c.Ports = append(c.Ports, Port{T: f.tile(L, gasV), Dir: f.dir(layout.W), Item: gas, In: true})
	if two {
		if err := pipeRow(waterV, "water", 0, end+1); err != nil {
			return err
		}
		c.Ports = append(c.Ports, Port{T: f.tile(-1, waterV), Dir: f.dir(layout.E), Item: "water", In: true})
	}
	// лента входа предмета (пластик — уголь): снизу у головы, с торца в ряду 1 и боком в голову
	for _, in := range c.In {
		if _, fl := l.P.Fluids[in.Item]; fl {
			continue
		}
		for _, b := range []struct{ u, v, d int }{{0, 2, layout.E}, {0, 1, layout.S}} {
			if err := add(f.at(l, "transport-belt", b.u, b.v, b.d, tag)); err != nil {
				return err
			}
		}
		c.Ports = append(c.Ports, Port{T: f.tile(-1, 1), Dir: f.dir(layout.E), Item: in.Item, In: true})
		if err := row(2, 1, end, layout.E); err != nil {
			return err
		}
		deadEnd(2, end)
	}
	if err := row(9, c.Head, L, layout.E); err != nil {
		return err
	}
	l.AllowEnd(f.tile(L, 9))
	c.Ports = append(c.Ports, Port{T: f.tile(L, 9), Dir: f.dir(layout.E), Item: c.Product})
	return nil
}

// ChemPipes — трубы химии: газ — с трубы bus-2 (tag gas), вода — своей трубой в bus-1 от своей
// помпы (stages.md, стадия 7: вода для химии — своя помпа и своя линия).
func (b *Base) ChemPipes(l *layout.Layout, buses []*ZBus, chems []*Cluster, gas string) error {
	end := 0
	water := false
	for _, c := range chems {
		end = max(end, c.Area.Max.X+10)
		_, w := fluidPort(c, "water", true)
		water = water || w
	}
	var wtag string
	if water {
		var err error
		if wtag, err = b.PipeLane(l, buses, 0, "water", end); err != nil {
			return err
		}
		head := world.Tile{X: buses[0].Rect.Min.X, Y: (buses[0].Rect.Min.Y + buses[0].Rect.Max.Y) / 2}
		// вода бывает далеко (238178543 — дальше 150 тайлов): труба — подземная, как нефтетрасса
		l.PipeUnder = true
		_, err = l.WaterPumpR(head, 400, wtag, func(pump layout.Placement) bool {
			if !l.CanPlace(pump, layout.OffOre) {
				return false
			}
			l.Add(pump)
			_, err := l.JoinPipe(layout.Connections(l.P, pump)[0], wtag)
			return err == nil
		})
		l.PipeUnder = false
		if err != nil {
			return fmt.Errorf("cluster: помпа воды для химии: %w", err)
		}
	}
	for _, c := range chems {
		if p, ok := fluidPort(c, "petroleum-gas", true); ok {
			if err := JoinLane(l, c, p, gas); err != nil {
				return err
			}
		}
		if p, ok := fluidPort(c, "water", true); ok {
			if err := JoinLane(l, c, p, wtag); err != nil {
				return err
			}
		}
	}
	return nil
}

// tankBlock — цистерны шахматкой: соседние повёрнуты на 90° — угловые соединения стыкуются,
// весь блок — одна ёмкость.
func tankBlock(l *layout.Layout, f frame, c *Cluster, tag string, add func(layout.Placement) error) error {
	rows := c.Cross / 3
	for i := 0; i < c.Count; i++ {
		for j := 0; j < rows; j++ {
			d := layout.N
			if (i+j)%2 == 1 {
				d = layout.E
			}
			if err := add(f.at(l, "storage-tank", 3*i, 3*j, d, tag)); err != nil {
				return err
			}
		}
	}
	return nil
}

// tankConns — свободные соединения цистерн блока c наружу (куда подвести трубу).
func tankConns(l *layout.Layout, c *Cluster) []layout.Conn {
	var res []layout.Conn
	in := func(t world.Tile) bool {
		return t.X >= c.Area.Min.X && t.X < c.Area.Max.X && t.Y >= c.Area.Min.Y && t.Y < c.Area.Max.Y
	}
	for _, p := range l.List {
		if p.Tag != Tag(c.Name) || p.Name != "storage-tank" {
			continue
		}
		for _, cn := range layout.Connections(l.P, p) {
			if !in(cn.To) && l.Free(cn.To, layout.AnyLand) {
				res = append(res, cn)
			}
		}
	}
	return res
}

// pumpEvery — насос на нефтетрассе через столько тайлов: участок трубы шире 320×320 стоит
// (default_pipeline_extent), с запасом (cluster.md §2а «Нефть»).
const pumpEvery = 280

// OilTrunk — нефтетрасса от месторождения field до склада tank (cluster.md §2а «Нефть»):
// подземными трубами везде, где можно; насосы — по пути каждые pumpEvery тайлов; столбы — рядом
// с трубой (соседние тайлы), до сети базы.
func (b *Base) OilTrunk(l *layout.Layout, field, tank *Cluster) error {
	if len(field.Ports) == 0 {
		return fmt.Errorf("cluster %s: нет выхода трубы", field.Name)
	}
	tag := LogisticsTag + "/" + field.Name
	defer unsolid(l, field)()
	// своя площадь — не закрыта: трасса выходит от трубы месторождения
	var opened []world.Tile
	for y := field.Area.Min.Y; y < field.Area.Max.Y; y++ {
		for x := field.Area.Min.X; x < field.Area.Max.X; x++ {
			if t := (world.Tile{X: x, Y: y}); l.Reserved[t] && l.At(t) == nil {
				delete(l.Reserved, t)
				opened = append(opened, t)
			}
		}
	}
	defer func() {
		for _, t := range opened {
			if l.At(t) == nil {
				l.Reserved[t] = true
			}
		}
	}()
	start := field.Ports[0].T
	goals := tankConns(l, tank)
	if len(goals) == 0 {
		return fmt.Errorf("cluster %s: к цистернам не подойти", tank.Name)
	}
	tx, ty := tank.Area.Center()
	to := world.Tile{X: int(tx), Y: int(ty)}
	// насосы — на прямой от начала к складу, через pumpEvery
	var froms []layout.Conn
	for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
		if n := layout.Step(start, d); l.Free(n, layout.AnyLand) && !l.Solid[n] {
			froms = append(froms, layout.Conn{At: start, To: n, Dir: d})
		}
	}
	if len(froms) == 0 {
		return fmt.Errorf("cluster %s: от трубы нефти не отойти", field.Name)
	}
	sort.Slice(froms, func(i, j int) bool { return dist(froms[i].To, to) < dist(froms[j].To, to) })
	cur := froms[0]
	n := (abs(start.X-to.X) + abs(start.Y-to.Y)) / pumpEvery
	l.PipeUnder = true
	defer func() { l.PipeUnder = false }()
	var legs [][]layout.Placement
	for k := 1; k <= n; k++ {
		p := world.Tile{X: start.X + (to.X-start.X)*k/(n+1), Y: start.Y + (to.Y-start.Y)*k/(n+1)}
		pump, in, out, ok := pumpNear(l, p, to, tag)
		if !ok {
			return fmt.Errorf("cluster %s: насосу нет места у (%d, %d)", field.Name, p.X, p.Y)
		}
		leg, err := l.RoutePipe(cur, in, tag)
		if err != nil {
			return fmt.Errorf("нефтетрасса %s → насос %d: %w", field.Name, k, err)
		}
		l.Add(pump)
		legs = append(legs, leg)
		cur = out
	}
	leg, err := l.RoutePipeTo(cur, goals, tag)
	if err != nil {
		return fmt.Errorf("нефтетрасса %s → %s: %w", field.Name, tank.Name, err)
	}
	legs = append(legs, leg)
	for _, leg := range legs {
		polesAlong(l, leg, tag)
	}
	return nil
}

// pumpNear — насос у тайла p, вход — с дальней от to стороны, выход — к to (вдоль главной оси),
// на свободной суше; входное и выходное соединения — на свободные тайлы.
func pumpNear(l *layout.Layout, p, to world.Tile, tag string) (layout.Placement, layout.Conn, layout.Conn, bool) {
	d := layout.E
	switch dx, dy := to.X-p.X, to.Y-p.Y; {
	case abs(dx) >= abs(dy) && dx < 0:
		d = layout.W
	case abs(dy) > abs(dx) && dy > 0:
		d = layout.S
	case abs(dy) > abs(dx):
		d = layout.N
	}
	for r := 0; r <= 20; r++ {
		for _, o := range ringOffsets(r) {
			t := world.Tile{X: p.X + o.X, Y: p.Y + o.Y}
			pl := layout.Placement{Name: "pump", Pos: layout.CenterAt(l.P, "pump", d, t), Dir: d, Tag: tag}
			if !l.CanPlace(pl, layout.AnyLand) || slices.ContainsFunc(layout.Tiles(l.P, pl), func(t world.Tile) bool { return l.Solid[t] }) {
				continue
			}
			var in, out layout.Conn
			for _, c := range layout.Connections(l.P, pl) {
				if c.Flow == "input" {
					in = c
				} else {
					out = c
				}
			}
			if !l.Free(in.To, layout.AnyLand) || !l.Free(out.To, layout.AnyLand) {
				continue
			}
			return pl, in, out, true
		}
	}
	return layout.Placement{}, layout.Conn{}, layout.Conn{}, false
}

// polesAlong — малые столбы вдоль трубы leg (соседние тайлы), не дальше 6 тайлов друг от друга
// (провод — 7.5): трасса питает свои насосы, качалки получают сеть от базы.
func polesAlong(l *layout.Layout, leg []layout.Placement, tag string) {
	const pole, step = "small-electric-pole", 6
	var pts []world.Tile // тайлы трассы по порядку, с промежутками подземок
	for i := len(leg) - 1; i >= 0; i-- {
		t := world.TileOf(leg[i].Pos)
		if len(pts) > 0 {
			last := pts[len(pts)-1]
			for k := 1; k < abs(t.X-last.X)+abs(t.Y-last.Y); k++ {
				pts = append(pts, world.Tile{X: last.X + sign(t.X-last.X)*k, Y: last.Y + sign(t.Y-last.Y)*k})
			}
		}
		pts = append(pts, t)
	}
	var lastPole *world.Tile
	for i, t := range pts {
		if lastPole != nil && abs(t.X-lastPole.X)+abs(t.Y-lastPole.Y) < step && i != len(pts)-1 {
			continue
		}
		for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
			n := layout.Step(t, d)
			pl := layout.Placement{Name: pole, Pos: n.Center(), Tag: tag}
			if l.CanPlace(pl, layout.AnyLand) && !l.Solid[n] {
				l.Add(pl)
				lastPole = &n
				break
			}
		}
	}
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}

// taken — рамку r задевает кластер базы.
func (b *Base) taken(r Rect) bool {
	for _, c := range b.Clusters {
		if c.Kind != Bus && c.Area.Overlaps(r) {
			return true
		}
	}
	return false
}

// oilField — внутренность добычи нефти c: качалка на каждой скважине в площади (соседняя
// задевает уже стоящую — мимо), выход — у ближней к голове базы; трубы от остальных — к уже
// проложенной трубе нефти (JoinPipe); выход трубы — порт c (трасса на базу).
func oilField(l *layout.Layout, c *Cluster, tag string, head world.Tile) error {
	var wells []world.Tile
	for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
		for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
			t := world.Tile{X: x, Y: y}
			if r, ok := l.W.ResourceAt(t); ok && r.Name == "crude-oil" {
				wells = append(wells, t)
			}
		}
	}
	dist := func(t world.Tile) float64 { return math.Hypot(float64(t.X-head.X), float64(t.Y-head.Y)) }
	sort.Slice(wells, func(i, j int) bool { return dist(wells[i]) < dist(wells[j]) })
	pipeTag := tag + "/oil"
	placed := 0
	for _, w := range wells {
		var err error = fmt.Errorf("не встаёт")
		for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
			pj := layout.Placement{Name: "pumpjack", Pos: bridge.Pos{X: float64(w.X) + 0.5, Y: float64(w.Y) + 0.5}, Dir: d, Tag: tag}
			if !l.CanPlace(pj, layout.AnyLand) {
				break // место занято при любом повороте
			}
			conn := layout.Connections(l.P, pj)[0]
			if !l.Free(conn.To, layout.AnyLand) {
				continue
			}
			mark := l.Mark()
			l.Add(pj)
			if placed == 0 { // первая (ближняя к базе) — начало трубы, отсюда трасса
				l.Add(layout.Placement{Name: "pipe", Pos: conn.To.Center(), Tag: pipeTag})
				c.Ports = append(c.Ports, Port{T: conn.To, Dir: conn.Dir, Item: "crude-oil"})
				err = nil
				break
			}
			if _, err = l.JoinPipe(conn, pipeTag); err == nil {
				break
			}
			l.Rollback(mark) // труба не подошла — другой поворот
		}
		if err == nil {
			placed++
		}
	}
	if placed == 0 {
		return fmt.Errorf("cluster %s: ни одной качалки", c.Name)
	}
	c.Count = placed
	return nil
}
