package cluster

import (
	"fmt"
	"math"
	"slices"
	"sort"

	"factorio-bot/internal/bridge"
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
