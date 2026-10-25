package cluster

import (
	"fmt"
	"math"
	"slices"
	"sort"

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
