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

// Нефть (cluster.md §2а «Нефть», stages.md — стадия 5): месторождение — качалки на каждой
// скважине, трубы между ними, столбы; выход — конец трубы у ближней к базе качалки (оттуда —
// трасса на базу).

// IsOil — кластер добычи нефти.
func (c *Cluster) IsOil() bool { return c.Kind == Mine && c.Product == "crude-oil" }

// OilWells — скважин нефти на игру не меньше (cluster.md §2а «Нефть»: 30 на минимуме — 60/с).
const OilWells = 30

// MoreOilWells — добыча нефти на месторождениях в зонах добычи, ближние к голове bus-1 первыми,
// каждое целиком (качалка на каждой скважине), пока скважин меньше wells; зон с нефтью мало —
// дописываются (MoreMineZones). Возвращает зоны, новые кластеры и сколько скважин набралось.
func (b *Base) MoreOilWells(zones []Zone, wells int) ([]Zone, []*Cluster, error) {
	b.grid()
	var head world.Tile
	for _, z := range zones {
		if z.Name == "bus-1" {
			head = z.Rect.Min
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
	// месторождение годится, если на нём нет чужих кластеров (под станцией или плавкой — не в счёт)
	grown := func(p world.Patch) Rect {
		return Rect{world.Tile{X: p.Min.X - 3, Y: p.Min.Y - 3}, world.Tile{X: p.Max.X + 4, Y: p.Max.Y + 4}}
	}
	// и до каждой скважины дойти пешком от базы (бот ставит качалку, стоя на суше в 8 тайлах):
	// месторождение на острове — мимо (954176679, стадия 34 на бумаге: «до pumpjack не дойти»)
	var mid world.Tile
	for _, z := range zones {
		if z.Name == "bus-1" {
			x, y := z.Rect.Center()
			mid = world.Tile{X: int(x), Y: int(y)}
		}
	}
	reach := b.landReach(mid)
	reachable := func(t world.Tile) bool {
		for dy := -8; dy <= 8; dy += landStep {
			for dx := -8; dx <= 8; dx += landStep {
				if reach(world.Tile{X: t.X + dx, Y: t.Y + dy}) {
					return true
				}
			}
		}
		return false
	}
	usable := func(p world.Patch) bool {
		for _, c := range b.Clusters {
			if c.Kind != Bus && !c.IsOil() && c.Area.Overlaps(grown(p)) {
				return false
			}
		}
		if slices.ContainsFunc(p.Tiles, func(t world.Tile) bool { return !reachable(t) }) {
			return false
		}
		return !slices.ContainsFunc(b.NoOil, grown(p).Overlaps) // трассу не провести — мимо
	}
	pts := oilFields(b.W.Patches())
	sort.Slice(pts, func(i, j int) bool {
		return math.Hypot(pts[i].Center.X-float64(head.X), pts[i].Center.Y-float64(head.Y)) <
			math.Hypot(pts[j].Center.X-float64(head.X), pts[j].Center.Y-float64(head.Y))
	})
	count := func() (n int, oil float64) {
		for _, p := range pts {
			if inZone(p) != nil {
				oil += float64(p.Amount)
				if usable(p) {
					n += len(p.Tiles)
				}
			}
		}
		return n, oil
	}
	for n, oil := count(); n < wells; n, oil = count() { // зон мало — ещё одна на следующем пятне
		zs := b.MoreMineZones(zones, "crude-oil", oil+1)
		if len(zs) == len(zones) {
			break
		}
		zones = zs
	}
	have := 0
	for _, c := range b.Clusters {
		if c.IsOil() {
			have += c.Count
		}
	}
	var res []*Cluster
	for _, p := range pts {
		if have >= wells {
			break
		}
		z := inZone(p)
		if z == nil || !usable(p) || b.taken(grown(p)) {
			continue
		}
		r := grown(p)
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
		have += c.Count
	}
	if have < wells {
		return zones, res, fmt.Errorf("cluster: скважин нефти %d из %d", have, wells)
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
	c.Cross, c.Pitch, c.Head = 7, refPitch, 1
	c.Count, c.Room, c.Need = n, n, n
	return c
}

// refPitch — шаг НПЗ (5×5) вдоль ряда: между заводами — тайл под столб (места под рост в конце
// ряда больше нет — столбу было негде встать: 427965634, 267145981).
const refPitch = 6

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
	// ряды лент заняты; ряд, отведённый зонами под жидкость (лента с жидкостью — место трубы,
	// лент там нет), — свободен, свой — первым (46082668, стадия 7: «нет ряда под трубу
	// lubricant» — ряды смазки, масел и кислоты считались лентами)
	used := map[int]bool{}
	own := -1
	for _, ln := range bus.Lanes {
		if _, fl := b.P.Fluids[ln.Item]; fl {
			if ln.Item == fluid {
				own = ln.Y - bus.Rect.Min.Y
			}
			continue
		}
		used[ln.Y-bus.Rect.Min.Y] = true
	}
	// ряды, вдоль которых уже идут трубы шины (у каждой — ряд, где её тайлов больше всего): отводы
	// той же трубы поперёк шины ряд не занимают (267145981, стадия 7: отводы к кластерам
	// пересекали все ряды bus-2 — «труба рядом» везде, кислоте ряда не нашлось)
	piped := map[int]bool{}
	tags := map[string]bool{}
	for _, p := range l.List {
		if (p.Name == "pipe" || p.Name == "pipe-to-ground") && strings.HasPrefix(p.Tag, Tag(bus.Name)+"/") {
			tags[p.Tag] = true
		}
	}
	for t := range tags {
		if y, ok := pipeLaneRow(l, t); ok {
			piped[y-bus.Rect.Min.Y] = true
		}
	}
	pipeRow := func(v int) bool { return piped[v] } // в ряду уже труба шины
	var rows []int
	if own >= 0 {
		rows = append(rows, own)
	}
	for j := 0; laneV(j) < bus.Rect.H(); j++ {
		if v := laneV(j); v != own {
			rows = append(rows, v)
		}
	}
	// рядов лент не хватило (в паре — одна труба) — средние ряды зазоров между парами
	// (cluster.md §5; 267145981, стадия 7: «нет ряда под трубу heavy-oil» — пятая труба bus-2)
	for j := 0; laneV(j)+3 < bus.Rect.H()-1; j += 2 {
		rows = append(rows, laneV(j)+3)
	}
	var why []string // разбор: почему не встал каждый ряд
	for _, v := range rows {
		switch {
		case used[v]:
			why = append(why, fmt.Sprintf("v%d лента", v))
			continue
		case pipeRow(v) || pipeRow(v-1) || pipeRow(v+1):
			why = append(why, fmt.Sprintf("v%d труба рядом", v))
			continue
		}
		// начало — у левого края; у головы тесно (подводы с добычи, переброски — 565937761) —
		// правее: к трубе подключаются в любом месте (JoinPipe)
		var last error
		for _, dx := range []int{0, 12, 24, 36} {
			mark := l.Mark()
			if last = pipeAlong(l, bus.Rect.Min.X+dx, end, bus.Rect.Min.Y+v, tag); last == nil {
				return tag, nil
			}
			l.Rollback(mark)
		}
		why = append(why, fmt.Sprintf("v%d %v", v, last))
	}
	return "", fmt.Errorf("cluster: %s: нет ряда под трубу %s (до x %d; %s)", bus.Name, fluid, end, strings.Join(why, "; "))
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
// fixed — обычные трубы, которые остаются как есть (дописывание плана: старые трубы шины
// построены — их конец при продлении не становится подземкой).
func UnderLane(l *layout.Layout, tag string, y int, fixed map[world.Tile]bool) {
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
		if p := l.At(t); p != nil && p.Name != pipe || fixed[t] {
			keep[x] = true // уже подземка (обход занятого) или построенная труба — на месте
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

// UnderPipes — все трубы шин buses (ряды труб жидкостей) — UnderLane; fixed — обычные трубы,
// которые не трогать (nil — все можно).
func UnderPipes(l *layout.Layout, buses []*ZBus, fixed map[world.Tile]bool) {
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
		UnderLane(l, tag, best, fixed)
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

// solidInputs — входов-предметов (не жидкостей): им нужны буферы в голове (cluster.md §6).
func (c *Cluster) solidInputs(p *knowledge.Prototypes) int { return len(c.In) - c.fluids(p) }

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
		if c.solidInputs(l.P) > 0 { // с ленты входа — только если на входе есть предметы (сера — газ и вода)
			if err := add(f.inserter(l, "inserter", u+1, 3, 2, tag)); err != nil {
				return err
			}
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
	// первая жидкость (газ, кислота батарей) — трубой с дальнего конца; вода (вторая) — снизу
	gas := ""
	for _, ing := range ings {
		if _, fl := l.P.Fluids[ing.Name]; fl && (ing.Name != "water" || !two) {
			gas = ing.Name
		}
	}
	if fluidAtHead(c, gas) { // жидкость — с шины у головы (батареи на снабжении: кислота с bus-2 снизу)
		if err := pipeRow(gasV, gas, 0, end+1); err != nil {
			return err
		}
		c.Ports = append(c.Ports, Port{T: f.tile(-1, gasV), Dir: f.dir(layout.E), Item: gas, In: true})
	} else {
		if err := pipeRow(gasV, gas, c.Head, L); err != nil {
			return err
		}
		c.Ports = append(c.Ports, Port{T: f.tile(L, gasV), Dir: f.dir(layout.W), Item: gas, In: true})
	}
	if two {
		if err := pipeRow(waterV, "water", 0, end+1); err != nil {
			return err
		}
		c.Ports = append(c.Ports, Port{T: f.tile(-1, waterV), Dir: f.dir(layout.E), Item: "water", In: true})
	}
	// лента входа предметов (пластик — уголь; батареи — железо | медь по полосам): снизу у
	// головы, предмет 1 с торца в ряду 1 боком в голову, предмет 2 — из ряда 3 (cluster.md §6б)
	var solids []string
	for _, in := range c.In {
		if _, fl := l.P.Fluids[in.Item]; !fl {
			solids = append(solids, in.Item)
		}
	}
	if len(solids) > 2 {
		return fmt.Errorf("cluster %s: предметов на входе %d — одна лента, две полосы", c.Name, len(solids))
	}
	if len(solids) > 0 {
		if err := add(f.at(l, "transport-belt", 0, 2, layout.E, tag)); err != nil {
			return err
		}
		for k, it := range solids {
			fv, d := 1, layout.S
			if k == 1 {
				fv, d = 3, layout.N
			}
			if err := add(f.at(l, "transport-belt", 0, fv, d, tag)); err != nil {
				return err
			}
			c.Ports = append(c.Ports, Port{T: f.tile(-1, fv), Dir: f.dir(layout.E), Item: it, In: true})
		}
		if err := row(2, 1, end, layout.E); err != nil {
			return err
		}
		deadEnd(2, end)
	}
	if err := row(9, c.Head, L, layout.E); err != nil {
		return err
	}
	// буфер выхода (предмет) — в зазоре перед последним химзаводом: длинный инсертер в ряду
	// инсертеров выхода берёт с ленты выхода через трубу и кладёт в сундук между химзаводами
	if err := outBuffer(l, f, c, c.Head+(c.Count-1)*c.Pitch-1, 7, 9, 5, tag, add); err != nil {
		return err
	}
	l.AllowEnd(f.tile(L, 9))
	c.Ports = append(c.Ports, Port{T: f.tile(L, 9), Dir: f.dir(layout.E), Item: c.Product})
	return nil
}

// WaterLane — труба воды химии в bus-1 до end от своей помпы (не от станции; stages.md, стадия 35):
// помпа — у ближней к голове шины воды, труба от неё — подземная, как нефтетрасса. Тег трубы.
func (b *Base) WaterLane(l *layout.Layout, buses []*ZBus, end int) (string, error) {
	wtag, err := b.PipeLane(l, buses, 0, "water", end)
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("cluster: помпа воды для химии: %w", err)
	}
	return wtag, nil
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

// HoldTankInlets — у каждого свободного соединения склада c — 3 тайла наружу (насос на входе и
// его вход) под запрет, пока прокладываются ленты: нефтетрассы идут после них, а ленты обступали
// склад вплотную — насосу на входе негде было встать (17466282). Возвращает снятие запрета.
func HoldTankInlets(l *layout.Layout, c *Cluster) func() {
	var held []world.Tile
	for _, cn := range tankConns(l, c) {
		for t, k := cn.To, 0; k < 3; t, k = layout.Step(t, cn.Dir), k+1 {
			if l.Free(t, layout.AnyLand) && !l.Reserved[t] {
				l.Reserved[t] = true
				held = append(held, t)
			}
		}
	}
	return func() {
		for _, t := range held {
			delete(l.Reserved, t)
		}
	}
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
	// через кластеры базы и шины — нет, ни поверху, ни подземкой (cluster.md §3, решение
	// пользователя 26.10): их площади закрыты на время трассы
	var shut []world.Tile
	rects := slices.Clone(b.busRects)
	for _, c := range b.Clusters {
		if c == field || c == tank || c.Kind == Bus || c.Area.W() == 0 {
			continue
		}
		rects = append(rects, c.Area)
	}
	for _, r := range rects {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if t := (world.Tile{X: x, Y: y}); !l.Solid[t] {
					l.Solid[t] = true
					shut = append(shut, t)
				}
			}
		}
	}
	// и суша, до которой от базы не дойти (острова): подземная труба на ней — не поставить
	reach := b.landReach(tank.Area.Min)
	box := Rect{world.Tile{X: min(field.Area.Min.X, tank.Area.Min.X), Y: min(field.Area.Min.Y, tank.Area.Min.Y)},
		world.Tile{X: max(field.Area.Max.X, tank.Area.Max.X), Y: max(field.Area.Max.Y, tank.Area.Max.Y)}}.Grow(60)
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			t := world.Tile{X: x, Y: y}
			own := x >= field.Area.Min.X && x < field.Area.Max.X && y >= field.Area.Min.Y && y < field.Area.Max.Y
			if !l.Solid[t] && !own && l.Free(t, layout.AnyLand) && !reach(t) {
				l.Solid[t] = true
				shut = append(shut, t)
			}
		}
	}
	defer func() {
		for _, t := range shut {
			delete(l.Solid, t)
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
	n := (abs(start.X-to.X) + abs(start.Y-to.Y)) / pumpEvery
	l.PipeUnder = true
	defer func() { l.PipeUnder = false }()
	// насосы — на прямой; отрезок не провёлся (насос встал в тесноте у подводов руды, из его
	// выхода не выйти — 565937761) — откатить и сдвинуть насосы вдоль прямой
	mark := l.Mark()
	var err error
	for _, off := range []int{0, 40, -40, 80, -80, 120} {
		if err = b.trunkLegs(l, field, tank, froms[0], start, to, goals, n, off, tag); err == nil {
			return nil
		}
		l.Rollback(mark)
		b.logf("нефтетрасса %s: насосы со сдвигом %d — %v", field.Name, off, err)
	}
	return err
}

// trunkLegs — отрезки нефтетрассы от cur (у трубы месторождения start) к складу to: n насосов на
// прямой start → to через равные доли, сдвинутые вдоль прямой на off тайлов; последний отрезок —
// к любому из goals; столбы вдоль всех отрезков.
func (b *Base) trunkLegs(l *layout.Layout, field, tank *Cluster, cur layout.Conn, start, to world.Tile, goals []layout.Conn, n, off int, tag string) error {
	var legs [][]layout.Placement
	length := max(abs(start.X-to.X)+abs(start.Y-to.Y), 1)
	// сдвиг удлиняет крайний отрезок на |off| — насосов столько, чтобы и он был не длиннее
	// pumpEvery (17555262: сдвиг 120 — отрезок 340 тайлов, участок шире 320)
	if a := abs(off); n > 0 && a > 0 && a < pumpEvery {
		n = max(n, (length+pumpEvery-a-1)/(pumpEvery-a)-1)
		if sp := length / (n + 1); sp <= a { // насос ушёл бы за начало или конец трассы
			return fmt.Errorf("cluster %s: сдвиг насосов %d больше шага %d", field.Name, off, sp)
		}
	}
	for k := 1; k <= n; k++ {
		p := world.Tile{X: start.X + (to.X-start.X)*(k*length/(n+1)+off)/length, Y: start.Y + (to.Y-start.Y)*(k*length/(n+1)+off)/length}
		pump, in, out, ok := pumpNear(l, p, to, tag)
		if !ok {
			return fmt.Errorf("cluster %s: насосу нет места у (%d, %d)", field.Name, p.X, p.Y)
		}
		// место насоса — закрыто на время отрезка: труба шла через него (238178543, стадия 5:
		// подземная труба и насос на одном тайле)
		var shut []world.Tile
		for _, t := range layout.Tiles(l.P, pump) {
			if !l.Solid[t] {
				l.Solid[t] = true
				shut = append(shut, t)
			}
		}
		leg, err := l.RoutePipe(cur, in, tag)
		for _, t := range shut {
			delete(l.Solid, t)
		}
		if err != nil {
			return fmt.Errorf("нефтетрасса %s → насос %d у (%d, %d): %w", field.Name, k, p.X, p.Y, err)
		}
		// отрезок (первый — с трубами месторождения) — не шире участка игры: насос, нашедший место
		// далеко от прямой, растягивал его (1330900585: 132×456 — насосу не встать, нефть стоит)
		box := leg
		if k == 1 {
			for _, q := range l.List {
				if strings.HasPrefix(q.Tag, Tag(field.Name)+"/") && (q.Name == "pipe" || q.Name == "pipe-to-ground") {
					box = append(box, q)
				}
			}
		}
		if w := pipeExtent(box); w > layout.PipeExtentGame {
			return fmt.Errorf("нефтетрасса %s → насос %d у (%d, %d): отрезок %d шире %d", field.Name, k, p.X, p.Y, w, layout.PipeExtentGame)
		}
		l.Add(pump)
		legs = append(legs, leg)
		cur = out
	}
	leg, err := routeInlet(l, cur, goals, tag)
	if err != nil { // соединения склада заняты насосами других трасс — в трубу соседней у склада
		if jl, jerr := joinInlet(l, cur, tank, tag); jerr == nil {
			leg, err = jl, nil
		}
	}
	if err != nil {
		var near []string // разбор: что у начала последнего отрезка
		for _, p := range l.List {
			if t := world.TileOf(p.Pos); abs(t.X-cur.To.X) <= 2 && abs(t.Y-cur.To.Y) <= 2 {
				near = append(near, fmt.Sprintf("%v %s %s %s", t, p.Name, layout.DirName(p.Dir), p.Tag))
			}
		}
		b.logf("нефтетрасса %s %v → %s %v от (%d,%d), целей %d: %v; рядом %v", field.Name, field.Area, tank.Name, tank.Area, cur.To.X, cur.To.Y, len(goals), err, near)
		var gs []string // разбор: соединения склада и что вокруг него
		for _, g := range goals {
			gs = append(gs, fmt.Sprintf("%v→%v", g.At, g.To))
		}
		b.logf("склад %s: соединения %v", tank.Name, gs)
		b.dumpBetween(l, tank.Area.Min, tank.Area.Max)
		return fmt.Errorf("нефтетрасса %s → %s: %w", field.Name, tank.Name, err)
	}
	legs = append(legs, leg)
	for _, leg := range legs {
		polesAlong(l, leg, tag)
	}
	return nil
}

// pipeExtent — большая сторона рамки труб из list (как участок игры, default_pipeline_extent).
func pipeExtent(list []layout.Placement) int {
	if len(list) == 0 {
		return 0
	}
	lo, hi := world.TileOf(list[0].Pos), world.TileOf(list[0].Pos)
	for _, p := range list {
		t := world.TileOf(p.Pos)
		lo.X, lo.Y, hi.X, hi.Y = min(lo.X, t.X), min(lo.Y, t.Y), max(hi.X, t.X), max(hi.Y, t.Y)
	}
	return max(hi.X-lo.X+1, hi.Y-lo.Y+1)
}

// routeInlet — последний отрезок трассы tag от cur в склад — через насос на входе (cluster.md §2а
// «Нефть», решение пользователя: цистерны участок не делят — без него трасса, склад и труба к
// потребителям одним участком шире 320: стадия 36 на 427965634, нефть не шла). goals —
// соединения склада, пробуются от ближнего к cur; насос — в конце отрезка (столбы и к нему).
func routeInlet(l *layout.Layout, cur layout.Conn, goals []layout.Conn, tag string) ([]layout.Placement, error) {
	gs := slices.Clone(goals)
	sort.Slice(gs, func(i, j int) bool { return dist(gs[i].To, cur.To) < dist(gs[j].To, cur.To) })
	err := fmt.Errorf("у склада насосу нет места")
	for _, g := range gs {
		pump, in, ok := inletPump(l, g, tag)
		if !ok {
			continue
		}
		mark := l.Mark()
		l.Add(pump)
		leg, rerr := l.RoutePipe(cur, in, tag)
		if rerr == nil {
			return append(leg, pump), nil
		}
		err = rerr
		l.Rollback(mark)
	}
	return nil, err
}

// joinInlet — последний отрезок трассы tag от cur — не в склад tank (его соединения заняты
// насосами других трасс: месторождений бывает 7 — 427965634), а через свой насос в трубу другой
// трассы у самого склада (ближе inletCut): участок до насоса — свой, после — короткий общий с
// её последним отрезком до насоса на входе склада.
func joinInlet(l *layout.Layout, cur layout.Conn, tank *Cluster, tag string) ([]layout.Placement, error) {
	var pipes []world.Tile
	for _, p := range l.List {
		if p.Name == "pipe" && p.Tag != tag && strings.HasPrefix(p.Tag, LogisticsTag+"/") && rectDist(tank.Area, world.TileOf(p.Pos)) <= inletCut {
			pipes = append(pipes, world.TileOf(p.Pos))
		}
	}
	sort.Slice(pipes, func(i, j int) bool { return dist(pipes[i], cur.To) < dist(pipes[j], cur.To) })
	err := fmt.Errorf("у склада ни насосу на входе, ни трубы другой трассы")
	for _, t := range pipes {
		for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
			q := layout.Step(t, d)
			if !l.Free(q, layout.AnyLand) || l.Solid[q] {
				continue
			}
			pump, in, ok := inletPump(l, layout.Conn{At: t, To: q, Dir: d}, tag)
			if !ok {
				continue
			}
			mark := l.Mark()
			l.Add(pump)
			leg, rerr := l.RoutePipe(cur, in, tag)
			if rerr == nil {
				return append(leg, pump), nil
			}
			err = rerr
			l.Rollback(mark)
		}
	}
	return nil, err
}

// inletCut — на сколько тайлов от склада конец построенной трассы перекладывается через насос
// на входе (InletPumps).
const inletCut = 10

// InletPumps — план, построенный до насоса на входе склада: конец каждой нефтетрассы у склада
// tank (её трубы ближе inletCut тайлов к нему и подземные пары, задетые ими) — заново, через
// насос на входе (cluster.md §2а «Нефть»); площади кластеров, кроме месторождений и склада, —
// закрыты, как у трассы. Возвращает убранное из раскладки (в игре — снять).
func InletPumps(l *layout.Layout, clusters []*Cluster, tank *Cluster) ([]layout.Placement, error) {
	if l.Solid == nil { // раскладка построенного плана — не из разметки
		l.Solid = map[world.Tile]bool{}
	}
	var shut []world.Tile
	for _, c := range clusters {
		if c == tank || c.IsOil() || c.Area.W() == 0 {
			continue
		}
		for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
			for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
				if t := (world.Tile{X: x, Y: y}); !l.Solid[t] {
					l.Solid[t] = true
					shut = append(shut, t)
				}
			}
		}
	}
	defer func() {
		for _, t := range shut {
			delete(l.Solid, t)
		}
	}()
	l.PipeUnder = true
	defer func() { l.PipeUnder = false }()
	var removed []layout.Placement
	for _, field := range clusters {
		if !field.IsOil() {
			continue
		}
		tag := LogisticsTag + "/" + field.Name
		if hasInlet(l, tag, tank) {
			continue
		}
		pipe := func(p layout.Placement) bool {
			return p.Tag == tag && (p.Name == "pipe" || p.Name == "pipe-to-ground")
		}
		cut := map[world.Tile]bool{}
		for _, p := range l.List {
			if pipe(p) && rectDist(tank.Area, world.TileOf(p.Pos)) <= inletCut {
				cut[world.TileOf(p.Pos)] = true
			}
		}
		// подземная пара — целиком
		for more := true; more; {
			more = false
			for t := range cut {
				if p := l.At(t); p.Name == "pipe-to-ground" {
					if m, ok := ugPartner(l, *p); ok && !cut[m] {
						cut[m], more = true, true
					}
				}
			}
		}
		if len(cut) == 0 {
			continue
		}
		// откуда продолжить: соединение оставшейся части трассы в убранное
		var cur *layout.Conn
		for _, p := range l.List {
			if p.Tag != tag || cut[world.TileOf(p.Pos)] || !pipe(p) && p.Name != "pump" {
				continue
			}
			for _, c := range layout.Connections(l.P, p) {
				if cut[c.To] && c.At == world.TileOf(p.Pos) {
					cur = &c
				}
			}
		}
		if cur == nil {
			return removed, fmt.Errorf("нефтетрасса %s: у склада %s не найти, откуда перекладывать", field.Name, tank.Name)
		}
		var kept []layout.Placement
		for _, p := range l.List {
			if p.Tag == tag && cut[world.TileOf(p.Pos)] {
				removed = append(removed, p)
			} else {
				kept = append(kept, p)
			}
		}
		l.List = kept
		l.Rollback(-1)
		leg, err := routeInlet(l, *cur, tankConns(l, tank), tag)
		if err != nil {
			return removed, fmt.Errorf("нефтетрасса %s: вход склада %s через насос: %w", field.Name, tank.Name, err)
		}
		polesAlong(l, leg, tag)
	}
	// новый отрезок бывает частью по старым местам (та же подземная пара) — такое не убирается
	now := map[layout.Placement]bool{}
	for _, p := range l.List {
		now[p] = true
	}
	return slices.DeleteFunc(removed, func(p layout.Placement) bool { return now[p] }), nil
}

// hasInlet — у трассы tag уже есть насос на входе склада tank (выход — в его цистерну).
func hasInlet(l *layout.Layout, tag string, tank *Cluster) bool {
	for _, p := range l.List {
		if p.Tag != tag || p.Name != "pump" {
			continue
		}
		for _, c := range layout.Connections(l.P, p) {
			if c.Flow != "input" && rectDist(tank.Area, c.To) == 0 {
				return true
			}
		}
	}
	return false
}

// ugPartner — тайл пары подземной трубы p (того же тега): по оси подземной стороны, ближняя,
// наземной стороной навстречу.
func ugPartner(l *layout.Layout, p layout.Placement) (world.Tile, bool) {
	d := (p.Dir + 8) % 16 // подземная сторона — против наземной
	t := world.TileOf(p.Pos)
	for k := 1; k <= l.P.Entities["pipe-to-ground"].MaxUndergroundDistance+1; k++ {
		t = layout.Step(t, d)
		if q := l.At(t); q != nil && q.Name == "pipe-to-ground" && q.Tag == p.Tag && q.Dir == (p.Dir+8)%16 {
			return t, true
		}
	}
	return world.Tile{}, false
}

// rectDist — расстояние от тайла t до прямоугольника r по большей оси (внутри — 0).
func rectDist(r Rect, t world.Tile) int {
	dx := max(r.Min.X-t.X, 0, t.X-(r.Max.X-1))
	dy := max(r.Min.Y-t.Y, 0, t.Y-(r.Max.Y-1))
	return max(dx, dy)
}

// inletPump — насос на входе склада у его соединения g (At — тайл цистерны, To — снаружи):
// выход — в цистерну, вход — снаружи, на свободный тайл не у чужой трубы.
func inletPump(l *layout.Layout, g layout.Conn, tag string) (layout.Placement, layout.Conn, bool) {
	d := (g.Dir + 8) % 16 // поток — к цистерне
	for _, t := range []world.Tile{g.To, layout.Step(g.To, g.Dir)} {
		pl := layout.Placement{Name: "pump", Pos: layout.CenterAt(l.P, "pump", d, t), Dir: d, Tag: tag}
		if !l.CanPlace(pl, layout.AnyLand) || slices.ContainsFunc(layout.Tiles(l.P, pl), func(t world.Tile) bool { return l.Solid[t] }) {
			continue
		}
		var in layout.Conn
		fits := false
		for _, c := range layout.Connections(l.P, pl) {
			if c.Flow == "input" {
				in = c
			} else if c.At == g.To && c.To == g.At {
				fits = true
			}
		}
		if fits && l.Free(in.To, layout.AnyLand) && !l.Solid[in.To] && !fluidNear(l, in.To, tag) {
			return pl, in, true
		}
	}
	return layout.Placement{}, layout.Conn{}, false
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
	// до 100 тайлов: прямая к складу идёт через базу — точка насоса внутри неё, место — снаружи
	// (46082668: нефть с востока за базой)
	for r := 0; r <= 100; r++ {
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
			// вход и выход — не рядом с чужой трубой (565937761: насос встал на линию трубы воды —
			// выход сразу «не провести»)
			if !l.Free(in.To, layout.AnyLand) || !l.Free(out.To, layout.AnyLand) || fluidNear(l, in.To, tag) || fluidNear(l, out.To, tag) {
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
			if placed == 0 { // первая (ближняя к базе) — начало трубы
				l.Add(layout.Placement{Name: "pipe", Pos: conn.To.Center(), Tag: pipeTag})
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
	// выход (трасса на базу) — у ближней к голове трубы, от которой есть куда отойти: у первой
	// качалки бывает тесно (старый подвод угля через пятно нефти — 565937761)
	var pipes []world.Tile
	for _, p := range l.List {
		if p.Tag == pipeTag && p.Name == "pipe" {
			pipes = append(pipes, world.Tile{X: int(math.Floor(p.Pos.X)), Y: int(math.Floor(p.Pos.Y))})
		}
	}
	sort.SliceStable(pipes, func(i, j int) bool { return dist(pipes[i]) < dist(pipes[j]) })
	for _, t := range pipes {
		for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
			if n := layout.Step(t, d); l.Free(n, layout.AnyLand) && !l.Solid[n] {
				c.Ports = append(c.Ports, Port{T: t, Dir: d, Item: "crude-oil"})
				c.Count = placed
				return nil
			}
		}
	}
	return fmt.Errorf("cluster %s: от трубы нефти не отойти", c.Name)
}
