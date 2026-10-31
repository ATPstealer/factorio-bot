package stage

import (
	"container/heap"
	"context"
	"fmt"
	"maps"
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
	// пролёт и площадь — малого столба (сеть — из малых; средний в цепочке с малым достаёт не
	// дальше малого), имя нового столба — по уровню стадии (tier: с 43-й — средний)
	const pole = "small-electric-pole"
	if _, err := h.World.Sync(h.B, false); err != nil {
		return nil, err
	}
	reach := h.Protos.Entities[pole].MaxWireDistance
	all := layout.New(h.Protos, h.World)
	planPole := map[world.Tile]layout.Placement{}
	for _, pl := range plan.Layout {
		all.Add(pl)
		if h.Protos.Entities[pl.Name].Type == "electric-pole" && pl.Tag == cluster.LogisticsTag {
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
	// и полный план (синяя и фиолетовая; все планы — до стройки): столбы ранних стадий вставали
	// на место будущих кластеров и лент (двигатель power-2 на столбе — 954176679, на бумаге)
	if full, err := LoadPurplePlan(); err == nil {
		for _, pl := range full.Layout {
			if all.At(world.TileOf(pl.Pos)) == nil {
				all.Add(pl)
			}
		}
		for _, c := range full.Clusters {
			if c.Kind != cluster.Bus && !slices.Contains(allow, c.Name) {
				areas = append(areas, c.Area)
			}
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
					pl = layout.Placement{Name: tier(pole), Pos: st.t.Center(), Tag: "poles"}
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
	var net []bridge.Pos
	if paper != nil { // на бумаге: сеть — столбы прошлых стадий (paper.go)
		for _, pl := range paper.list {
			if h.Protos.Entities[pl.Name].Type == "electric-pole" {
				net = append(net, pl.Pos)
			}
		}
	} else {
		p, err := h.B.Player()
		if err != nil {
			return nil, err
		}
		built, err := h.B.FindNearest(bridge.FindArgs{Type: "electric-pole", Radius: 1000, Position: &p.Position, Limit: 1000})
		if err != nil {
			return nil, err
		}
		for _, e := range built {
			net = append(net, e.Position)
		}
	}
	return connectNet(h, plan, list, allow, net)
}

// connectNet — connectPower к сети net (столбы, уже в ней).
func connectNet(h *act.Hands, plan *ClusterPlan, list []layout.Placement, allow []string, net []bridge.Pos) ([]layout.Placement, error) {
	for _, o := range plan.Clusters {
		if o.Kind == cluster.Bus || o.Kind == cluster.Power {
			allow = append(allow, o.Name)
		}
	}
	// пролёт — малого столба (сеть из малых; средние стадий фиолетовой с малым достают не дальше)
	const pole = "small-electric-pole"
	reach := h.Protos.Entities[pole].MaxWireDistance
	isPole := func(name string) bool { return h.Protos.Entities[name].Type == "electric-pole" }
	net = slices.Clone(net)
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
		if isPole(pl.Name) {
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
		if !h.Protos.Entities[pl.Name].Electric || isPole(pl.Name) {
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

// healPower — сеть базы одна (stages.md, общее): свои столбы вне сети станции (острова — обрыв
// после перестановки столбов на обслуживании) подключить цепочкой столбов; сеть — только
// столбы станции (connectPower считает сетью любой стоящий столб — остров «в сети»).
func healPower(ctx context.Context, h *act.Hands) error {
	plan, err := LoadPlan()
	if err != nil {
		return nil // плана нет — нечего
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	engines, err := h.B.FindNearest(bridge.FindArgs{Name: "steam-engine", Radius: 1000, Position: &p.Position, Limit: 1})
	if err != nil || len(engines) == 0 {
		return err
	}
	eng, err := h.B.GetEntity("steam-engine", engines[0].Position)
	if err != nil || eng == nil || eng.Network == 0 {
		return err
	}
	poles, err := h.B.FindNearest(bridge.FindArgs{Type: "electric-pole", Radius: 1000, Position: &p.Position, Limit: 5000})
	if err != nil {
		return err
	}
	var net []bridge.Pos
	islands := map[int][]layout.Placement{}
	for i := 0; i < len(poles); i += auditBatch {
		var refs []bridge.EntityRef
		for _, e := range poles[i:min(len(poles), i+auditBatch)] {
			refs = append(refs, bridge.EntityRef{Name: e.Name, Position: e.Position})
		}
		got, err := h.B.GetEntities(refs)
		if err != nil {
			return err
		}
		for _, st := range got {
			switch {
			case st == nil || !st.Own:
			case st.Network == eng.Network:
				net = append(net, st.Position)
			default:
				islands[st.Network] = append(islands[st.Network], layout.Placement{Name: st.Name, Pos: st.Position})
			}
		}
	}
	if len(islands) == 0 {
		return nil
	}
	// только по коридорам: площади кластеров и места плана — под то, что там потом встанет
	// (решение пользователя 28.10); шина и станции — можно (connectNet)
	var allow []string
	ids := slices.Sorted(maps.Keys(islands))
	var add []layout.Placement
	n := 0
	for _, id := range ids {
		isl := islands[id]
		n += len(isl)
		path, err := connectNet(h, plan, isl, allow, net)
		if err != nil {
			return fmt.Errorf("сеть: остров %d (%d столбов): %w", id, len(isl), err)
		}
		add = append(add, path...)
		for _, q := range append(slices.Clone(isl), path...) {
			net = append(net, q.Pos)
		}
	}
	h.Say("сеть: %d островов (%d столбов) вне сети станции — ставлю %d столбов", len(islands), n, len(add))
	return buildSorted(ctx, h, add, []string{"small-electric-pole", "medium-electric-pole"})
}
