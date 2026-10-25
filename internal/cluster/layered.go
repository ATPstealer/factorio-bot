package cluster

import (
	"fmt"
	"math"
	"slices"
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
	maxShift   = 24             // зазор шина — зона сверх коридора 6 (снизу и сверху от зоны): всего 6…30 (zones.md)
	busLanes0  = 20             // лент шины ресурсов (zones.md), не меньше
	busSpare0  = 4              // шине ресурсов — ещё лент в запас на будущее (решение пользователя)
	busLanes   = 16             // лент шин 2 и 3, не меньше
	busLanes4  = 8              // лент шины конечных (4) — вдвое меньше, не меньше
	busThick   = 0.03           // шине: толстых утёсов и воды — не больше доли площади
	mineGap    = 16             // от зон базы до зон добычи — подводам развернуться (zones.md)
	// этаж выше — правее нижнего на долю ширины нижнего (zones.md): лучше floorShift, хоть floorShiftMin
	floorShift, floorShiftMin = 0.5, 0.3
)

// minCols — столбцов в зоне этажа не меньше (zones.md): 15 (30 на картах не влезают); colWidth — ширина столбца: кластер с бордюрами (плавка 8+6, сборка 9+6).
var (
	minCols  = [floors + 1]int{1: 15, 2: 15, 3: 15, 4: 15}
	colWidth = [floors + 1]int{1: 14, 2: 15, 3: 15, 4: 15}
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
	Floors [][]Rect       `json:"floors"` // зоны этажа: части слева направо (zones.md)
	Of     map[string]int `json:"of"`
}

// floorOf — этаж кластера (0 — вне этажей: добыча, станция, хранение).
func floorOf(c *Cluster) int {
	switch c.Kind {
	case Smelt:
		return 1
	case Lab:
		return 3 // лаборатории — на этаже конечного продукта, справа (zones.md, решение пользователя)
	case Make:
		for _, u := range c.Units {
			switch u.Machine {
			case "rocket-silo":
				return 3 // шахта — на этаже конечного продукта, справа (четвёртого этажа нет)
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
// length — длина кластера (nil — Len).
func pack(cs []*Cluster, depth int, length func(*Cluster) int) (pos []world.Tile, width int) {
	if length == nil {
		length = (*Cluster).Len
	}
	order := make([]int, len(cs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return length(cs[order[a]]) > length(cs[order[b]]) })
	type stack struct{ x, w, used int }
	var st []stack
	pos = make([]world.Tile, len(cs))
	x := 0
	for _, i := range order {
		c := cs[i]
		placed := false
		for k := range st {
			if st[k].used+floorGap+length(c) <= depth && c.Cross <= st[k].w {
				pos[i] = world.Tile{X: st[k].x, Y: st[k].used + floorGap}
				st[k].used += floorGap + length(c)
				placed = true
				break
			}
		}
		if !placed {
			pos[i] = world.Tile{X: x, Y: 0}
			st = append(st, stack{x, c.Cross, length(c)})
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
			c.Name = b.name(c.Product, cs)
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
					p.Need = min(p.Count, max(1, (c.Need+k-1)/k))
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
		switch i {
		case 0:
			least = busLanes0
		case 3:
			least = busLanes4
		}
		n = max(n, least)
		if i == 0 {
			n += busSpare0 // шине ресурсов — ещё ленты в запас (решение пользователя)
		}
		L.Buses = append(L.Buses, LayerBus{Lanes: lanes, Total: n, Dir: "E"})
		widths[i] = busWidth(n)
	}
	// глубина зон — по самой глубокой (самый длинный кластер), ширина — по укладке. Минимум
	// MinUnits мест удлиняет кластеры — этажи растут в глубину, а не в ширину (cluster.md §2):
	// ширина — как у кластеров без минимума, глубина — пока укладка в неё не влезет
	len0 := func(c *Cluster) int {
		if c.Need == 0 || c.Need > c.Count {
			return c.Len()
		}
		return c.Len() - (c.Count-c.Need)*c.Pitch
	}
	depth, depth0 := 0, 0
	for f := 1; f <= floors; f++ {
		for _, c := range fl[f] {
			depth, depth0 = max(depth, c.Len()), max(depth0, len0(c))
		}
	}
	widest := func(d int, length func(*Cluster) int) int {
		w := 0
		for f := 1; f <= floors; f++ {
			_, x := pack(fl[f], d, length)
			w = max(w, x)
		}
		return w
	}
	for w0 := widest(depth0, len0); widest(depth, nil) > w0 && depth < 4*max(depth0, 1); {
		depth += 2
	}
	// верхний этаж (лаборатории, шахта) — вдвое мельче (zones.md), не короче своих кластеров
	depths := make([]int, floors+1)
	for f := 1; f <= floors; f++ {
		depths[f] = depth
	}
	depths[floors] = depth / 2
	for _, c := range fl[floors] {
		depths[floors] = max(depths[floors], c.Len())
	}
	// этаж конечного продукта (с лабораториями справа) — вдвое мельче (решение пользователя)
	depths[3] = depth / 2
	for _, c := range fl[3] {
		depths[3] = max(depths[3], c.Len())
	}
	// лаборатории и шахта — правее сборки этажа (укладка — слева направо)
	right := func(c *Cluster) int {
		if c.Kind == Lab || slices.ContainsFunc(c.Units, func(u Unit) bool { return u.Machine == "rocket-silo" }) {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(fl[3], func(a, b *Cluster) int { return right(a) - right(b) })
	zw := make([]int, floors+1)
	var pos [floors + 1][]world.Tile
	base := 0
	for f := 1; f <= floors; f++ {
		pos[f], zw[f] = pack(fl[f], depths[f], nil)
		if len(fl[f]) > 0 { // не уже минимума (zones.md): прикидка даёт мало машин
			zw[f] = max(zw[f], minCols[f]*colWidth[f])
		}
		if f == 1 && zw[f] > 0 { // переработка почти забита — шире на 30% (решение пользователя)
			zw[f] = zw[f] * 13 / 10
		}
		if b.CliffZones && zw[f] > 0 { // утёсы съедают место — шире на половину (zones.md)
			zw[f] = zw[f] * 3 / 2
		}
		base = max(base, zw[f])
	}
	width := layerHead + base + layerSlack
	// части этажей между препятствиями — база шире; из ширин — место ближе к старту (лишний тайл
	// ширины — как тайл расстояния): у старта препятствия, а узкая база уезжает за сотни тайлов
	var lo *layered
	bestCost := math.Inf(1)
	for _, extra := range []int{0, base / 4, base / 2, base} {
		if l := b.fitLayered(zw, colWidth[:], depths, widths, width+extra); l != nil && l.dist+float64(extra) < bestCost {
			lo, bestCost = l, l.dist+float64(extra)
		}
	}
	if lo == nil {
		return nil, &NoSiteError{&Cluster{Name: "этажи"}, fmt.Errorf("cluster: нет места под этажную базу %d×~%d (утёсы и вода не толще %d, без месторождений)", width, sum(depths)+sum(widths), maxGap)}
	}
	L.Area, L.Floors = lo.area, lo.floors
	for i := range L.Buses {
		L.Buses[i].Rect = lo.buses[i]
	}
	for f := 1; f <= floors; f++ {
		at := partsAt(pos[f], fl[f], lo.floors[f])
		for i, c := range fl[f] {
			o := at[pos[f][i].X]
			c.Vertical = true // ряд кластера — вдоль Y (столбец)
			c.Area = Rect{world.Tile{X: o.X, Y: o.Y + pos[f][i].Y}, world.Tile{X: o.X + c.Cross, Y: o.Y + pos[f][i].Y + c.Len()}}
			c.Rect = c.Area
			b.Clusters = append(b.Clusters, c)
			L.Of[c.Name] = f
		}
	}
	rs := append([]Rect(nil), lo.buses...)
	for _, ps := range lo.floors {
		rs = append(rs, ps...)
	}
	for _, r := range rs {
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

// partsAt — где столбцы укладки (pack: x столбца → его кластеры) встают в частях этажа: по
// частям слева направо, столбец не влез в часть — в следующую. Ключ — x столбца в укладке.
func partsAt(pos []world.Tile, cs []*Cluster, parts []Rect) map[int]world.Tile {
	colw := map[int]int{}
	for i, c := range cs {
		colw[pos[i].X] = max(colw[pos[i].X], c.Cross)
	}
	var xs []int
	for x := range colw {
		xs = append(xs, x)
	}
	sort.Ints(xs)
	res := map[int]world.Tile{}
	pi, off := 0, 0
	for _, x := range xs {
		for pi < len(parts)-1 && off+colw[x] > parts[pi].W() {
			pi, off = pi+1, 0
		}
		res[x] = world.Tile{X: parts[pi].Min.X + off, Y: parts[pi].Min.Y}
		off += colw[x] + floorGap
	}
	return res
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
	floors [][]Rect
	dist   float64
}

// fitLayered — место этажной базы шириной width, голова bus-1 — ближе к добыче (mineAnchor): снизу вверх шина (на всю
// ширину), зона (ширина zw[f], глубина depths[f]; зазор от шины снизу и части между препятствиями —
// чтобы обойти утёсы и воду; часть — не уже столбца colw[f]), шина, …; всё — без зон
// месторождений, неоткрытого, чужих коридоров, утёсов и воды толще maxGap. nil — нет места.
func (b *Base) fitLayered(zw, colw []int, depths []int, widths []int, width int) *layered {
	g := b.g
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
		if b.CliffZones { // база с утёсами: в зоне утёсы можно, вода — нет (zones.md)
			n, _ := g.count(g.thickWet, r)
			return n == 0
		}
		n, _ := g.count(g.thick, r)
		return n == 0
	}
	// шине — мягче (zones.md): толстых утёсов и воды — не больше busThick площади, ленты обойдут
	freeBus := func(r Rect) bool {
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
		return float64(n) <= busThick*float64(r.W()*r.H())
	}
	// части этажа f между шинами (от top до bottom): свободные полосы на всю глубину в [lo, hi)
	// — слева направо (right — справа налево, прижать вправо), пока в сумме не наберётся zw[f];
	// nil — не набралось
	parts := func(f, lo, hi, top, bottom int, right bool) []Rect {
		var runs [][2]int // свободные полосы [a, b) слева направо
		run := -1
		for x := lo; x <= hi; x++ {
			if x < hi && free(Rect{world.Tile{X: x, Y: top}, world.Tile{X: x + 1, Y: bottom}}) {
				if run < 0 {
					run = x
				}
				continue
			}
			if run >= 0 && x-run >= colw[f] {
				runs = append(runs, [2]int{run, x})
			}
			run = -1
		}
		if right {
			slices.Reverse(runs)
		}
		var res []Rect
		need := zw[f]
		for _, r := range runs {
			if need <= 0 {
				break
			}
			w := max(min(r[1]-r[0], need), colw[f])
			a := r[0]
			if right {
				a = r[1] - w
			}
			res = append(res, Rect{world.Tile{X: a, Y: top}, world.Tile{X: a + w, Y: bottom}})
			need -= w
		}
		if need > 0 {
			return nil
		}
		sort.Slice(res, func(i, j int) bool { return res[i].Min.X < res[j].Min.X })
		return res
	}
	// лесенка (zones.md): этаж ищется в окне шириной span от своего левого края; этаж выше —
	// правее нижнего (левый край — на 50%, не влезло — на 30% ширины нижнего правее его левого
	// края), верхний (лаборатории, шахта) — прижат вправо в своём окне; шина под этажом — от
	// левого края этажа под ней (у первой — от головы) до правого края этажа над ней
	span := width - layerHead
	stack := func(x0, y0 int) *layered {
		lo := &layered{buses: make([]Rect, floors), floors: make([][]Rect, floors+1)}
		y := y0
		busL := x0                      // левый край шины над последним этажом
		prevL, prevW := x0+layerHead, 0 // этаж ниже: левый край и ширина
		area := Rect{world.Tile{X: x0, Y: y0}, world.Tile{X: x0, Y: y0}}
		for f := 1; f <= floors; f++ {
			busY := Rect{world.Tile{Y: y - widths[f-1]}, world.Tile{Y: y}}
			y = busY.Min.Y - 2*ZoneBorder
			if zw[f] == 0 { // пустой этаж: шина — под окном этажа ниже
				// (верхняя шина над конечным продуктом — не длиннее его этажа: пользователь)
				bus := Rect{world.Tile{X: busL, Y: busY.Min.Y}, world.Tile{X: prevL + max(prevW, 1), Y: busY.Max.Y}}
				if !freeBus(bus) {
					return nil
				}
				lo.buses[f-1] = bus
				area = union(area, bus)
				continue
			}
			shifts := []float64{0}
			if prevW > 0 {
				shifts = []float64{floorShift, floorShiftMin}
			}
			placed := false
			// шина над этажом ниже — выше коридора ещё на 0…maxShift (bus-1 — на месте)
			low := busY.Max.Y // нижний край шины без зазора (Y растёт вниз)
			for bg := 0; bg <= maxShift && !placed; bg += 2 {
				if f == 1 && bg > 0 {
					break
				}
				busY = Rect{world.Tile{Y: low - bg - widths[f-1]}, world.Tile{Y: low - bg}}
				y = busY.Min.Y - 2*ZoneBorder
				for _, sh := range shifts {
					l := prevL + int(sh*float64(prevW))
					for gap := 0; gap <= maxShift && !placed; gap += 2 {
						ps := parts(f, l, l+span, y-gap-depths[f], y-gap, f == floors)
						if ps == nil {
							continue
						}
						left := busL
						if f == 1 { // шина ресурсов — от головы (layerHead левее переработки)
							left = ps[0].Min.X - layerHead
						}
						bus := Rect{world.Tile{X: left, Y: busY.Min.Y}, world.Tile{X: ps[len(ps)-1].Max.X, Y: busY.Max.Y}}
						// шине ресурсов — зазор от добычи: в головы слева входят подводы (zones.md)
						if !freeBus(bus) || f == 1 && b.nearMine(bus) {
							continue
						}
						lo.buses[f-1], lo.floors[f] = bus, ps
						area = union(union(area, bus), union(ps[0], ps[len(ps)-1]))
						y, placed = y-gap-depths[f]-2*ZoneBorder, true
						busL, prevL, prevW = ps[0].Min.X, ps[0].Min.X, ps[len(ps)-1].Max.X-ps[0].Min.X
					}
					if placed {
						break
					}
				}
			}
			if !placed {
				return nil
			}
		}
		lo.area = area
		return lo
	}
	// голова шины ресурсов (левый край bus-1) — ближе к добыче первой итерации (zones.md): поиск
	// — кольцами вокруг неё, (x0, y0) — левый нижний угол bus-1
	ax, ay := b.mineAnchor()
	var best *layered
	for r := 0; r*8 <= b.MaxR; r++ {
		if best != nil && float64(r*8) > best.dist+16 {
			break
		}
		for _, o := range ringOffsets(r) {
			x0, y0 := ax+o.X*8, ay+o.Y*8+widths[0]/2
			lo := stack(x0, y0)
			if lo == nil {
				continue
			}
			hx, hy := float64(lo.buses[0].Min.X), float64(lo.buses[0].Min.Y+lo.buses[0].Max.Y)/2
			lo.dist = math.Hypot(hx-float64(ax), hy-float64(ay))
			if d := float64(ax) - hx; d > 0 { // добыча правее головы — подводы в обход базы к левому краю
				lo.dist += 2 * d
			}
			if best == nil || lo.dist < best.dist {
				best = lo
			}
		}
	}
	return best
}

// nearMine — r ближе mineGap к зоне добычи.
func (b *Base) nearMine(r Rect) bool {
	for _, z := range b.mineZones {
		if r.Grow(mineGap).Overlaps(z.Rect) {
			return true
		}
	}
	return false
}

// mineAnchor — куда тянуть голову шины ресурсов: среднее центров ближних к центру базы зон
// добычи угля, железа и меди (добыча первой итерации); зон нет — центр базы.
func (b *Base) mineAnchor() (int, int) {
	ox, oy := b.Origin.X, b.Origin.Y
	sx, sy, n := 0.0, 0.0, 0
	for _, res := range []string{"coal", "iron-ore", "copper-ore"} {
		bd, bx, by := math.Inf(1), 0.0, 0.0
		for _, z := range b.mineZones {
			if z.Resources[res] == 0 {
				continue
			}
			cx, cy := z.Rect.Center()
			if d := math.Hypot(cx-float64(ox), cy-float64(oy)); d < bd {
				bd, bx, by = d, cx, cy
			}
		}
		if !math.IsInf(bd, 1) {
			sx, sy, n = sx+bx, sy+by, n+1
		}
	}
	if n == 0 {
		return ox, oy
	}
	return int(math.Round(sx / float64(n))), int(math.Round(sy / float64(n)))
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
