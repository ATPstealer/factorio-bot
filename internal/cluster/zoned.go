package cluster

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Кластеры внутри зон (zones.md, стадия 5): место кластера — в зоне его вида, шины — ленты в
// зонах шин (bus-1 ресурсы … bus-4 конечные), всё течёт вправо (+X): головы лент — у левого
// края шины. Зона берёт с шины под собой (снабжение — и над собой), отдаёт в шину над собой;
// что нужно выше, чем лежит, — сквозной лентой на следующие шины; уголь на станцию — отводом
// прямо с ленты добычи угля, мимо шин.

// ZBus — шина в зоне: прямоугольник и ленты (предмет, ряд Y).
type ZBus struct {
	Name  string     `json:"name"`
	Rect  Rect       `json:"rect"`
	Lanes []ZoneLane `json:"lanes"`
}

// ZoneLane — лента шины: предмет, ряд (Y), есть ли поставщик в голове.
type ZoneLane struct {
	Item string `json:"item"`
	Y    int    `json:"y"`
	Fed  bool   `json:"fed,omitempty"`
}

func (zb *ZBus) lane(item string) *ZoneLane {
	for i := range zb.Lanes {
		if zb.Lanes[i].Item == item {
			return &zb.Lanes[i]
		}
	}
	return nil
}

// floorOfZone — номер этажа (1–4) зоны производства по её виду и порядку (final-1 → 3, final-2 → 4).
func floorOfZone(z Zone) int {
	switch {
	case z.Kind == ZoneProcessing:
		return 1
	case z.Kind == ZoneSupply:
		return 2
	case z.Kind == ZoneFinal && strings.HasSuffix(z.Name, "-2"):
		return 4
	case z.Kind == ZoneFinal:
		return 3
	}
	return 0
}

// PlaceZoned — места кластерам cs внутри зон zones: добыча — в зонах добычи своего ресурса
// (ближе к шине ресурсов), производство — в зоне своего этажа, станция — в зоне энергии,
// хранение — рядом с базой. Возвращает шины (ленты — потом, ConnectZoned).
func (b *Base) PlaceZoned(cs []*Cluster, zones []Zone) ([]*ZBus, error) {
	b.grid()
	var buses []*ZBus
	floor := map[int]Rect{}
	var mines []Zone
	var power *Rect
	base := Rect{world.Tile{X: math.MaxInt, Y: math.MaxInt}, world.Tile{X: math.MinInt, Y: math.MinInt}}
	for _, z := range zones {
		switch z.Kind {
		case ZoneBus:
			buses = append(buses, &ZBus{Name: z.Name, Rect: z.Rect})
			base = union(base, z.Rect)
		case ZoneMine:
			mines = append(mines, z)
		case ZonePower:
			r := z.Rect
			power = &r
		default:
			floor[floorOfZone(z)] = z.Rect
		}
	}
	sort.Slice(buses, func(i, j int) bool { return buses[i].Name < buses[j].Name })
	if len(buses) == 0 {
		return nil, fmt.Errorf("cluster: в зонах нет шин")
	}
	head := world.Tile{X: buses[0].Rect.Min.X, Y: buses[0].Rect.Min.Y}
	for _, c := range cs {
		if c.Name == "" {
			c.Name = b.name(c.Product)
		}
	}
	// добыча: зона своего ресурса, ближняя к голове шины ресурсов
	for _, c := range cs {
		if c.Kind != Mine {
			continue
		}
		placed := false
		sort.SliceStable(mines, func(i, j int) bool {
			xi, yi := mines[i].Rect.Center()
			xj, yj := mines[j].Rect.Center()
			return math.Hypot(xi-float64(head.X), yi-float64(head.Y)) < math.Hypot(xj-float64(head.X), yj-float64(head.Y))
		})
		for _, z := range mines {
			if z.Resources[c.Product] == 0 {
				continue
			}
			r := z.Rect
			c.Zone, c.Near = &r, &head
			if err := b.Place(c); err == nil {
				placed = true
				break
			}
		}
		if !placed {
			return nil, &NoSiteError{c, fmt.Errorf("cluster: в зонах добычи %s нет места под %s", c.Product, c.Name)}
		}
	}
	for _, c := range cs {
		f := floorOf(c)
		switch {
		case c.Kind == Mine:
			continue
		case c.Kind == Power:
			if power == nil {
				return nil, fmt.Errorf("cluster: нет зоны энергии")
			}
			r := power.Grow(4)
			c.Zone = &r
		case f > 0:
			r, ok := floor[f]
			if !ok {
				return nil, fmt.Errorf("cluster: нет зоны этажа %d под %s", f, c.Name)
			}
			c.Zone = &r
			left := world.Tile{X: r.Min.X, Y: (r.Min.Y + r.Max.Y) / 2} // ближе к головам шин
			c.Near = &left
		default: // хранение — рядом с базой
			cx, cy := base.Center()
			near := world.Tile{X: int(cx), Y: int(cy)}
			c.Near = &near
		}
		if err := b.Place(c); err != nil {
			return nil, err
		}
	}
	return buses, nil
}

// ConnectZoned — логистика кластеров в зонах: ленты шин, подводы с добычи в головы лент шины
// ресурсов, выходы этажей — боком в ленты шины над собой, сквозные ленты (что нужно выше), входы
// — делителем с ленты шины снизу (снабжение — и сверху), уголь на станцию — отводом с ленты
// добычи угля, вода станции — помпой.
func (b *Base) ConnectZoned(l *layout.Layout, buses []*ZBus) error {
	// где что лежит: шина 0 — добыча; шина f — выходы этажа f
	on := map[string]int{}
	for _, c := range b.Clusters {
		if c.Kind == Mine {
			on[c.Product] = 0
		}
	}
	for _, c := range b.Clusters {
		if f := floorOf(c); f > 0 && f < len(buses) {
			for _, o := range c.Out {
				if _, set := on[o.Item]; !set {
					on[o.Item] = f
				}
			}
		}
	}
	// ленты: предмет — на своей шине и сквозной на всех до шины под потребителем
	need := make([]map[string]bool, len(buses))
	for i := range need {
		need[i] = map[string]bool{}
	}
	for _, c := range b.Clusters {
		f := floorOf(c)
		if f == 0 {
			continue
		}
		for _, in := range c.In {
			src, ok := on[in.Item]
			if !ok {
				continue
			}
			if _, fluid := b.P.Fluids[in.Item]; fluid {
				continue
			}
			for k := src; k <= f-1 && k < len(buses); k++ {
				need[k][in.Item] = true
			}
		}
	}
	for k, bus := range buses {
		var items []string
		for it := range need[k] {
			items = append(items, it)
		}
		sort.Slice(items, func(i, j int) bool { // сначала свои (этажа под шиной), потом сквозные
			si, sj := on[items[i]] == k, on[items[j]] == k
			if si != sj {
				return si
			}
			return items[i] < items[j]
		})
		for j, it := range items {
			v := laneV(j)
			if v >= bus.Rect.H() {
				return fmt.Errorf("cluster: %s: нет ряда под ленту %s", bus.Name, it)
			}
			bus.Lanes = append(bus.Lanes, ZoneLane{Item: it, Y: bus.Rect.Min.Y + v})
			for x := bus.Rect.Min.X; x < bus.Rect.Max.X; x++ {
				l.Add(layout.Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: bus.Rect.Min.Y + v}.Center(), Dir: layout.E, Recipe: it, Tag: Tag(bus.Name)})
			}
			l.AllowEnd(world.Tile{X: bus.Rect.Max.X, Y: bus.Rect.Min.Y + v})
		}
	}
	laneBelts := func(bus *ZBus, ln *ZoneLane, x0, x1 int) []layout.Placement {
		var res []layout.Placement
		for x := max(x0, bus.Rect.Min.X); x < min(x1, bus.Rect.Max.X); x++ {
			if p := l.At(world.Tile{X: x, Y: ln.Y}); p != nil && p.Name == "transport-belt" {
				res = append(res, *p)
			}
		}
		return res
	}
	// подвод боком в ленту ln шины bus у столбцов x (зазор над или под лентой)
	sidePorts := func(bus *ZBus, ln *ZoneLane, x0, x1 int) []Port {
		var res []Port
		for x := max(x0, bus.Rect.Min.X+1); x < min(x1, bus.Rect.Max.X-1); x++ {
			for _, s := range []struct{ dy, d int }{{-1, layout.S}, {1, layout.N}} {
				t := world.Tile{X: x, Y: ln.Y + s.dy}
				if l.Free(t, layout.AnyLand) {
					res = append(res, Port{T: t, Dir: s.d, Item: ln.Item, In: true})
				}
			}
		}
		return res
	}
	route := func(from Port, to []Port, tag string) error {
		var err error = layout.ErrNoRoute
		for _, p := range to {
			if _, err = l.RouteBelt(from.T, from.Dir, p.T, p.Dir, tag); err == nil {
				return nil
			}
		}
		return err
	}
	outPort := func(c *Cluster) (Port, bool) {
		for _, p := range c.Ports {
			if !p.In {
				return p, true
			}
		}
		return Port{}, false
	}
	// вода станции — первой (потом ленты не замуруют вход)
	for _, c := range b.Clusters {
		if c.Water == nil {
			continue
		}
		_, err := l.WaterPump(c.Water.To, LogisticsTag+"/"+c.Name, func(pump layout.Placement) bool {
			if !l.CanPlace(pump, layout.AnyLand) {
				return false
			}
			l.Add(pump)
			_, err := l.RoutePipe(layout.Connections(l.P, pump)[0], *c.Water, LogisticsTag+"/"+c.Name)
			return err == nil
		})
		if err != nil {
			return fmt.Errorf("вода к %s: %w", c.Name, err)
		}
	}
	// добыча → головы лент шины ресурсов (второй поставщик — боком у головы)
	for _, c := range b.Clusters {
		op, ok := outPort(c)
		if c.Kind != Mine || !ok {
			continue
		}
		ln := buses[0].lane(c.Product)
		if ln == nil {
			continue
		}
		bus := buses[0]
		var to []Port
		if !ln.Fed {
			to = append(to, Port{T: world.Tile{X: bus.Rect.Min.X - 1, Y: ln.Y}, Dir: layout.E, Item: ln.Item, In: true})
		}
		to = append(to, sidePorts(bus, ln, bus.Rect.Min.X, bus.Rect.Min.X+12)...)
		if err := route(op, to, LogisticsTag+"/"+bus.Name+"/"+c.Name); err != nil {
			return fmt.Errorf("добыча %s → %s (%s): %w", c.Name, bus.Name, c.Product, err)
		}
		ln.Fed = true
	}
	// выходы этажей — боком в ленты шины над собой, у своих столбцов
	for _, c := range b.Clusters {
		f := floorOf(c)
		op, ok := outPort(c)
		if f == 0 || f >= len(buses) || !ok {
			continue
		}
		bus := buses[f]
		ln := bus.lane(op.Item)
		if ln == nil {
			continue // продукт выше не нужен (буфер выхода)
		}
		to := sidePorts(bus, ln, c.Area.Min.X-20, c.Area.Max.X+20)
		sort.SliceStable(to, func(i, j int) bool {
			return math.Abs(float64(to[i].T.X-op.T.X)) < math.Abs(float64(to[j].T.X-op.T.X))
		})
		if len(to) > 40 {
			to = to[:40]
		}
		if err := route(op, to, LogisticsTag+"/"+bus.Name+"/"+c.Name); err != nil {
			return &ConnectError{c, fmt.Errorf("%s → %s (%s): %w", c.Name, bus.Name, op.Item, err)}
		}
		ln.Fed = true
	}
	// сквозные ленты: голова ленты шины k — отводом с той же ленты шины k-1
	for k := 1; k < len(buses); k++ {
		for i := range buses[k].Lanes {
			ln := &buses[k].Lanes[i]
			if ln.Fed {
				continue
			}
			low := buses[k-1].lane(ln.Item)
			if low == nil {
				continue
			}
			p := Port{T: world.Tile{X: buses[k].Rect.Min.X - 1, Y: ln.Y}, Dir: layout.E, Item: ln.Item, In: true}
			line := laneBelts(buses[k-1], low, buses[k-1].Rect.Min.X, buses[k-1].Rect.Min.X+40)
			if err := SplitTap(l, line, p, LogisticsTag+"/"+buses[k].Name+"/"+buses[k-1].Name, 40); err != nil {
				return fmt.Errorf("сквозная %s → %s (%s): %w", buses[k-1].Name, buses[k].Name, ln.Item, err)
			}
			ln.Fed = true
		}
	}
	// входы: делителем с ленты шины снизу (снабжение — и сверху), у своих столбцов
	for _, c := range b.Clusters {
		f := floorOf(c)
		if f == 0 {
			continue
		}
		for _, p := range c.Ports {
			if !p.In {
				continue
			}
			var line []layout.Placement
			for _, k := range []int{f - 1, f} {
				if k >= len(buses) || (k == f && c.Kind != Make) {
					continue
				}
				if ln := buses[k].lane(p.Item); ln != nil && len(line) == 0 {
					line = laneBelts(buses[k], ln, c.Area.Min.X-40, c.Area.Max.X+40)
				}
			}
			if len(line) == 0 {
				return fmt.Errorf("cluster: %s ← %s: на шинах рядом нет ленты", c.Name, p.Item)
			}
			if err := SplitTap(l, line, p, LogisticsTag+"/"+c.Name, 40); err != nil {
				return &ConnectError{c, fmt.Errorf("шина → %s (%s): %w", c.Name, p.Item, err)}
			}
		}
	}
	// уголь на станцию — отводом с ленты добычи угля (мимо шин)
	for _, c := range b.Clusters {
		if c.Kind != Power {
			continue
		}
		for _, p := range c.Ports {
			if !p.In || p.Item != "coal" {
				continue
			}
			var feed []layout.Placement
			for _, m := range b.Clusters {
				if m.Kind == Mine && m.Product == "coal" {
					for _, q := range l.List {
						if q.Tag == LogisticsTag+"/"+buses[0].Name+"/"+m.Name && q.Name == "transport-belt" {
							feed = append(feed, q)
						}
					}
				}
			}
			if err := SplitTap(l, feed, p, LogisticsTag+"/"+c.Name, 60); err != nil {
				return fmt.Errorf("уголь на %s: %w", c.Name, err)
			}
		}
	}
	return nil
}
