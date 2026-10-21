package cluster

import (
	"fmt"
	"maps"
	"math"
	"sort"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Главная шина (решение пользователя: спагетти «лента от каждого поставщика к каждому
// потребителю» не работает): отдельный кластер — прямая полоса лент по предметам, все текут в
// одну сторону (+u в системе шины), от базовых ресурсов к сложным продуктам. Ширина — по
// потокам: лент предмета хватает на его расход с запасом роста, и место под ленты следующих
// этапов (BusCap). Клиенты (сборка, лаборатории, снабжение) — обычными кластерами по одну
// сторону шины (не уместились — по другую), поперёк неё, началом ряда к шине, каждый со своим
// коридором; по ходу шины — в порядке зависимостей. Вход клиента — делителем с ленты шины и
// лентой по коридорам (под соседними лентами — подземкой), выход промежуточного продукта — в
// свою ленту ниже по ходу. Плавка, добыча, станция — вне шины: пластины приходят в голову.
//
// Поперёк (v): ряд лент — пары лент через зазоры в 3 тайла (в зазоре — половина делителя,
// поворот и тайл между подземками; v 0–2 зазор, 3–4 ленты, 5–7 зазор, …). Вдоль (u): голова
// BusHead (сюда подходят ленты от плавки), дальше — клиенты.

const (
	Bus        Kind = "bus"
	BusCap          = 40 // лент в шине не меньше (запас ширины на следующие этапы; пары через зазоры 3)
	BusReserve      = 5  // запас длины шины: ×5 к длине под нынешних клиентов (итеративная стройка)
	BusGap          = 5  // подводы у клиента: столбцы до и после него
	BusHead         = 4  // голова: подход лент от плавки
)

// BeltRate — пропускная способность ленты шины, шт/с (жёлтая 15; прикидка базы — красная 30).
var BeltRate = 15.0

// Lane — лента шины: предмет, ряд поперёк (V), занятый отрезок вдоль [Start, End); Fed —
// подведён поставщик в голову.
type Lane struct {
	Item  string `json:"item"`
	V     int    `json:"v"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Fed   bool   `json:"fed,omitempty"`
}

// BusInfo — шина: ленты, где следующий клиент (Next), ширина ряда лент, клиенты (имя → u
// начала), расход по предметам с запасом (под него — число лент).
type BusInfo struct {
	Lanes   []Lane             `json:"lanes"`
	Next    int                `json:"next"`
	Width   int                `json:"width"`
	Clients map[string]int     `json:"clients,omitempty"`
	Side    int                `json:"side,omitempty"` // глубина клиентов: полоса сбоку свободна
	Demand  map[string]float64 `json:"demand,omitempty"`
}

// laneV — ряд j-й ленты: пары через зазоры в 3.
func laneV(j int) int { return 3 + (j/2)*5 + j%2 }

// busWidth — ширина ряда лент на n лент (с зазорами по краям).
func busWidth(n int) int { return 5*((n+1)/2) + 3 }

// laneRow — ряд v — лента (или место будущей), не зазор.
func laneRow(v, width int) bool { return v >= 3 && v < width && (v-3)%5 < 2 }

// OnBus — клиент шины (стоит у неё): плавка, сборка, лаборатории, снабжение. Станция — у
// воды, уголь берёт отводом с ленты шины; добыча — поставщик в головы лент.
func OnBus(c *Cluster) bool {
	return c.Kind == Smelt || c.Kind == Make || c.Kind == Lab || c.Kind == Supply
}

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
		bi.Clients = maps.Clone(c.Bus.Clients)
		bi.Demand = maps.Clone(c.Bus.Demand)
		x.Bus = &bi
	}
	return &x
}

func (c *Cluster) frame() frame {
	return frame{r: c.Area, vertical: c.Vertical, up: c.Upright && c.Vertical}
}

// lane — лента предмета (nil — нет).
func (bi *BusInfo) lane(item string) *Lane {
	for i := range bi.Lanes {
		if bi.Lanes[i].Item == item {
			return &bi.Lanes[i]
		}
	}
	return nil
}

// newBus — шина под клиентов: длина — клиенты с коридорами, запас ×BusReserve; ширина — ленты
// под расход клиентов, не меньше BusCap.
func (b *Base) newBus(clients []*Cluster) *Cluster {
	length, side := 0, 0
	demand := map[string]float64{}
	for _, c := range clients {
		length += c.Cross + 2*Corridor
		side = max(side, c.Len())
		for _, in := range c.In {
			if _, fluid := b.P.Fluids[in.Item]; !fluid { // жидкости — трубами
				demand[in.Item] += in.Rate * max(1, c.Reserve)
			}
		}
	}
	lanes := 0
	for _, d := range demand {
		lanes += max(1, int(math.Ceil(d/BeltRate)))
	}
	need := BusHead + length + 1
	// запас роста ×5 по длине (cluster.md), не урезается; прикидка всей базы — +10% на ошибку
	reserve := float64(BusReserve)
	if b.Rough {
		reserve = 1.1
	}
	room := int(math.Ceil(float64(need) * reserve))
	w := busWidth(max(BusCap, lanes))
	return &Cluster{Kind: Bus, Product: "bus", Reserve: reserve, Pitch: 1, Head: BusHead, Room: room, Count: need, Cross: w,
		Bus: &BusInfo{Next: BusHead + 1, Width: w, Side: side, Clients: map[string]int{}, Demand: map[string]float64{}}}
}

// busOrder — клиенты по зависимостям: поставщик промежуточного — выше по шине, чем его
// потребители; лаборатории и снабжение — в конце.
func busOrder(cs []*Cluster) []*Cluster {
	rank := map[Kind]int{Smelt: 0, Make: 1, Supply: 2, Lab: 3}
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

// placeOnBus — места клиентам вдоль шины (нет шины — сначала она: у плавки) и ленты под их
// входы. Клиент поперёк шины, началом ряда к ней, через коридоры (свой и шины); следующий —
// дальше по ходу.
func (b *Base) placeOnBus(clients []*Cluster) error {
	bus := b.Bus()
	if bus == nil {
		bus = b.newBus(clients)
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
	b.grid()
	bi := bus.Bus
	if bi.Clients == nil {
		bi.Clients = map[string]int{}
	}
	f := bus.frame()
	for _, c := range busOrder(clients) {
		c.Vertical = !bus.Vertical
		placed := false
		// сначала по одну сторону на всю длину, потом — по другую
		for _, side := range []int{1, -1} {
			for u0 := bi.Next; !placed && u0 < bi.Next+400; u0++ {
				if u0+c.Cross+Corridor > bus.Head+bus.Room {
					if err := b.extendBus(bus, u0+c.Cross+Corridor-bus.Head-bus.Room); err != nil {
						break
					}
				}
				v0, v1 := bi.Width+2*Corridor, bi.Width+2*Corridor+c.Len()-1
				if side < 0 {
					v0, v1 = -2*Corridor-c.Len(), -2*Corridor-1
				}
				a, z := f.tile(u0, v0), f.tile(u0+c.Cross-1, v1)
				r := Rect{world.Tile{X: min(a.X, z.X), Y: min(a.Y, z.Y)}, world.Tile{X: max(a.X, z.X) + 1, Y: max(a.Y, z.Y) + 1}}
				if !b.fits(c, r, c.Vertical) {
					continue
				}
				c.Area, c.Rect = r, r
				l := c.Head + c.Count*c.Pitch
				if c.Vertical {
					c.Rect.Max.Y = c.Rect.Min.Y + l
				} else {
					c.Rect.Max.X = c.Rect.Min.X + l
				}
				b.g.mark(c.Area.Grow(Corridor))
				bi.Clients[c.Name] = u0
				bi.Next = u0 + c.Cross + 2*Corridor
				placed = true
			}
			if placed {
				break
			}
		}
		if !placed { // у шины места нет — разведать (мир бесконечный) и заново
			return &NoSiteError{c, fmt.Errorf("cluster: нет места у шины %s под %s", bus.Name, c.Name)}
		}
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
func (b *Base) busU(bus, c *Cluster) int { return bus.Bus.Clients[c.Name] }

// busFree — тайл шины, свободный для подводов: голова и зазоры между парами лент — до
// последнего клиента (дальше — запас будущих клиентов и лент); ряды лент (и будущих) —
// пересекать только подземкой.
func busFree(bus *Cluster, u, v int) bool {
	bi := bus.Bus
	if u >= bi.Next {
		return false
	}
	return !laneRow(v, bi.Width) || u < BusHead
}

// reserveBus — закрыть тайлы шины, кроме свободных (busFree), в l.
func (b *Base) reserveBus(l *layout.Layout, bus *Cluster) {
	f := bus.frame()
	for u := 0; u < bus.Head+bus.Room; u++ {
		for v := 0; v < bus.Cross; v++ {
			if t := f.tile(u, v); !busFree(bus, u, v) && l.At(t) == nil {
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
		l.Add(layout.Placement{Name: "transport-belt", Pos: f.tile(u, ln.V).Center(), Dir: d, Recipe: ln.Item, Tag: Tag(bus.Name)})
	}
	ln.End = max(ln.End, end)
	var res []layout.Placement
	for u := ln.Start; u < ln.End; u++ {
		res = append(res, layout.Placement{Name: "transport-belt", Pos: f.tile(u, ln.V).Center(), Dir: d, Recipe: ln.Item, Tag: Tag(bus.Name)})
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

// onBusArea — o — клиент шины.
func (b *Base) onBusArea(bus, o *Cluster) bool {
	_, ok := bus.Bus.Clients[o.Name]
	return ok
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
		for v := 0; v < bus.Cross; v++ {
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

// addLanes — ленты шины под входы клиента c: лент предмета — на весь расход с запасом.
func (b *Base) addLanes(bus, c *Cluster) error {
	bi := bus.Bus
	if bi.Demand == nil {
		bi.Demand = map[string]float64{}
	}
	for _, in := range c.In {
		if _, fluid := b.P.Fluids[in.Item]; fluid {
			continue // жидкости — трубами, не по шине
		}
		bi.Demand[in.Item] += in.Rate * max(1, c.Reserve)
		for n := bi.lanes(in.Item); n == 0 || float64(n)*BeltRate < bi.Demand[in.Item]; n++ {
			if len(bi.Lanes) >= (bi.Width-3)/5*2 {
				return fmt.Errorf("cluster: в шине %s нет места под ленту %s", bus.Name, in.Item)
			}
			bi.Lanes = append(bi.Lanes, Lane{Item: in.Item, V: laneV(len(bi.Lanes)), Start: BusHead, End: BusHead})
		}
	}
	return nil
}

// lanes — сколько лент предмета в шине.
func (bi *BusInfo) lanes(item string) int {
	n := 0
	for _, ln := range bi.Lanes {
		if ln.Item == item {
			n++
		}
	}
	return n
}
