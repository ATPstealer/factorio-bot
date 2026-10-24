package cluster

import (
	"fmt"
	"math"
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
	ZoneFinal      ZoneKind = "final"
	ZoneLab        ZoneKind = "lab" // верхний этаж: лаборатории и ракетная шахта
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
	kinds := map[int]ZoneKind{1: ZoneProcessing, 2: ZoneSupply, 3: ZoneFinal, 4: ZoneLab}
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

// MineZones — зоны добычи по месторождениям (zones.md): на каждый ресурс need (сколько нужно
// за игру) — пятна от ближних ко входу bus-1 (MineFrom; нет — к центру базы), пока запаса хватит; зона — пятно целиком;
// пересекающиеся — в одну, соседние (до patchNear) — в одну, только если плотно (ресурса не
// меньше minOre площади). Машины и выход добычи (кластеры добычи прикидки) — по зонам
// пропорционально запасу. Нужно до PlaceLayered (станция — по зонам угля).
func (b *Base) MineZones(cs []*Cluster, need map[string]float64) []Zone {
	g := b.grid()
	ox, oy := float64(b.Origin.X), float64(b.Origin.Y)
	if b.MineFrom != nil { // ближние ко входу bus-1 (zones.md)
		ox, oy = float64(b.MineFrom.X), float64(b.MineFrom.Y)
	}
	pts := b.W.Patches()
	sort.Slice(pts, func(i, j int) bool {
		return math.Hypot(pts[i].Center.X-ox, pts[i].Center.Y-oy) < math.Hypot(pts[j].Center.X-ox, pts[j].Center.Y-oy)
	})
	var rs []Rect
	for it, n := range need {
		have := 0.0
		for _, pt := range pts {
			if have >= n {
				break
			}
			if pt.Name == it {
				rs = append(rs, Rect{pt.Min, world.Tile{X: pt.Max.X + 1, Y: pt.Max.Y + 1}})
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
