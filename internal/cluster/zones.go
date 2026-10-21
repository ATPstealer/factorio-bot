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
	ZonePower      ZoneKind = "power"
)

// Zone — зона: прямоугольник, вид, вход и выход пачками (шт/с), машины (прикидка), у шины —
// ленты по предметам, у добычи — запас ресурсов в прямоугольнике.
type Zone struct {
	Name      string             `json:"name"`
	Kind      ZoneKind           `json:"kind"`
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
	kinds := map[int]ZoneKind{1: ZoneProcessing, 2: ZoneSupply, 3: ZoneFinal, 4: ZoneFinal}
	n := map[ZoneKind]int{}
	for f := 1; f < len(lay.Floors); f++ {
		if lay.Floors[f].W() == 0 || len(byFloor[f]) == 0 {
			continue
		}
		k := kinds[f]
		n[k]++
		in, out, ms, pw := bundle(byFloor[f])
		res = append(res, Zone{Name: fmt.Sprintf("%s-%d", k, n[k]), Kind: k, Rect: lay.Floors[f], In: in, Out: out, Machines: ms, PowerW: pw})
	}
	// добыча: зона на пятно под кластерами добычи (пятно целиком); соседние (до patchNear) —
	// в одну, только если прямоугольник плотный (без ресурсов не больше 30%); пересекающиеся —
	// в одну всегда (zones.md)
	type mz struct {
		r  Rect
		cs []*Cluster
	}
	g := b.grid()
	ore := func(r Rect) int {
		n := 0
		for _, o := range g.ore {
			k, _ := g.count(o, r)
			n += k
		}
		return n
	}
	var mines []mz
	for _, pt := range b.W.Patches() {
		pr := Rect{pt.Min, world.Tile{X: pt.Max.X + 1, Y: pt.Max.Y + 1}}
		var cs []*Cluster
		for _, c := range b.Clusters {
			if c.Kind == Mine && c.Product == pt.Name && pr.Overlaps(c.Area) {
				cs = append(cs, c)
			}
		}
		if len(cs) > 0 {
			mines = append(mines, mz{pr, cs})
		}
	}
	for merged := true; merged; {
		merged = false
		for i := 0; i < len(mines) && !merged; i++ {
			for j := i + 1; j < len(mines); j++ {
				u := union(mines[i].r, mines[j].r)
				dense := mines[i].r.Grow(patchNear).Overlaps(mines[j].r) && float64(ore(u)) >= minOre*float64(u.W()*u.H())
				if mines[i].r.Overlaps(mines[j].r) || dense {
					mines[i] = mz{u, append(mines[i].cs, mines[j].cs...)}
					mines = append(mines[:j], mines[j+1:]...)
					merged = true
					break
				}
			}
		}
	}
	for i := range mines {
		mines[i].r = mines[i].r.Grow(ZoneBorder)
	}
	for i, m := range mines {
		in, out, ms, pw := bundle(m.cs)
		z := Zone{Name: fmt.Sprintf("mine-%d", i+1), Kind: ZoneMine, Rect: m.r, In: in, Out: out, Machines: ms, PowerW: pw,
			Resources: map[string]float64{}}
		for y := m.r.Min.Y; y < m.r.Max.Y; y++ {
			for x := m.r.Min.X; x < m.r.Max.X; x++ {
				if r, ok := b.W.ResourceAt(world.Tile{X: x, Y: y}); ok {
					z.Resources[r.Name] += float64(r.Amount)
				}
			}
		}
		res = append(res, z)
	}
	for _, c := range b.Clusters {
		if c.Kind == Power {
			in, out, ms, pw := bundle([]*Cluster{c})
			res = append(res, Zone{Name: "power-1", Kind: ZonePower, Rect: c.Area, In: in, Out: out, Machines: ms, PowerW: pw})
		}
	}
	return res
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
