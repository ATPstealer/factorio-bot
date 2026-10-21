package cluster

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"factorio-bot/internal/world"
)

// Этажная база (zones.md, «Расположение — этажами»): снизу вверх шина 1 (ресурсы), переработка,
// шина 2 (материалы), снабжение, шина 3 (промежуточное), конечный продукт, шина 4 (конечные),
// конечный продукт (лаборатории, ракетная шахта). Шины — на всю ширину базы; зоны производства
// одной глубины (по самой глубокой), ширина — по необходимости; зона сдвигается влево-вправо и
// отходит от шины снизу с зазором (обходит утёсы и воду). Ширина — вдоль шин (X), глубина —
// поперёк (Y).

const (
	floors     = 4
	floorGap   = 2 * ZoneBorder // между кластерами зоны — бордюры 3+3 = коридор 6 (zones.md)
	floorDepth = 40             // кластер длиннее — несколькими столбцами
	layerHead  = 10             // начало шин: головы лент, подвод с добычи
	layerSlack = 24             // запас ширины базы: зонам — сдвигаться влево-вправо
	maxShift   = 12             // зазор между шиной снизу и зоной — до стольких тайлов
	busLanes0  = 20             // лент шины ресурсов (zones.md), не меньше
	busLanes   = 16             // лент остальных шин, не меньше
)

// LayerBus — шина этажной базы: прямоугольник, лент по предметам (трубы жидкостей — тоже),
// всего лент с запасом, направление течения (E — вправо, W — влево; меняется).
type LayerBus struct {
	Rect  Rect           `json:"rect"`
	Lanes map[string]int `json:"lanes"`
	Total int            `json:"total"`
	Dir   string         `json:"dir"`
}

// Layered — этажная база: шины (1–4 снизу вверх), зоны производства (этажи 1–4), этаж кластера.
type Layered struct {
	Area   Rect           `json:"area"`
	Buses  []LayerBus     `json:"buses"`
	Floors []Rect         `json:"floors"`
	Of     map[string]int `json:"of"`
}

// floorOf — этаж кластера (0 — вне этажей: добыча, станция, хранение).
func floorOf(c *Cluster) int {
	switch c.Kind {
	case Smelt:
		return 1
	case Lab:
		return 4
	case Make:
		for _, u := range c.Units {
			switch u.Machine {
			case "rocket-silo":
				return 4
			case "oil-refinery", "chemical-plant":
				return 1
			}
		}
		if strings.HasSuffix(c.Product, "-science-pack") || c.Product == "rocket-part" {
			return 3
		}
		return 2
	}
	return 0
}

// pack — кластеры зоны в глубину depth: стопками столбцов (в стопке кластеры друг над другом,
// пока влезают), стопки — слева направо. Смещения от левого верхнего угла зоны и её ширина.
func pack(cs []*Cluster, depth int) (pos []world.Tile, width int) {
	order := make([]int, len(cs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return cs[order[a]].Len() > cs[order[b]].Len() })
	type stack struct{ x, w, used int }
	var st []stack
	pos = make([]world.Tile, len(cs))
	x := 0
	for _, i := range order {
		c := cs[i]
		placed := false
		for k := range st {
			if st[k].used+floorGap+c.Len() <= depth && c.Cross <= st[k].w {
				pos[i] = world.Tile{X: st[k].x, Y: st[k].used + floorGap}
				st[k].used += floorGap + c.Len()
				placed = true
				break
			}
		}
		if !placed {
			pos[i] = world.Tile{X: x, Y: 0}
			st = append(st, stack{x, c.Cross, c.Len()})
			x += c.Cross + floorGap
		}
	}
	return pos, max(0, x-floorGap)
}

// PlaceLayered — зоны всей базы этажами: добыча — по месторождениям, этажи — ближе к центру
// базы, станция — у воды ближе к базе, хранение — рядом. belt — пропускная способность ленты
// шины, шт/с.
func (b *Base) PlaceLayered(cs []*Cluster, belt float64) (*Layered, error) {
	g := b.grid()
	for _, c := range cs {
		if c.Name == "" {
			c.Name = b.name(c.Product)
		}
	}
	var fl [floors + 1][]*Cluster
	var later []*Cluster
	for _, c := range cs {
		switch f := floorOf(c); {
		case c.Kind == Mine:
			// добыча — зонами по месторождениям (MineZones, до этажей)
		case f == 0:
			later = append(later, c)
		default:
			if k := int(math.Ceil(float64(c.Len()) / floorDepth)); k > 1 { // длинный — столбцами
				for i := 0; i < k; i++ {
					p := scaleCluster(c, 1/float64(k))
					p.Count = max(1, (c.Count+k-1)/k)
					p.Room = p.Count
					if i > 0 {
						p.Name = fmt.Sprintf("%s.%d", c.Name, i+1)
					}
					fl[f] = append(fl[f], p)
				}
				continue
			}
			fl[f] = append(fl[f], c)
		}
	}
	// шины: шина i — выходы этажа i (шина 0 — то, что приходит извне); жидкости — трубами
	producedOn := map[string]int{}
	for f := 1; f <= floors; f++ {
		for _, c := range fl[f] {
			for _, o := range c.Out {
				producedOn[o.Item] = f
			}
		}
	}
	demand := make([]map[string]float64, floors)
	for i := range demand {
		demand[i] = map[string]float64{}
	}
	for f := 1; f <= floors; f++ {
		for _, c := range fl[f] {
			for _, in := range c.In {
				if in.Item == "water" {
					continue // вода — помпой на месте
				}
				if bus := producedOn[in.Item]; bus < floors {
					demand[bus][in.Item] += in.Rate
				}
			}
		}
	}
	L := &Layered{Of: map[string]int{}}
	widths := make([]int, floors)
	for i := range demand {
		lanes := map[string]int{}
		n := 0
		for it, r := range demand[i] {
			if _, fluid := b.P.Fluids[it]; fluid {
				lanes[it] = 1 // труба
			} else {
				lanes[it] = max(1, int(math.Ceil(r/belt)))
			}
			n += lanes[it]
		}
		least := busLanes
		if i == 0 {
			least = busLanes0
		}
		n = max(n, least)
		L.Buses = append(L.Buses, LayerBus{Lanes: lanes, Total: n, Dir: "E"})
		widths[i] = busWidth(n)
	}
	// глубина зон — по самой глубокой (самый длинный кластер), ширина — по укладке
	depth := 0
	for f := 1; f <= floors; f++ {
		for _, c := range fl[f] {
			depth = max(depth, c.Len())
		}
	}
	zw := make([]int, floors+1)
	var pos [floors + 1][]world.Tile
	base := 0
	for f := 1; f <= floors; f++ {
		pos[f], zw[f] = pack(fl[f], depth)
		base = max(base, zw[f])
	}
	width := layerHead + base + layerSlack
	lo := b.fitLayered(zw, depth, widths, width)
	if lo == nil {
		return nil, &NoSiteError{&Cluster{Name: "этажи"}, fmt.Errorf("cluster: нет места под этажную базу %d×~%d (утёсы и вода не толще %d, без месторождений)", width, floors*depth+sum(widths), maxGap)}
	}
	L.Area, L.Floors = lo.area, lo.floors
	for i := range L.Buses {
		L.Buses[i].Rect = lo.buses[i]
	}
	for f := 1; f <= floors; f++ {
		o := lo.floors[f].Min
		for i, c := range fl[f] {
			c.Vertical = true // ряд кластера — вдоль Y (столбец)
			c.Area = Rect{world.Tile{X: o.X + pos[f][i].X, Y: o.Y + pos[f][i].Y}, world.Tile{X: o.X + pos[f][i].X + c.Cross, Y: o.Y + pos[f][i].Y + c.Len()}}
			c.Rect = c.Area
			b.Clusters = append(b.Clusters, c)
			L.Of[c.Name] = f
		}
	}
	for _, r := range append(append([]Rect(nil), lo.buses...), lo.floors...) {
		if r.W() > 0 {
			g.mark(r.Grow(ZoneBorder))
		}
	}
	// станция — у воды, где меньше сумма «добыча угля → водоём» + «водоём → край базы»;
	// хранение — рядом с базой
	cx, cy := lo.area.Center()
	near := world.Tile{X: int(cx), Y: int(cy)}
	sort.SliceStable(later, func(i, j int) bool { return later[i].Kind == Power })
	for _, c := range later {
		c.Near = &near
		if c.Kind == Power {
			if shore, ok := b.powerSpot(lo.area); ok {
				c.Near = &shore
			}
		}
		if err := b.Place(c); err != nil {
			return nil, err
		}
	}
	return L, nil
}

func sum(xs []int) int {
	s := 0
	for _, x := range xs {
		s += x
	}
	return s
}

// layered — найденное место этажной базы.
type layered struct {
	area   Rect
	buses  []Rect
	floors []Rect
	dist   float64
}

// fitLayered — место этажной базы шириной width, ближе к центру базы: снизу вверх шина (на всю
// ширину), зона (ширина zw[f], глубина depth; сдвиг вбок и зазор от шины снизу — чтобы обойти
// утёсы и воду), шина, …; всё — без зон месторождений, неоткрытого, чужих коридоров, утёсов и
// воды толще maxGap. nil — нет места.
func (b *Base) fitLayered(zw []int, depth int, widths []int, width int) *layered {
	g := b.g
	height := floors*(depth+maxShift/2) + sum(widths)
	free := func(r Rect) bool {
		if n, out := g.count(g.ring, r.Grow(ZoneBorder)); n > 0 || out > 0 {
			return false
		}
		if n, _ := g.count(g.unknown, r); n > 0 {
			return false
		}
		if n, _ := g.count(g.zone, r); n > 0 {
			return false
		}
		n, _ := g.count(g.thick, r)
		return n == 0
	}
	stack := func(x0, y0 int) *layered {
		lo := &layered{buses: make([]Rect, floors), floors: make([]Rect, floors+1)}
		y := y0
		for f := 1; f <= floors; f++ {
			bus := Rect{world.Tile{X: x0, Y: y - widths[f-1]}, world.Tile{X: x0 + width, Y: y}}
			if !free(bus) {
				return nil
			}
			lo.buses[f-1], y = bus, bus.Min.Y-2*ZoneBorder
			if zw[f] == 0 {
				continue
			}
			placed := false
			for gap := 0; gap <= maxShift && !placed; gap += 2 {
				for sx := layerHead; sx+zw[f] <= width && !placed; sx += 2 {
					r := Rect{world.Tile{X: x0 + sx, Y: y - gap - depth}, world.Tile{X: x0 + sx + zw[f], Y: y - gap}}
					if free(r) {
						lo.floors[f], y, placed = r, r.Min.Y-2*ZoneBorder, true
					}
				}
			}
			if !placed {
				return nil
			}
		}
		lo.area = Rect{world.Tile{X: x0, Y: y + 2*ZoneBorder}, world.Tile{X: x0 + width, Y: y0}}
		return lo
	}
	ox, oy := b.Origin.X, b.Origin.Y
	var best *layered
	for r := 0; r*8 <= b.MaxR; r++ {
		if best != nil && float64(r*8) > best.dist+16 {
			break
		}
		for _, o := range ringOffsets(r) {
			x0, y0 := ox+o.X*8-width/2, oy+o.Y*8+height/2
			lo := stack(x0, y0)
			if lo == nil {
				continue
			}
			cx, cy := lo.area.Center()
			lo.dist = math.Hypot(cx-float64(ox), cy-float64(oy))
			if best == nil || lo.dist < best.dist {
				best = lo
			}
		}
	}
	return best
}

// powerSpot — берег (вода рядом с сушей), где меньше всего сумма расстояний от ближайшей
// зоны добычи угля до него и от него до края базы area (zones.md, энергия).
func (b *Base) powerSpot(area Rect) (world.Tile, bool) {
	var coal []Rect
	for _, z := range b.mineZones {
		if z.Resources["coal"] > 0 {
			coal = append(coal, z.Rect)
		}
	}
	if len(coal) == 0 {
		return world.Tile{}, false
	}
	toRect := func(t world.Tile, r Rect) float64 {
		dx := math.Max(0, math.Max(float64(r.Min.X-t.X), float64(t.X-r.Max.X)))
		dy := math.Max(0, math.Max(float64(r.Min.Y-t.Y), float64(t.Y-r.Max.Y)))
		return math.Hypot(dx, dy)
	}
	g := b.g
	best, bd := world.Tile{}, math.Inf(1)
	for y := 0; y < g.h; y += 4 {
		for x := 0; x < g.w; x += 4 {
			t := world.Tile{X: g.x0 + x, Y: g.y0 + y}
			if b.W.TileAt(t) != world.Water {
				continue
			}
			shore := false
			for _, d := range []world.Tile{{X: 4}, {X: -4}, {Y: 4}, {Y: -4}} {
				shore = shore || b.W.TileAt(world.Tile{X: t.X + d.X, Y: t.Y + d.Y}) == world.Land
			}
			if !shore {
				continue
			}
			dc := math.Inf(1)
			for _, r := range coal {
				dc = math.Min(dc, toRect(t, r))
			}
			if d := dc + toRect(t, area); d < bd {
				best, bd = t, d
			}
		}
	}
	return best, !math.IsInf(bd, 1)
}
