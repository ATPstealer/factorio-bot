package cluster

import (
	"container/heap"
	"fmt"
	"math"
	"slices"
	"sort"

	"factorio-bot/internal/world"
)

// Зоны (zones.md): крупное деление карты под всю базу — шины, добыча, переработка, снабжение,
// конечный продукт, энергия. Вход и выход — пачками (без разбивки по кластерам); кластеры
// проектируются внутри зон потом.

// ZoneKind — вид зоны.
type ZoneKind string

const (
	ZoneBus        ZoneKind = "bus"
	ZoneMine       ZoneKind = "mine"
	ZoneProcessing ZoneKind = "processing"
	ZoneSupply     ZoneKind = "supply"
	ZoneFinal      ZoneKind = "final" // верхний этаж: наука, лаборатории и ракетная шахта
	ZonePower      ZoneKind = "power"
)

// Zone — зона: прямоугольник, вид, вход и выход пачками (шт/с), машины (прикидка), у шины —
// ленты по предметам, у добычи — запас ресурсов в прямоугольнике.
type Zone struct {
	Name      string             `json:"name"`
	Kind      ZoneKind           `json:"kind"`
	Floor     int                `json:"floor,omitempty"` // зона производства: этаж 1–4
	Rect      Rect               `json:"rect"`
	In        []Flow             `json:"in,omitempty"`
	Out       []Flow             `json:"out,omitempty"`
	Machines  map[string]float64 `json:"machines,omitempty"`
	PowerW    float64            `json:"power_w,omitempty"`
	Lanes     map[string]int     `json:"lanes,omitempty"`
	Resources map[string]float64 `json:"resources,omitempty"` // добыча: запас в прямоугольнике
}

func (z Zone) String() string {
	s := fmt.Sprintf("%s [%s] %v", z.Name, z.Kind, z.Rect)
	if len(z.Machines) > 0 {
		var ms []string
		for m, n := range z.Machines {
			ms = append(ms, fmt.Sprintf("%s ×%.0f", m, math.Ceil(n-1e-9)))
		}
		sort.Strings(ms)
		s += fmt.Sprintf(" машины %v", ms)
	}
	if len(z.In) > 0 || len(z.Out) > 0 {
		s += fmt.Sprintf(" in %s out %s", flows(z.In), flows(z.Out))
	}
	if len(z.Lanes) > 0 {
		s += fmt.Sprintf(" лент %v", z.Lanes)
	}
	if len(z.Resources) > 0 {
		s += fmt.Sprintf(" запас %v", z.Resources)
	}
	return s
}

// bundle — вход и выход пачками для набора кластеров: что делается и тратится внутри — не в счёт.
func bundle(cs []*Cluster) (in, out []Flow, machines map[string]float64, power float64) {
	need, made := map[string]float64{}, map[string]float64{}
	machines = map[string]float64{}
	for _, c := range cs {
		for _, f := range c.In {
			need[f.Item] += f.Rate
		}
		for _, f := range c.Out {
			made[f.Item] += f.Rate
		}
		for _, u := range c.Units {
			machines[u.Machine] += u.Machines
		}
		power += c.PowerW
	}
	inm, outm := map[string]float64{}, map[string]float64{}
	for it, v := range need {
		if d := v - made[it]; d > 1e-9 {
			inm[it] = d
		}
	}
	for it, v := range made {
		if d := v - need[it]; d > 1e-9 {
			outm[it] = d
		}
	}
	return toFlows(inm), toFlows(outm), machines, power
}

// Zones — зоны прикидки: шины и зоны производства этажной базы lay, зоны добычи по
// месторождениям кластеров добычи (пятна рядом сливаются в один прямоугольник — можно на
// 2 ресурса; запас — по тайлам внутри), энергия — по станции.
func (b *Base) Zones(lay *Layered) []Zone {
	var res []Zone
	for i, bus := range lay.Buses {
		if bus.Rect.W() > 0 {
			res = append(res, Zone{Name: fmt.Sprintf("bus-%d", i+1), Kind: ZoneBus, Rect: bus.Rect, Lanes: bus.Lanes})
		}
	}
	byFloor := map[int][]*Cluster{}
	for _, c := range b.Clusters {
		if f, ok := lay.Of[c.Name]; ok {
			byFloor[f] = append(byFloor[f], c)
		}
	}
	kinds := map[int]ZoneKind{1: ZoneProcessing, 2: ZoneSupply, 3: ZoneFinal}
	n := map[ZoneKind]int{}
	for f := 1; f < len(lay.Floors); f++ {
		if len(byFloor[f]) == 0 {
			continue
		}
		k := kinds[f]
		for _, r := range lay.Floors[f] { // части этажа — отдельными зонами
			var cs []*Cluster
			for _, c := range byFloor[f] {
				if c.Area.Overlaps(r) {
					cs = append(cs, c)
				}
			}
			n[k]++
			in, out, ms, pw := bundle(cs)
			res = append(res, Zone{Name: fmt.Sprintf("%s-%d", k, n[k]), Kind: k, Floor: f, Rect: r, In: in, Out: out, Machines: ms, PowerW: pw})
		}
	}
	res = append(res, b.mineZones...)
	for _, c := range b.Clusters {
		if c.Kind == Power {
			in, out, ms, pw := bundle([]*Cluster{c})
			res = append(res, Zone{Name: "power-1", Kind: ZonePower, Rect: c.Area, In: in, Out: out, Machines: ms, PowerW: pw})
		}
	}
	return res
}

// landDist — расстояние по суше от origin (zones.md §4): заливка по сетке шага 2 по суше и
// мелководью (вода шире — стена; объекты — деревья, камни, утёсы — не считаются), до limit
// тайлов. Возвращает функцию: расстояние до ближайшего тайла прямоугольника и достижим ли он.
func landDist(w *world.World, origin world.Tile, limit int) func(r Rect) (int, bool) {
	const step = 2
	const _ = 0
	land := func(c cell) bool {
		switch w.TileAt(world.Tile{X: c.x * step, Y: c.y * step}) {
		case world.Land, world.Shallow:
			return true
		}
		return false
	}
	o := cell{origin.X / step, origin.Y / step}
	// Дейкстра по 8 соседям (диагональ — √2): расстояние близко к настоящему, а не
	// манхэттенское (4 соседа тянули к пятнам по прямой на юг вместо юго-западных — 17466282)
	d := map[cell]float64{o: 0}
	done := map[cell]bool{}
	pq := &cellHeap{{o, 0}}
	lim := limit / step
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(cellDist)
		if done[cur.c] {
			continue
		}
		done[cur.c] = true
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				if dx == 0 && dy == 0 {
					continue
				}
				n := cell{cur.c.x + dx, cur.c.y + dy}
				if done[n] || abs(n.x-o.x) > lim || abs(n.y-o.y) > lim || !land(n) {
					continue
				}
				w := 1.0
				if dx != 0 && dy != 0 {
					w = math.Sqrt2
				}
				if v, seen := d[n]; !seen || cur.d+w < v {
					d[n] = cur.d + w
					heap.Push(pq, cellDist{n, cur.d + w})
				}
			}
		}
	}
	return func(r Rect) (int, bool) {
		best, ok := 0.0, false
		for y := r.Min.Y / step; y <= r.Max.Y/step; y++ {
			for x := r.Min.X / step; x <= r.Max.X/step; x++ {
				if v, seen := d[cell{x, y}]; seen && (!ok || v < best) {
					best, ok = v, true
				}
			}
		}
		return int(best * step), ok
	}
}

type cellDist struct {
	c cell
	d float64
}

type cellHeap []cellDist

func (h cellHeap) Len() int           { return len(h) }
func (h cellHeap) Less(i, j int) bool { return h[i].d < h[j].d }
func (h cellHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *cellHeap) Push(x any)        { *h = append(*h, x.(cellDist)) }
func (h *cellHeap) Pop() any          { o := *h; v := o[len(o)-1]; *h = o[:len(o)-1]; return v }

// patchHasPair — на пятне укладывается пара буров ряда блока добычи (zones.md §4): два бура
// через 4 тайла вдоль ряда (как в mineBlock), у каждого под зоной добычи 5×5 больше половины —
// свой ресурс.
func patchHasPair(w *world.World, pt world.Patch) bool {
	own := map[world.Tile]bool{}
	for _, t := range pt.Tiles {
		own[t] = true
	}
	// как mineBlock: под зоной добычи 5×5 больше половины — свой ресурс и ни тайла чужого
	// (OreUnder: mixed), сам бур 3×3 — на суше (17466282: пятно с чужой рудой прошло проверку
	// по числу тайлов, блок не поставил ни одного бура)
	fits := func(cx, cy int) bool {
		n := 0
		for y := cy - 2; y <= cy+2; y++ {
			for x := cx - 2; x <= cx+2; x++ {
				t := world.Tile{X: x, Y: y}
				if own[t] {
					n++
				} else if r, ok := w.ResourceAt(t); ok && r.Name != pt.Name {
					return false
				}
			}
		}
		for y := cy - 1; y <= cy+1; y++ {
			for x := cx - 1; x <= cx+1; x++ {
				if ok, _ := w.Buildable(world.Tile{X: x, Y: y}); !ok {
					return false
				}
			}
		}
		return n > 12
	}
	for _, t := range pt.Tiles {
		if fits(t.X, t.Y) && (fits(t.X+4, t.Y) || fits(t.X, t.Y+4)) {
			return true
		}
	}
	return false
}

// MineZones — зоны добычи по месторождениям (zones.md): на каждый ресурс need (сколько нужно
// за игру) — пятна от ближних к центру базы (Origin; у головы bus-1), пока запаса хватит; зона — пятно целиком;
// пересекающиеся — в одну, соседние (до patchNear) — в одну, только если плотно (ресурса не
// меньше minOre площади). Машины и выход добычи (кластеры добычи прикидки) — по зонам
// пропорционально запасу. Нужно до PlaceLayered (станция — по зонам угля).
func (b *Base) MineZones(cs []*Cluster, need map[string]float64) []Zone {
	g := b.grid()
	ox, oy := float64(b.Origin.X), float64(b.Origin.Y)
	pts := b.W.Patches()
	// ближние — по пути по суше от головы bus-1, не по прямой (zones.md §4, решение
	// пользователя: пятно за озером ленту к шине не проведёт — 436722908); недостижимые —
	// последними и только если достижимых не хватило
	dist := landDist(b.W, b.Origin, 2500)
	far := map[int]int{}
	reach := map[int]bool{}
	for i, pt := range pts {
		r := Rect{pt.Min, world.Tile{X: pt.Max.X + 1, Y: pt.Max.Y + 1}}
		far[i], reach[i] = dist(r)
	}
	idx := make([]int, len(pts))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, c int) bool {
		i, j := idx[a], idx[c]
		if reach[i] != reach[j] {
			return reach[i]
		}
		if reach[i] {
			return far[i] < far[j]
		}
		return math.Hypot(pts[i].Center.X-ox, pts[i].Center.Y-oy) < math.Hypot(pts[j].Center.X-ox, pts[j].Center.Y-oy)
	})
	var rs []Rect
	for it, n := range need {
		have := 0.0
		for _, i := range idx {
			pt := pts[i]
			if have >= n {
				break
			}
			r := Rect{pt.Min, world.Tile{X: pt.Max.X + 1, Y: pt.Max.Y + 1}}
			if slices.ContainsFunc(b.NoMine, r.Overlaps) {
				continue
			}
			// пятно меньше блока добычи (minBlockOre) — не зона: блок на нём не встанет, план
			// кластеров падал «ни одного бура» (17466282: ближайшее к голове bus-1 железо — 6.5k)
			if _, fluid := b.P.Fluids[pt.Name]; !fluid && pt.Amount < minBlockOre {
				continue
			}
			// и пара буров ряда на нём укладывается (решение пользователя; 158846952: уголь 80k
			// в зоне 21×10 — «ни одного бура на coal»)
			if _, fluid := b.P.Fluids[pt.Name]; !fluid && !patchHasPair(b.W, pt) {
				continue
			}
			if !reach[i] && have > 0 {
				continue // недостижимые — только когда достижимых нет совсем
			}
			if pt.Name == it {
				rs = append(rs, r)
				have += float64(pt.Amount)
			}
		}
	}
	ore := func(r Rect) int {
		n := 0
		for _, o := range g.ore {
			k, _ := g.count(o, r)
			n += k
		}
		return n
	}
	for merged := true; merged; {
		merged = false
		for i := 0; i < len(rs) && !merged; i++ {
			for j := i + 1; j < len(rs); j++ {
				u := union(rs[i], rs[j])
				dense := rs[i].Grow(patchNear).Overlaps(rs[j]) && float64(ore(u)) >= minOre*float64(u.W()*u.H())
				if rs[i].Overlaps(rs[j]) || dense {
					rs[i] = u
					rs = append(rs[:j], rs[j+1:]...)
					merged = true
					break
				}
			}
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		ci, cj := rs[i], rs[j]
		xi, yi := ci.Center()
		xj, yj := cj.Center()
		return math.Hypot(xi-ox, yi-oy) < math.Hypot(xj-ox, yj-oy)
	})
	var zs []Zone
	total := map[string]float64{}
	for i, r := range rs {
		r = r.Grow(ZoneBorder)
		z := Zone{Name: fmt.Sprintf("mine-%d", i+1), Kind: ZoneMine, Rect: r, Resources: map[string]float64{}, Machines: map[string]float64{}}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if res, ok := b.W.ResourceAt(world.Tile{X: x, Y: y}); ok {
					z.Resources[res.Name] += float64(res.Amount)
				}
			}
		}
		for it, v := range z.Resources {
			total[it] += v
		}
		zs = append(zs, z)
	}
	// машины и выход добычи — по зонам пропорционально запасу ресурса
	for _, c := range cs {
		if c.Kind != Mine {
			continue
		}
		for i := range zs {
			if total[c.Product] == 0 || zs[i].Resources[c.Product] == 0 {
				continue
			}
			k := zs[i].Resources[c.Product] / total[c.Product]
			for _, u := range c.Units {
				zs[i].Machines[u.Machine] += u.Machines * k
			}
			for _, f := range c.Out {
				zs[i].Out = append(zs[i].Out, Flow{f.Item, f.Rate * k})
			}
			zs[i].PowerW += c.PowerW * k
		}
	}
	b.mineZones = zs
	return zs
}

// MoreMineZones — зоны добычи ресурса it дописаны, пока запаса в них (с уже стоящими блоками)
// меньше want (stages.md, стадия 5: 2M под бурами): пятна от ближних к голове bus-1, зона — пятно
// целиком (+ZoneBorder), только мимо всех зон (части базы не трогаются).
func (b *Base) MoreMineZones(zones []Zone, it string, want float64) []Zone {
	var head world.Tile
	have, n := 0.0, 0
	for _, z := range zones {
		if z.Name == "bus-1" {
			head = z.Rect.Min
		}
		if z.Kind == ZoneMine {
			have += z.Resources[it]
			n++
		}
	}
	pts := b.W.Patches()
	sort.Slice(pts, func(i, j int) bool {
		return math.Hypot(pts[i].Center.X-float64(head.X), pts[i].Center.Y-float64(head.Y)) <
			math.Hypot(pts[j].Center.X-float64(head.X), pts[j].Center.Y-float64(head.Y))
	})
	for _, pt := range pts {
		if have >= want {
			break
		}
		if pt.Name != it {
			continue
		}
		if _, fluid := b.P.Fluids[pt.Name]; !fluid && (pt.Amount < minBlockOre || !patchHasPair(b.W, pt)) {
			continue // меньше блока добычи или пары буров не уложить — не зона (MineZones; 158846952, стадия 3: уголь 80k в 21×10)
		}
		r := Rect{pt.Min, world.Tile{X: pt.Max.X + 1, Y: pt.Max.Y + 1}}.Grow(ZoneBorder)
		if slices.ContainsFunc(zones, func(z Zone) bool { return z.Rect.Overlaps(r) }) || b.avoids(r) {
			continue
		}
		n++
		z := Zone{Name: fmt.Sprintf("mine-%d", n), Kind: ZoneMine, Rect: r, Resources: map[string]float64{}}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if res, ok := b.W.ResourceAt(world.Tile{X: x, Y: y}); ok {
					z.Resources[res.Name] += float64(res.Amount)
				}
			}
		}
		have += z.Resources[it]
		zones = append(zones, z)
		b.logf("зоны: %v", z)
	}
	return zones
}

// avoids — в рамке r есть тайл Avoid (построенное по плану вне кластеров: подводы, ленты шин).
func (b *Base) avoids(r Rect) bool {
	for t := range b.Avoid {
		if t.X >= r.Min.X && t.X < r.Max.X && t.Y >= r.Min.Y && t.Y < r.Max.Y {
			return true
		}
	}
	return false
}

// patchNear — соседние пятна (до стольких тайлов) — кандидаты в одну зону добычи; minOre —
// доля тайлов с ресурсом в прямоугольнике объединённой зоны, не меньше (zones.md: без ресурсов
// не больше 30%).
const (
	patchNear = 30
	minOre    = 0.7
)

func union(a, b Rect) Rect {
	return Rect{world.Tile{X: min(a.Min.X, b.Min.X), Y: min(a.Min.Y, b.Min.Y)}, world.Tile{X: max(a.Max.X, b.Max.X), Y: max(a.Max.Y, b.Max.Y)}}
}

// cell — ячейка сетки заливки landDist (шаг 2 тайла).
type cell struct{ x, y int }
