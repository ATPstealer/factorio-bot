package cluster

import (
	"fmt"
	"sort"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Главная шина (решение пользователя: спагетти «лента от каждого поставщика к каждому
// потребителю» не работает): прямая полоса лент по предметам, все текут в одну сторону (+u
// в системе шины). Клиенты (сборка, лаборатории, снабжение) — по одну сторону шины поперёк
// неё, началом ряда к шине; вход — делителем с ленты шины и подземками под соседние ленты,
// выход промежуточного продукта — в голову своей ленты ниже по ходу. Плавка, добыча, станция
// — вне шины: пластины приходят в голову шины.
//
// Поперёк (v): ряд лент — пары лент через зазоры в 3 тайла (в зазоре — половина делителя,
// поворот и тайл между подземками: спуск к клиентам прямой — подземка под парой, выход,
// лента, снова подземка; v 0–2 зазор, 3–4 ленты, 5–7 зазор, …), потом полоса BusStrip (подвод к
// клиентам, столбы), потом клиенты на глубину Depth. Вдоль (u): голова BusHead (сюда
// подходят ленты от плавки), потом клиенты через BusGap (порты и подъём выхода).

const (
	Bus      Kind = "bus"
	BusCap        = 10 // лент в шине (с запасом на следующие этапы)
	BusStrip      = 4  // полоса между лентами и клиентами
	BusGap        = 5  // между клиентами: порты, подходы к ним и поворот выхода к шине
	BusHead       = 4  // голова: подход лент от плавки
	busDepth      = 40 // глубина клиентов (ряд с запасом длиннее — запас урезается)
)

// Lane — лента шины: предмет, ряд поперёк (V), занятый отрезок вдоль [Start, End); Fed —
// подведён поставщик в голову.
type Lane struct {
	Item  string `json:"item"`
	V     int    `json:"v"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Fed   bool   `json:"fed,omitempty"`
}

// BusInfo — шина: ленты, где следующий клиент (Next), ширина ряда лент, глубина клиентов.
type BusInfo struct {
	Lanes []Lane `json:"lanes"`
	Next  int    `json:"next"`
	Width int    `json:"width"`
	Depth int    `json:"depth"`
}

// laneV — ряд j-й ленты: пары через зазоры в 3.
func laneV(j int) int { return 3 + (j/2)*5 + j%2 }

// busWidth — ширина ряда лент на n лент (с зазорами по краям).
func busWidth(n int) int { return 5*((n+1)/2) + 3 }

// laneRow — ряд v — лента (или место будущей), не зазор.
func laneRow(v, width int) bool { return v >= 3 && v < width && (v-3)%5 < 2 }

// OnBus — кластер берёт входы с шины.
func OnBus(c *Cluster) bool { return c.Kind == Make || c.Kind == Lab || c.Kind == Supply }

// Bus — шина базы (nil — нет).
func (b *Base) Bus() *Cluster {
	for _, c := range b.Clusters {
		if c.Kind == Bus {
			return c
		}
	}
	return nil
}

// Clone — копия шины (разметка этапа может не выйти — вернуть как было).
func (c *Cluster) Clone() *Cluster {
	x := *c
	if c.Bus != nil {
		bi := *c.Bus
		bi.Lanes = append([]Lane(nil), c.Bus.Lanes...)
		x.Bus = &bi
	}
	return &x
}

func (c *Cluster) frame() frame { return frame{r: c.Area, vertical: c.Vertical} }

// lane — лента предмета (nil — нет).
func (bi *BusInfo) lane(item string) *Lane {
	for i := range bi.Lanes {
		if bi.Lanes[i].Item == item {
			return &bi.Lanes[i]
		}
	}
	return nil
}

// newBus — шина под клиентов: длина — с тройным запасом на следующие этапы.
func newBus(clients []*Cluster) *Cluster {
	length, depth := 0, busDepth // глубина — сразу под клиентов следующих этапов
	for _, c := range clients {
		length += c.Cross + BusGap
		depth = max(depth, c.Head+c.Count*c.Pitch)
	}
	need := length + 1
	length = max(length*6/5, 150) // с запасом на следующие этапы (клиентов к этапу 3 — ~10 × 14)
	w := busWidth(BusCap)
	c := &Cluster{Kind: Bus, Product: "bus", Reserve: 1, Pitch: 1, Head: BusHead, Room: length, Count: need,
		Cross: w + BusStrip + depth, Bus: &BusInfo{Next: BusHead + 1, Width: w, Depth: depth}}
	return c
}

// busOrder — клиенты по зависимостям: поставщик промежуточного — выше по шине, чем его
// потребители; лаборатории и снабжение — в конце.
func busOrder(cs []*Cluster) []*Cluster {
	rank := map[Kind]int{Make: 0, Supply: 1, Lab: 2}
	cs = append([]*Cluster(nil), cs...)
	sort.SliceStable(cs, func(i, j int) bool { return rank[cs[i].Kind] < rank[cs[j].Kind] })
	var res []*Cluster
	seen := map[*Cluster]bool{}
	var visit func(c *Cluster)
	visit = func(c *Cluster) {
		if seen[c] {
			return
		}
		seen[c] = true
		for _, in := range c.In {
			for _, o := range cs {
				if o != c && hasOut(o, in.Item) {
					visit(o)
				}
			}
		}
		res = append(res, c)
	}
	for _, c := range cs {
		visit(c)
	}
	return res
}

func hasOut(c *Cluster, item string) bool {
	for _, f := range c.Out {
		if f.Item == item {
			return true
		}
	}
	return false
}

// placeOnBus — места клиентам вдоль шины (нет шины — сначала она: у плавки); ленты под их
// входы. Клиент поперёк шины, началом ряда к ней; запас роста — не глубже шины.
func (b *Base) placeOnBus(clients []*Cluster) error {
	bus := b.Bus()
	if bus == nil {
		bus = newBus(clients)
		bus.Name = b.name("bus")
		for _, c := range clients { // якорь — у поставщиков входов (плавка)
			for _, f := range c.In {
				addFlow(&bus.In, f.Item, f.Rate)
			}
		}
		err := b.Place(bus)
		bus.In = nil // входы — у клиентов (иначе посчитаются дважды)
		if err != nil {
			return err
		}
	}
	bi := bus.Bus
	f := bus.frame()
	for _, c := range busOrder(clients) {
		if need := max(c.Head+c.Count*c.Pitch, min(c.Len(), busDepth)); need > bi.Depth {
			if err := b.deepenBus(bus, need); err != nil && c.Head+c.Count*c.Pitch > bi.Depth {
				return fmt.Errorf("cluster: %s длиннее шины (%d > %d): %w", c.Name, c.Head+c.Count*c.Pitch, bi.Depth, err)
			}
		}
		c.Room = max(c.Count, min(c.Room, (bi.Depth-c.Head)/c.Pitch))
		u0 := bi.Next
		if u0+c.Cross > bus.Head+bus.Room {
			if err := b.extendBus(bus, u0+c.Cross+BusGap-bus.Head-bus.Room); err != nil {
				// шина кончилась — клиент рядом, вход — делителем с ленты шины
				if b.Log != nil {
					b.Log("шина %s кончилась (%v): %s — рядом", bus.Name, err, c.Name)
				}
				if err := b.Place(c); err != nil {
					return err
				}
				if err := b.addLanes(bus, c); err != nil {
					return err
				}
				continue
			}
		}
		v0 := bi.Width + BusStrip
		a, z := f.tile(u0, v0), f.tile(u0+c.Cross-1, v0+c.Len()-1)
		c.Area = Rect{world.Tile{X: min(a.X, z.X), Y: min(a.Y, z.Y)}, world.Tile{X: max(a.X, z.X) + 1, Y: max(a.Y, z.Y) + 1}}
		c.Vertical = !bus.Vertical
		c.Rect = c.Area
		l := c.Head + c.Count*c.Pitch
		if c.Vertical {
			c.Rect.Max.Y = c.Rect.Min.Y + l
		} else {
			c.Rect.Max.X = c.Rect.Min.X + l
		}
		bi.Next = u0 + c.Cross + BusGap
		if err := b.addLanes(bus, c); err != nil {
			return err
		}
		b.Clusters = append(b.Clusters, c)
	}
	bus.Count = bi.Next
	bus.Rect = bus.Area
	return nil
}

// busU — u начала клиента c вдоль шины.
func (b *Base) busU(bus, c *Cluster) int {
	f := bus.frame()
	for u := 0; u < bus.Head+bus.Room; u++ {
		t := f.tile(u, bus.Bus.Width+BusStrip)
		if t == c.Area.Min {
			return u
		}
	}
	return 0
}

// busFree — тайл шины, свободный для лент и подводов: голова, зазоры и полоса — везде, где
// клиенты уже стоят (ряды лент — нет); дальше (u ≥ Next) — запас будущих клиентов и лент; клиенты — только
// столбцы между ними у начала ряда (порты, подъём выхода).
func busFree(bus *Cluster, u, v int, clientAt func(u int) bool) bool {
	bi := bus.Bus
	if u >= bi.Next {
		return false
	}
	if laneRow(v, bi.Width) && u >= BusHead {
		return false // ряды лент (и будущих): пересекать — только подземкой
	}
	if v < bi.Width+BusStrip {
		return true
	}
	return !clientAt(u) && v < bi.Width+BusStrip+2
}

// reserveBus — закрыть тайлы шины, кроме свободных (busFree), в l.
func (b *Base) reserveBus(l *layout.Layout, bus *Cluster) {
	f := bus.frame()
	in := map[int]bool{}
	for _, c := range b.Clusters {
		if b.onBusArea(bus, c) {
			u0 := b.busU(bus, c)
			for u := u0; u < u0+c.Cross; u++ {
				in[u] = true
			}
		}
	}
	for u := 0; u < bus.Head+bus.Room; u++ {
		for v := 0; v < bus.Cross; v++ {
			if t := f.tile(u, v); !busFree(bus, u, v, func(u int) bool { return in[u] }) && l.At(t) == nil {
				l.Reserved[t] = true
			}
		}
	}
}

// laneBelts — ленты шины предмета: стоящие и новые; лента дотягивается до end.
func (b *Base) laneBelts(l *layout.Layout, bus *Cluster, ln *Lane, end int) []layout.Placement {
	f := bus.frame()
	d := f.dir(layout.E)
	for u := ln.End; u < end; u++ {
		l.Add(layout.Placement{Name: "transport-belt", Pos: f.tile(u, ln.V).Center(), Dir: d, Tag: Tag(bus.Name)})
	}
	ln.End = max(ln.End, end)
	var res []layout.Placement
	for u := ln.Start; u < ln.End; u++ {
		res = append(res, layout.Placement{Name: "transport-belt", Pos: f.tile(u, ln.V).Center(), Dir: d, Tag: Tag(bus.Name)})
	}
	return res
}

// headPort — куда подвести поставщика ленты: тайл перед её началом, смотрит по шине.
func headPort(bus *Cluster, ln *Lane) Port {
	f := bus.frame()
	return Port{T: f.tile(ln.Start-1, ln.V), Dir: f.dir(layout.E), Item: ln.Item, In: true}
}

// sidePorts — подвод боком в ленту из зазора рядом, от from вниз по шине: второй поставщик
// у головы, клиент шины — у своего подъёма выхода (выше по шине его ленты пусты).
func sidePorts(bus *Cluster, ln *Lane, from int) []Port {
	f := bus.frame()
	var res []Port
	for u := max(from, ln.Start); u < from+8 && u < ln.End; u++ {
		for _, s := range []struct{ dv, d int }{{-1, layout.S}, {1, layout.N}} {
			v := ln.V + s.dv
			if !laneRow(v, bus.Bus.Width) { // соседний ряд — зазор (не лента пары)
				res = append(res, Port{T: f.tile(u, v), Dir: f.dir(s.d), Item: ln.Item, In: true})
			}
		}
	}
	return res
}

// extendBus — удлинить шину на n (+ запас): продолжение свободно — суша без месторождений,
// не на коридорах других кластеров.
func (b *Base) extendBus(bus *Cluster, n int) error {
	n += 20
	f := bus.frame()
	L := bus.Head + bus.Room
	a, z := f.tile(L, 0), f.tile(L+n-1, bus.Cross-1)
	ext := Rect{world.Tile{X: min(a.X, z.X), Y: min(a.Y, z.Y)}, world.Tile{X: max(a.X, z.X) + 1, Y: max(a.Y, z.Y) + 1}}
	g := b.grid()
	if k, out := g.count(g.bad, ext); k > 0 || out > 0 {
		return fmt.Errorf("продолжение занято")
	}
	for _, o := range b.Clusters {
		if o != bus && o.Area.Grow(Corridor).Overlaps(ext.Grow(Corridor)) && !b.onBusArea(bus, o) {
			return fmt.Errorf("продолжение упирается в %s", o.Name)
		}
	}
	bus.Room += n
	z = f.tile(bus.Head+bus.Room-1, bus.Cross-1)
	a = f.tile(0, 0)
	bus.Area = Rect{world.Tile{X: min(a.X, z.X), Y: min(a.Y, z.Y)}, world.Tile{X: max(a.X, z.X) + 1, Y: max(a.Y, z.Y) + 1}}
	g.mark(bus.Area.Grow(Corridor))
	return nil
}

// onBusArea — o стоит на шине (клиент).
func (b *Base) onBusArea(bus, o *Cluster) bool {
	return OnBus(o) && bus.Area.Overlaps(o.Area)
}

// window — подводы только в столбцах [lo, hi) шины (у своего клиента — не гулять вдоль
// зазоров, забирая место у чужих отводов): свободные тайлы шины вне окна закрыть; вернуть —
// открыть их назад.
func (b *Base) window(l *layout.Layout, bus *Cluster, lo, hi int) func() {
	f := bus.frame()
	var shut []world.Tile
	for u := 0; u < bus.Head+bus.Room; u++ {
		if u >= lo && u < hi {
			continue
		}
		for v := 0; v < bus.Bus.Width+BusStrip+2; v++ {
			if t := f.tile(u, v); l.At(t) == nil && !l.Reserved[t] {
				l.Reserved[t] = true
				shut = append(shut, t)
			}
		}
	}
	return func() {
		for _, t := range shut {
			delete(l.Reserved, t)
		}
	}
}

// inWindow — ленты line в столбцах [lo, hi).
func inWindow(bus *Cluster, line []layout.Placement, lo, hi int) []layout.Placement {
	f := bus.frame()
	var res []layout.Placement
	for _, p := range line {
		t := world.TileOf(p.Pos)
		for u := lo; u < hi; u++ {
			if u >= 0 && f.tile(u, 0) == f.tile(u, 0) && (bus.Vertical && t.Y == f.tile(u, 0).Y || !bus.Vertical && t.X == f.tile(u, 0).X) {
				res = append(res, p)
				break
			}
		}
	}
	return res
}

// deepenBus — глубже под клиентов (до depth): полоса за клиентами свободна.
func (b *Base) deepenBus(bus *Cluster, depth int) error {
	bi := bus.Bus
	f := bus.frame()
	add := depth - bi.Depth
	L := bus.Head + bus.Room
	a, z := f.tile(0, bus.Cross), f.tile(L-1, bus.Cross+add-1)
	ext := Rect{world.Tile{X: min(a.X, z.X), Y: min(a.Y, z.Y)}, world.Tile{X: max(a.X, z.X) + 1, Y: max(a.Y, z.Y) + 1}}
	g := b.grid()
	if k, out := g.count(g.bad, ext); k > 0 || out > 0 {
		return fmt.Errorf("за шиной занято")
	}
	for _, o := range b.Clusters {
		if o != bus && !b.onBusArea(bus, o) && o.Area.Grow(Corridor).Overlaps(ext.Grow(Corridor)) {
			return fmt.Errorf("за шиной %s", o.Name)
		}
	}
	bi.Depth, bus.Cross = depth, bus.Cross+add
	z = f.tile(L-1, bus.Cross-1)
	a = f.tile(0, 0)
	bus.Area = Rect{world.Tile{X: min(a.X, z.X), Y: min(a.Y, z.Y)}, world.Tile{X: max(a.X, z.X) + 1, Y: max(a.Y, z.Y) + 1}}
	g.mark(bus.Area.Grow(Corridor))
	return nil
}

// addLanes — ленты шины под входы клиента c.
func (b *Base) addLanes(bus, c *Cluster) error {
	bi := bus.Bus
	for _, in := range c.In {
		if bi.lane(in.Item) != nil {
			continue
		}
		if len(bi.Lanes) >= BusCap {
			return fmt.Errorf("cluster: в шине %s нет места под ленту %s", bus.Name, in.Item)
		}
		bi.Lanes = append(bi.Lanes, Lane{Item: in.Item, V: laneV(len(bi.Lanes)), Start: BusHead, End: BusHead})
	}
	return nil
}
