package stage

import (
	"container/heap"
	"fmt"
	"math"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// corridorPoles — цепочка столбов от сети (start — стоящие столбы) до тайла, где столб
// запитает цель (target), только по коридорам: не в площадях кластеров плана, кроме allow
// (станция, сам кластер-цель); столбы — ровными линиями (по горизонтали и вертикали); extra — что ещё будет стоять (своё стадии вне плана). Места столбов плана с тегом логистики — дешевле (их всё равно
// ставить); прочие занятые планом тайлы — нельзя. Кратчайшее по числу столбов (Дейкстра).
func corridorPoles(h *act.Hands, plan *ClusterPlan, extra []layout.Placement, start []bridge.Pos, allow []string, target func(pole bridge.Pos) bool) ([]layout.Placement, error) {
	const pole = "small-electric-pole"
	if _, err := h.World.Sync(h.B, false); err != nil {
		return nil, err
	}
	reach := h.Protos.Entities[pole].MaxWireDistance
	all := layout.New(h.Protos, h.World)
	planPole := map[world.Tile]layout.Placement{}
	for _, pl := range plan.Layout {
		all.Add(pl)
		if pl.Name == pole && pl.Tag == cluster.LogisticsTag {
			planPole[world.TileOf(pl.Pos)] = pl
		}
	}
	for _, pl := range extra { // своё стадии вне плана (буферы, столбы)
		if all.At(world.TileOf(pl.Pos)) == nil {
			all.Add(pl)
		}
		delete(planPole, world.TileOf(pl.Pos))
	}
	var areas []cluster.Rect
	for _, c := range plan.Clusters {
		if !slices.Contains(allow, c.Name) {
			areas = append(areas, c.Area)
		}
	}
	inArea := func(t world.Tile) bool {
		for _, a := range areas {
			if t.X >= a.Min.X && t.X < a.Max.X && t.Y >= a.Min.Y && t.Y < a.Max.Y {
				return true
			}
		}
		return false
	}
	// где можно ставить столб и почём
	cost := func(t world.Tile) (float64, bool) {
		if _, ok := planPole[t]; ok {
			return 0.5, true
		}
		if inArea(t) || !all.Free(t, layout.AnyLand) {
			return 0, false
		}
		return 1, true
	}
	// состояние — тайл столба и направление пролёта к нему (-1 — стоящий столб сети): столбы
	// ровно — следующий строго по горизонтали или вертикали, поворот дороже (правило «ровно»)
	type pstate struct {
		t world.Tile
		d int
	}
	dirs := []world.Tile{{X: 0, Y: -1}, {X: 1, Y: 0}, {X: 0, Y: 1}, {X: -1, Y: 0}}
	dist := map[pstate]float64{}
	prev := map[pstate]pstate{}
	var pq pheap
	for _, p := range start {
		st := pstate{world.TileOf(p), -1}
		dist[st] = 0
		heap.Push(&pq, &pnode{t: st.t, dir: -1})
	}
	r := int(math.Floor(reach))
	for pq.Len() > 0 {
		n := heap.Pop(&pq).(*pnode)
		cur := pstate{n.t, n.dir}
		if n.d > dist[cur] {
			continue
		}
		if cur.d >= 0 && target(n.t.Center()) {
			var res []layout.Placement
			for st := cur; st.d >= 0; st = prev[st] {
				pl, ok := planPole[st.t]
				if !ok {
					pl = layout.Placement{Name: pole, Pos: st.t.Center(), Tag: "poles"}
				}
				res = append(res, pl)
			}
			slices.Reverse(res)
			return res, nil
		}
		if n.d > 400 {
			break
		}
		for d, dv := range dirs {
			for k := 1; k <= r; k++ {
				v := world.Tile{X: n.t.X + dv.X*k, Y: n.t.Y + dv.Y*k}
				c, ok := cost(v)
				if !ok {
					continue
				}
				nd := n.d + c + 1e-3*float64(r-k) // при равном числе столбов — длиннее пролёт
				if cur.d >= 0 && d != cur.d {
					nd += 0.6 // поворот линии
				}
				ns := pstate{v, d}
				if old, seen := dist[ns]; seen && old <= nd {
					continue
				}
				dist[ns], prev[ns] = nd, cur
				heap.Push(&pq, &pnode{t: v, dir: d, d: nd})
			}
		}
	}
	return nil, fmt.Errorf("столбами по коридорам от сети не дотянуться")
}

type pnode struct {
	t   world.Tile
	dir int
	d   float64
	i   int
}

type pheap []*pnode

func (h pheap) Len() int           { return len(h) }
func (h pheap) Less(i, j int) bool { return h[i].d < h[j].d }
func (h pheap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].i = i; h[j].i = j }
func (h *pheap) Push(x any)        { n := x.(*pnode); n.i = len(*h); *h = append(*h, n) }
func (h *pheap) Pop() any          { o := *h; n := o[len(o)-1]; *h = o[:len(o)-1]; return n }

// powers — столб в pole питает постройку pl (рамка коллизии задевает зону питания).
func powers(p *act.Hands, pole bridge.Pos, pl layout.Placement) bool {
	r := p.Protos.Entities["small-electric-pole"].SupplyAreaDistance
	b := world.RotateBox(p.Protos.Entities[pl.Name].CollisionBox, pl.Dir)
	return pl.Pos.X+b.LeftTop.X < pole.X+r && pl.Pos.X+b.RightBottom.X > pole.X-r &&
		pl.Pos.Y+b.LeftTop.Y < pole.Y+r && pl.Pos.Y+b.RightBottom.Y > pole.Y-r
}

// connectPower — столбы, чтобы всё электрическое из list было в сети: стоящие свои столбы —
// сеть; группы столбов list, не достающие до сети, — цепочкой по коридорам к ней; что
// после этого не запитано — своим столбом по коридорам. allow — свои кластеры, через площадь
// которых можно идти (шины и станция — всегда).
func connectPower(h *act.Hands, plan *ClusterPlan, list []layout.Placement, allow []string) ([]layout.Placement, error) {
	for _, o := range plan.Clusters {
		if o.Kind == cluster.Bus || o.Kind == cluster.Power {
			allow = append(allow, o.Name)
		}
	}
	const pole = "small-electric-pole"
	reach := h.Protos.Entities[pole].MaxWireDistance
	p, err := h.B.Player()
	if err != nil {
		return nil, err
	}
	built, err := h.B.FindNearest(bridge.FindArgs{Type: "electric-pole", Radius: 1000, Position: &p.Position, Limit: 1000})
	if err != nil {
		return nil, err
	}
	var net []bridge.Pos
	for _, e := range built {
		net = append(net, e.Position)
	}
	near := func(a bridge.Pos, set []bridge.Pos) bool {
		for _, b := range set {
			if math.Hypot(a.X-b.X, a.Y-b.Y) <= reach {
				return true
			}
		}
		return false
	}
	var own []bridge.Pos
	for _, pl := range list {
		if pl.Name == pole {
			own = append(own, pl.Pos)
		}
	}
	var added []layout.Placement
	// группы своих столбов: присоединённые к сети — растят её
	for left := own; len(left) > 0; {
		grew := true
		for grew {
			grew = false
			var rest []bridge.Pos
			for _, q := range left {
				if near(q, net) {
					net, grew = append(net, q), true
				} else {
					rest = append(rest, q)
				}
			}
			left = rest
		}
		if len(left) == 0 {
			break
		}
		group := left
		path, err := corridorPoles(h, plan, append(slices.Clone(list), added...), net, allow, func(t bridge.Pos) bool { return near(t, group) })
		if err != nil {
			return nil, fmt.Errorf("столбы (%.1f, %.1f) к сети: %w", group[0].X, group[0].Y, err)
		}
		for _, pl := range path {
			added = append(added, pl)
			net = append(net, pl.Pos)
		}
	}
	for _, pl := range list {
		if !h.Protos.Entities[pl.Name].Electric || pl.Name == pole {
			continue
		}
		ok := false
		for _, q := range net {
			ok = ok || powers(h, q, pl)
		}
		if ok {
			continue
		}
		path, err := corridorPoles(h, plan, append(slices.Clone(list), added...), net, allow, func(t bridge.Pos) bool { return powers(h, t, pl) })
		if err != nil {
			return nil, fmt.Errorf("%s в (%.1f, %.1f) запитать: %w", pl.Name, pl.Pos.X, pl.Pos.Y, err)
		}
		for _, q := range path {
			added = append(added, q)
			net = append(net, q.Pos)
		}
	}
	return added, nil
}
