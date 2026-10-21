package cluster

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"factorio-bot/internal/world"
)

// Этажная база (cluster.md 5а): прямоугольник из горизонтальных полос снизу вверх — шина 0
// (ресурсы), этаж 1 (плавка, нефтехимия), шина 1 (материалы), этаж 2 (сборка промежуточного),
// шина 2 (промежуточное), этаж 3 (наука, ракетные части), шина 3 (конечные), этаж 4
// (лаборатории, ракетная шахта). Клиенты этажа — столбцами через коридоры floorGap; добыча —
// на месторождениях, станция — у воды, хранение — рядом.

const (
	floors     = 4
	floorGap   = 2 * ZoneBorder // между кластерами этажа — бордюры 3+3 = коридор 6 (zones.md)
	floorDepth = 40             // глубина этажа: клиент длиннее — несколькими столбцами
	layerHead  = 10             // начало прямоугольника: головы шин, подвод с добычи
)

// LayerBus — шина этажной базы: прямоугольник и лент по предметам.
type LayerBus struct {
	Rect  Rect           `json:"rect"`
	Lanes map[string]int `json:"lanes"`
}

// Layered — этажная база: прямоугольник, шины (0–3 снизу вверх) и этажи (1–4).
type Layered struct {
	Area   Rect           `json:"area"`
	Buses  []LayerBus     `json:"buses"`
	Floors []Rect         `json:"floors"`
	Of     map[string]int `json:"of"` // кластер → этаж
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
			switch {
			case u.Machine == "rocket-silo":
				return 4
			case u.Machine == "oil-refinery", u.Machine == "chemical-plant":
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

// PlaceLayered — прикидка всей базы этажами: добыча — по месторождениям, станция и хранение —
// как обычно, остальное — в прямоугольник этажей ближе к центру базы. belt — пропускная
// способность ленты шины, шт/с.
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
			if err := b.placeMines(c); err != nil {
				return nil, err
			}
		case f == 0:
			later = append(later, c)
		default:
			// длинный клиент — несколькими столбцами
			if k := int(math.Ceil(float64(c.Len()) / floorDepth)); k > 1 {
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
	// шины: что этаж отдаёт выше (шина f — выходы этажа f; шина 0 — входы этажа 1 с добычи)
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
				if _, fluid := b.P.Fluids[in.Item]; fluid {
					continue // жидкости — трубами
				}
				bus := 0
				if pf, ok := producedOn[in.Item]; ok {
					bus = pf
				}
				if bus < floors {
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
			lanes[it] = max(1, int(math.Ceil(r/belt)))
			n += lanes[it]
		}
		L.Buses = append(L.Buses, LayerBus{Lanes: lanes})
		if n > 0 {
			widths[i] = busWidth(n)
		}
	}
	// этажи: глубина — самый длинный клиент, длина — столбцы через проход
	depth := make([]int, floors+1)
	length := 0
	for f := 1; f <= floors; f++ {
		u := layerHead
		for _, c := range fl[f] {
			depth[f] = max(depth[f], c.Len())
			u += c.Cross + floorGap
		}
		length = max(length, u)
	}
	H := 0
	for f := 1; f <= floors; f++ {
		H += depth[f] + widths[f-1]
	}
	if H == 0 {
		return L, nil
	}
	// место: ближе к центру базы, без зон месторождений, неоткрытого, чужих коридоров; утёсы
	// и вода — не толще maxGap подряд
	best, bestD := Rect{}, math.Inf(1)
	ok := func(r Rect) bool {
		ring := r.Grow(Corridor)
		if n, out := g.count(g.ring, ring); n > 0 || out > 0 {
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
	ox, oy := b.Origin.X, b.Origin.Y
	for rr := 0; rr <= b.MaxR; rr += 4 {
		if float64(rr) > bestD+8 {
			break
		}
		for _, o := range ringOffsets(rr) {
			x0, y0 := ox+o.X-length/2, oy+o.Y-H/2
			r := Rect{world.Tile{X: x0, Y: y0}, world.Tile{X: x0 + length, Y: y0 + H}}
			cx, cy := r.Center()
			d := math.Hypot(cx-float64(ox), cy-float64(oy))
			if d < bestD && ok(r) {
				best, bestD = r, d
			}
		}
	}
	if math.IsInf(bestD, 1) {
		return nil, &NoSiteError{&Cluster{Name: "этажи"}, fmt.Errorf("cluster: нет места под этажную базу %d×%d (утёсы и вода не толще %d, без месторождений)", length, H, maxGap)}
	}
	L.Area = best
	// снизу вверх: шина 0, этаж 1, шина 1, …, этаж 4 (низ — большие Y)
	y := best.Max.Y
	L.Floors = make([]Rect, floors+1)
	for f := 1; f <= floors; f++ {
		if w := widths[f-1]; w > 0 {
			L.Buses[f-1].Rect = Rect{world.Tile{X: best.Min.X, Y: y - w}, world.Tile{X: best.Max.X, Y: y}}
			y -= w
		}
		L.Floors[f] = Rect{world.Tile{X: best.Min.X, Y: y - depth[f]}, world.Tile{X: best.Max.X, Y: y}}
		u := best.Min.X + layerHead
		for _, c := range fl[f] {
			// столбец: ряд клиента — вдоль Y, начало — у шины снизу
			c.Vertical = true
			c.Area = Rect{world.Tile{X: u, Y: y - c.Len()}, world.Tile{X: u + c.Cross, Y: y}}
			c.Rect = c.Area
			b.Clusters = append(b.Clusters, c)
			L.Of[c.Name] = f
			u += c.Cross + floorGap
		}
		y -= depth[f]
	}
	g.mark(best.Grow(Corridor))
	// станция, хранение — как обычно (ближе к своим поставщикам и центру)
	sort.SliceStable(later, func(i, j int) bool { return later[i].Kind == Power })
	for _, c := range later {
		if err := b.Place(c); err != nil {
			return nil, err
		}
	}
	return L, nil
}
