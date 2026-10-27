package cluster

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Дописывание плана (stages.md, стадия 6): построенное по плану — препятствие, новые кластеры
// встают рядом в свои зоны, ленты шин — новые ряды или продолжение старых.

// FillLanes — ленты шин buses по раскладке прошлого плана list: предмет, ряд, голова, самый
// ранний подвод (отводы — только ниже по течению).
// LaterNames — имена будущих блоков (Later) среди cs: для FillLanes.
func LaterNames(cs []*Cluster) map[string]bool {
	res := map[string]bool{}
	for _, c := range cs {
		if c.Later {
			res[c.Name] = true
		}
	}
	return res
}

// later — имена будущих блоков (Later): их подводы ленту «сейчас» не кормят.
func FillLanes(buses []*ZBus, list []layout.Placement, later map[string]bool) {
	for _, bus := range buses {
		tag := Tag(bus.Name)
		rows := map[string]map[int]int{} // предмет → ряд → лент
		for _, p := range list {
			if p.Tag == tag && p.Name == "transport-belt" && p.Recipe != "" {
				if rows[p.Recipe] == nil {
					rows[p.Recipe] = map[int]int{}
				}
				rows[p.Recipe][world.TileOf(p.Pos).Y]++
			}
		}
		bus.Lanes = nil
		for it, ys := range rows {
			// ряды лент — где их больше всего (обходы неровностей — в стороне, в зазорах); у
			// добытого лент две — второй ряд не короче половины первого
			n := 0
			for yy, k := range ys {
				if laneRow(yy-bus.Rect.Min.Y, bus.Rect.H()) {
					n = max(n, k)
				}
			}
			var lns []ZoneLane
			for y, k := range ys {
				if laneRow(y-bus.Rect.Min.Y, bus.Rect.H()) && 2*k >= n {
					lns = append(lns, fillLane(bus, list, it, y, later))
				}
			}
			// подвода не нашли (бывает подземкой) — отводы где угодно, но только если у
			// предмета нет ленты с найденным подводом: вторая лента без подвода пустая
			if !slices.ContainsFunc(lns, func(ln ZoneLane) bool { return ln.Fed }) {
				for i := range lns {
					lns[i].feed(lns[i].Start, true)
				}
			}
			bus.Lanes = append(bus.Lanes, lns...)
		}
		sort.Slice(bus.Lanes, func(i, j int) bool { return bus.Lanes[i].Y < bus.Lanes[j].Y })
	}
}

// fillLane — лента it в ряду y шины bus по раскладке list: голова, самый ранний подвод.
func fillLane(bus *ZBus, list []layout.Placement, it string, y int, later map[string]bool) ZoneLane {
	tag := Tag(bus.Name)
	ln := ZoneLane{Item: it, Y: y, Start: bus.Rect.Max.X}
	for _, p := range list {
		if t := world.TileOf(p.Pos); p.Tag == tag && p.Recipe == it && t.Y == y {
			ln.Start = min(ln.Start, t.X)
		}
	}
	// подводы: ленты логистики этой шины, смотрящие в ряд сбоку или в голову
	prefix := LogisticsTag + "/" + bus.Name + "/"
	for _, p := range list {
		if !strings.HasPrefix(p.Tag, prefix) || p.Name != "transport-belt" {
			continue
		}
		t := world.TileOf(p.Pos)
		if t.Y == y-1 && p.Dir == layout.S || t.Y == y+1 && p.Dir == layout.N || t.Y == y && t.X == ln.Start-1 && p.Dir == layout.E {
			ln.feed(t.X, !later[strings.TrimPrefix(p.Tag, prefix)])
		}
	}
	return ln
}

// newLane — новая лента it шины k до end — в свободный ряд; обход неровности не проводится
// (там обходы других лент, утёс у самого ряда) — в следующий свободный.
func (b *Base) newLane(l *layout.Layout, buses []*ZBus, k int, it string, end int) error {
	_, _, err := b.newLaneFrom(l, buses, k, it, end, 0)
	return err
}

// newLaneFrom — newLane в первый свободный ряд не раньше ряда j0 (по порядку сверху вниз):
// Y ленты и номер следующего ряда.
func (b *Base) newLaneFrom(l *layout.Layout, buses []*ZBus, k int, it string, end, j0 int) (int, int, error) {
	bus := buses[k]
	used := map[int]bool{}
	for _, ln := range bus.Lanes {
		used[ln.Y-bus.Rect.Min.Y] = true
	}
	var err error = fmt.Errorf("cluster: %s: нет ряда под ленту %s", bus.Name, it)
	for j := j0; laneV(j) < bus.Rect.H(); j++ {
		v := laneV(j)
		if used[v] {
			continue
		}
		saved := slices.Clone(l.List)
		start := b.laneStart(bus, k, it)
		bus.Lanes = append(bus.Lanes, ZoneLane{Item: it, Y: bus.Rect.Min.Y + v, Start: start})
		var gaps []laneGap
		b.layLane(l, buses, k, len(bus.Lanes)-1, start, end, -1, false, &gaps)
		if err = fixGaps(l, buses, gaps); err == nil {
			return bus.Rect.Min.Y + v, j + 1, nil
		}
		bus.Lanes = bus.Lanes[:len(bus.Lanes)-1]
		l.List = saved
		l.Rollback(-1)
	}
	return 0, 0, err
}

// farthestUser — новый кластер (из fresh), который берёт it с шины k или кладёт в неё, самый
// правый (из-за него лента тянется дальше всего).
func farthestUser(fresh []*Cluster, it string, k int) *Cluster {
	var res *Cluster
	for _, c := range fresh {
		f, use := floorOf(c), false
		for _, in := range c.In {
			use = use || in.Item == it && (f == k+1 || f == k && c.TakesAbove())
		}
		for _, o := range c.Out {
			use = use || o.Item == it && f == k
		}
		if use && (res == nil || c.Area.Max.X > res.Area.Max.X) {
			res = c
		}
	}
	return res
}

// laneStart — откуда новая лента it шины k: продукт этажа под шиной кладётся у столбца своего
// кластера — лента начинается у самого левого из них (у головы шины тесно от старых подводов и
// отводов: обходы ломались, 859088572); добытое и сквозное — от головы.
func (b *Base) laneStart(bus *ZBus, k int, it string) int {
	start := math.MaxInt
	for _, c := range b.Clusters {
		if floorOf(c) == k && k > 0 && slices.ContainsFunc(c.Out, func(f Flow) bool { return f.Item == it }) {
			start = min(start, c.Area.Min.X-laneTail)
		}
	}
	if start == math.MaxInt {
		return bus.Rect.Min.X
	}
	return max(start, bus.Rect.Min.X)
}

// laneLast — X последней ленты ряда ln шины bus.
func laneLast(l *layout.Layout, bus *ZBus, ln *ZoneLane) int {
	last := ln.Start
	for _, p := range l.List {
		if t := world.TileOf(p.Pos); p.Tag == Tag(bus.Name) && p.Recipe == ln.Item && t.Y == ln.Y {
			last = max(last, t.X)
		}
	}
	return last
}

// ConnectMore — логистика новых кластеров fresh в уже связанной базе: недостающие ленты шин —
// новыми рядами, нужные дальше — продолжением; выходы — боком в ленты шины над собой,
// сквозные — отводом с шины ниже, входы — делителем; extra — новые входы старых кластеров.
func (b *Base) ConnectMore(l *layout.Layout, buses []*ZBus, fresh []*Cluster, extra map[*Cluster][]Port) error {
	on := b.zoneSources(len(buses))
	need := b.zoneNeed(on, len(buses))
	for k, bus := range buses {
		for _, it := range sortedItems(need[k], on, k) {
			end := b.laneEnd(buses, k, it)
			n := 0
			for j := range bus.Lanes { // старые ленты — продолжить, если нужны дальше
				ln := &bus.Lanes[j]
				if ln.Item != it {
					continue
				}
				n++
				if last := laneLast(l, bus, ln); end > last+1 {
					var gaps []laneGap
					b.layLane(l, buses, k, j, last+1, end, last, true, &gaps)
					if err := fixGaps(l, buses, gaps); err != nil {
						// ленту не продлить до нового потребителя — его в другое место (ближе)
						if c := farthestUser(fresh, it, k); c != nil {
							return &ConnectError{c, err}
						}
						return err
					}
				}
			}
			for ; n < b.laneCount(k, it); n++ {
				if err := b.newLane(l, buses, k, it, end); err != nil {
					return err
				}
			}
		}
	}
	g := guardPorts(l, fresh)
	for _, ps := range extra {
		for _, p := range ps {
			g.close(p)
		}
	}
	for _, c := range fresh {
		if c.IsOil() {
			continue // нефть — трубой (трасса), не лентой
		}
		if c.Kind == Mine {
			if err := b.moreMine(l, buses, c); err != nil {
				return &ConnectError{c, err}
			}
			continue
		}
		if err := b.feedOut(l, buses, c, g); err != nil {
			return err
		}
	}
	if err := feedThrough(l, buses); err != nil {
		// сквозную не провести — новый потребитель её предмета (этажом выше) — в другое место
		if te, ok := err.(*ThroughError); ok {
			if c := farthestUser(fresh, te.Item, te.Bus); c != nil {
				return &ConnectError{c, err}
			}
		}
		return err
	}
	for _, c := range fresh {
		f := floorOf(c)
		if f == 0 {
			continue
		}
		// сначала входы с шины снизу: ленты сверху (по коридору вниз) огибали голову и закрывали
		// подвод снизу (быстрые инсертеры, стадия 7)
		below := func(p Port) bool { return f-1 < len(buses) && len(buses[f-1].lanes(p.Item)) > 0 }
		for _, first := range []bool{true, false} {
			for _, p := range c.Ports {
				if _, fluid := b.P.Fluids[p.Item]; fluid {
					continue // жидкость — трубой с трубы шины (Pipes), не лентой
				}
				if p.In && below(p) == first {
					if err := feedIn(l, buses, c, p, g); err != nil {
						return err
					}
				}
			}
		}
	}
	for c, ps := range extra {
		for _, p := range ps {
			if err := feedIn(l, buses, c, p, g); err != nil {
				return err
			}
		}
	}
	return nil
}

// moreMine — ещё одна добыча c того же ресурса (cluster.md §2а «Добыча», §5): свои ленты в bus-1
// (по выходу блока), подвод каждого выхода — через делитель: одна половина — в голову своей
// новой ленты, другая — боком в старый подвод (руда нового месторождения питает и старые
// цепочки); новые ленты делят друг с другом. Делитель не встал — весь выход в свою голову.
func (b *Base) moreMine(l *layout.Layout, buses []*ZBus, c *Cluster) error {
	bus := buses[0]
	old := bus.lanes(c.Product)
	if len(old) == 0 {
		return nil
	}
	if !slices.ContainsFunc(old, func(ln *ZoneLane) bool { return ln.Fed }) {
		return b.feedMine(l, buses, c, nil) // первая добыча ресурса (камень) — в головы его лент
	}
	end := b.laneEnd(buses, 0, c.Product)
	n0 := len(bus.Lanes)
	for range outPorts(c) {
		if err := b.newLane(l, buses, 0, c.Product, end); err != nil {
			return err
		}
	}
	var fresh []*ZoneLane
	for i := n0; i < len(bus.Lanes); i++ {
		fresh = append(fresh, &bus.Lanes[i])
	}
	old = bus.lanes(c.Product)[:len(old)]
	defer unfence(l, b.fence(l, bus, c))
	defer unsolid(l, c)()
	tag := LogisticsTag + "/" + bus.Name + "/" + c.Name
	for i, op := range outPorts(c) {
		// куда: голова своей новой ленты; половина — боком в старый подвод
		route := func(from world.Tile, d int, ports []Port) (Port, error) {
			for _, q := range ports {
				g := guardHeads(l, bus, q.T)
				_, err := l.RouteBelt(from, d, q.T, q.Dir, tag)
				unfence(l, g)
				if err == nil {
					return q, nil
				}
			}
			return Port{}, layout.ErrNoRoute
		}
		heads := feedPorts(l, bus, fresh, op.T)
		heads = slices.DeleteFunc(heads, func(p Port) bool {
			return !slices.ContainsFunc(fresh, func(ln *ZoneLane) bool { return ln.head().T == p.T })
		})
		mark := l.Mark()
		if sp, a, s, ok := splitterAt(l, op, tag); ok {
			l.Add(sp)
			if q, err := route(a, op.Dir, heads); err == nil {
				if _, err := route(s, op.Dir, feederSides(l, bus, old, s)); err == nil {
					fedHead(fresh, q, !c.Later)
					continue
				}
			}
			l.Rollback(mark)
		}
		if q, err := route(op.T, op.Dir, heads); err == nil {
			fedHead(fresh, q, !c.Later)
			continue
		}
		// до головы не дойти (у головы bus-1 тесно от старых подводов) — весь выход боком в
		// старый подвод, как раньше
		if _, err := route(op.T, op.Dir, feederSides(l, bus, old, op.T)); err != nil {
			return fmt.Errorf("добыча %s → %s (%s, выход %d): %w", c.Name, bus.Name, c.Product, i+1, err)
		}
	}
	// новые ленты без подвода — убрать (пустые ленты на шине не нужны)
	empty := map[int]bool{}
	nb := &ZBus{Name: bus.Name}
	for _, ln := range fresh {
		if ln.Fed {
			nb.Lanes = append(nb.Lanes, *ln)
		} else {
			empty[ln.Y] = true
		}
	}
	if len(empty) > 0 {
		l.List = slices.DeleteFunc(l.List, func(p layout.Placement) bool {
			return p.Tag == Tag(bus.Name) && p.Recipe == c.Product && empty[world.TileOf(p.Pos).Y]
		})
		l.Rollback(-1)
		bus.Lanes = slices.DeleteFunc(bus.Lanes, func(ln ZoneLane) bool { return ln.Item == c.Product && empty[ln.Y] })
	}
	balanceLanes(l, nb)
	return nil
}

// splitterAt — делитель сразу за выходом op (поперёк хода, op.T — один из входов): выходы a (за
// op.T) и s (за соседним тайлом). false — не встаёт ни с одной стороны.
func splitterAt(l *layout.Layout, op Port, tag string) (layout.Placement, world.Tile, world.Tile, bool) {
	for _, side := range []int{(op.Dir + 4) % 16, (op.Dir + 12) % 16} {
		n := layout.Step(op.T, side)
		cn, co := n.Center(), op.T.Center()
		sp := layout.Placement{Name: "splitter", Pos: bridge.Pos{X: (cn.X + co.X) / 2, Y: (cn.Y + co.Y) / 2}, Dir: op.Dir, Tag: tag}
		a, s := layout.Step(op.T, op.Dir), layout.Step(n, op.Dir)
		if l.CanPlace(sp, layout.AnyLand) && l.Free(a, layout.AnyLand) && l.Free(s, layout.AnyLand) {
			return sp, a, s, true
		}
	}
	return layout.Placement{}, world.Tile{}, world.Tile{}, false
}

// AddLaneB — лента B уже построенного блока лабораторий (cluster.md §2а «Лаборатории»): третий
// пакет (синий) — лента вдоль левого столбца лабораторий (ряд labA−1), к каждой лаборатории
// левого столбца — длинный инсертер через ленту A; подача — с торца сбоку головы (v = labA−2).
// Места — оставленные шаблоном; построенное не трогает.
func AddLaneB(l *layout.Layout, c *Cluster, item string, rate float64, tag string) (Port, error) {
	if c.Kind != Lab || len(c.In) != 2 {
		return Port{}, fmt.Errorf("cluster %s: лента B — у блока лабораторий с двумя пакетами", c.Name)
	}
	f := c.frame()
	for _, t := range labReserve(f, c) {
		delete(l.Reserved, t)
	}
	defer func() {
		for _, t := range labReserve(f, c) {
			if l.At(t) == nil {
				l.Reserved[t] = true
			}
		}
	}()
	bv := labB
	add := func(p layout.Placement) error {
		for _, t := range layout.Tiles(l.P, p) { // площадь кластера закрыта целиком (Layout) — своё место открыть
			if t.X >= c.Area.Min.X && t.X < c.Area.Max.X && t.Y >= c.Area.Min.Y && t.Y < c.Area.Max.Y && l.At(t) == nil {
				delete(l.Reserved, t)
			}
		}
		if !l.CanPlace(p, layout.AnyLand) {
			t := world.TileOf(p.Pos)
			why := "не суша"
			if q := l.At(t); q != nil {
				why = q.String() + " " + q.Tag
			} else if l.Reserved[t] {
				why = "закрыто"
			}
			return fmt.Errorf("cluster %s: лента B не встаёт (%s: %s)", c.Name, p, why)
		}
		l.Add(p)
		return nil
	}
	// подача — с торца в ряду над B и поворотом боком в голову B (u=1: ряд B у торца занят
	// подачей красного); лента — через голову (буфер синего) и вдоль левого столбца лабораторий
	if err := add(f.at(l, "transport-belt", 0, bv-1, layout.E, tag)); err != nil {
		return Port{}, err
	}
	if err := add(f.at(l, "transport-belt", 1, bv-1, layout.S, tag)); err != nil {
		return Port{}, err
	}
	rows := (c.Count + labCols - 1) / labCols
	end := c.Head + 4*(rows-1) + 3
	for u := 1; u < end; u++ {
		if err := add(f.at(l, "transport-belt", u, bv, layout.E, tag)); err != nil {
			return Port{}, err
		}
	}
	l.AllowEnd(f.tile(end, bv))
	if n := labBufferChests(c); true { // буфер синего — над B, в голове
		if err := bufferAt(l, f, c, bv, bv-1, bv-2, n, item, tag, 3); err != nil {
			return Port{}, err
		}
	}
	for k := 0; k < c.Count; k++ {
		if k%labCols != 0 {
			continue // только левый столбец — дальше от лаборатории к лаборатории
		}
		u := c.Head + 4*(k/labCols)
		if err := add(f.inserter(l, "long-handed-inserter", u, labA+1, bv, tag)); err != nil {
			return Port{}, err
		}
	}
	port := Port{T: f.tile(-1, bv-1), Dir: f.dir(layout.E), Item: item, In: true}
	c.Ports = append(c.Ports, port)
	addFlow(&c.In, item, rate)
	return port, nil
}

// labBufferChests — сундуков в буфере одного пакета блока лабораторий (в голове — по два буфера
// друг за другом с каждой стороны: Head = 4n + 2).
func labBufferChests(c *Cluster) int { return (c.Head - 2) / 4 }

// AddInput — второй пакет ленты A уже построенного блока лабораторий (cluster.md §2а «Лаборатории»): подача
// с другой стороны ленты — боком в её голову (на свою полосу), буфер — напротив подачи, на
// оставленных шаблоном местах. Построенное не трогает; поток item rate — во входы кластера.
func AddInput(l *layout.Layout, c *Cluster, item string, rate float64, tag string) (Port, error) {
	if c.Kind != Lab || len(c.In) != 1 {
		return Port{}, fmt.Errorf("cluster %s: второй вход — только у блока лабораторий с одним пакетом", c.Name)
	}
	f := c.frame()
	for _, t := range labReserve(f, c) {
		delete(l.Reserved, t)
	}
	// подача — с торца снизу в ряду A+1 и поворотом боком в голову ленты A (cluster.md §6б)
	p := f.at(l, "transport-belt", 0, labA+1, layout.N, tag)
	if t := world.TileOf(p.Pos); l.At(t) != nil {
		return Port{}, fmt.Errorf("cluster %s: второй вход не встаёт (%s)", c.Name, p)
	} else if ok, _ := l.W.Buildable(t); !ok {
		return Port{}, fmt.Errorf("cluster %s: второй вход не встаёт (%s)", c.Name, p)
	}
	l.Add(p)
	if n := labBufferChests(c); true { // буфер второго пакета A — под A, за буфером первого
		if err := bufferAt(l, f, c, labA, labA+1, labA+2, n, item, tag, 2*n+1); err != nil {
			return Port{}, err
		}
	}
	// столбы к новым инсертерам — внутри площади, на свободных тайлах (не на месте под будущее)
	keep := map[world.Tile]bool{}
	for _, t := range labReserve(f, c) {
		keep[t] = true
	}
	var opened []world.Tile
	for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
		for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
			t := world.Tile{X: x, Y: y}
			if l.Reserved[t] && !keep[t] && l.At(t) == nil {
				delete(l.Reserved, t)
				opened = append(opened, t)
			}
		}
	}
	l.PoleTag = tag
	l.Poles("small-electric-pole", tag)
	l.PoleTag = ""
	for _, t := range opened {
		if l.At(t) == nil {
			l.Reserved[t] = true
		}
	}
	port := Port{T: f.tile(-1, labA+1), Dir: f.dir(layout.E), Item: item, In: true}
	c.Ports = append(c.Ports, port)
	addFlow(&c.In, item, rate)
	return port, nil
}
