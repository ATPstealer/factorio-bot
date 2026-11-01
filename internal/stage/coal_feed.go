package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Уголь станции не кончается (stages.md, общее; решение пользователя 28.10): у станций своя
// добыча угля, её пятно вырабатывается — бойлеры без угля, база без тока (электрические буры
// второй добычи тоже встают: стадии 31–33 на 859088572 и 260647970). Голодает — отвод делителем
// с подвода угля другой добычи в шину в начало ленты угля станции (боковой подвод): ленте ток не
// нужен, уголь на подводе запускает станцию. Раскладка — один раз на карту (coalFeedFile).

const (
	coalFeedFile = "state-coal-feed.json"
	coalFeedTag  = "coal-feed"
)

// healCoal — подвод угля станции, если она голодает (четверть бойлеров без угля или половина
// буров её добычи на выработанном); подвод уже посчитан — достроить недостающее. Подвод старого
// вида (боком в ленту станции — уголь на одну полосу) — отвод поднять и проложить заново от того
// же делителя, во вход делителя на ленте станции.
func healCoal(ctx context.Context, h *act.Hands) error {
	plan, err := LoadPlan()
	if err != nil {
		return nil // плана нет — нечего
	}
	var list []layout.Placement
	if data, err := os.ReadFile(runPath(coalFeedFile)); err == nil && json.Unmarshal(data, &list) == nil && len(list) > 0 {
		// подвод есть, а станция голодает и на подводе пусто — его источник тоже выработан
		// (267145981, стадия 43: подвод с ленты coal-2, рядом полные ленты другой добычи, все 44
		// бойлера без угля, база без тока): подвод снять и проложить заново с ленты, где уголь
		hungry, why, err := coalHungry(h, plan)
		if err != nil {
			return err
		}
		dry, err := coalFeedDry(h, list)
		if err != nil {
			return err
		}
		// источник подвода — выработанная добыча (на лентах ещё остатки, но они кончатся:
		// 267145981, третий заход) — тоже заново, если есть живая
		dead := false
		if t, ok := coalFeedSourceTile(h, plan, list); ok {
			at, pair := beltTiles(plan.Layout)
			if alive, err := laneAliveFn(h, plan, at, pair); err == nil && !alive(t) {
				dead = true
			}
		}
		if hungry && (dry || dead) {
			h.Say("уголь станции: %s, подвод угля другой добычи пуст или с выработанной добычи — прокладываю заново", why)
			var es []bridge.Entity
			for _, pl := range list {
				if e, err := h.B.EntityAt(pl.Name, pl.Pos); err == nil && e != nil {
					es = append(es, *e)
				}
			}
			if err := h.PickupAll(ctx, es); err != nil {
				return err
			}
			if list, err = coalFeed(h, plan, nil); err != nil {
				return err
			}
			if err := writeState(coalFeedFile, list); err != nil {
				return err
			}
		} else if list, err = coalFeedJoin(ctx, h, plan, list); err != nil {
			return err
		}
	} else {
		hungry, why, err := coalHungry(h, plan)
		if err != nil || !hungry {
			return err
		}
		h.Say("уголь станции: %s — подвожу уголь другой добычи", why)
		if list, err = coalFeed(h, plan, nil); err != nil {
			return err
		}
		if err := writeState(coalFeedFile, list); err != nil {
			return err
		}
	}
	left, err := notBuilt(h, list)
	if err != nil || len(left) == 0 {
		return err
	}
	var belts, splitters []layout.Placement
	for _, pl := range left {
		if pl.Name == "splitter" {
			splitters = append(splitters, pl)
		} else {
			belts = append(belts, pl)
		}
	}
	// сначала отвод, делители — последними: встают поверх лент подвода и станции сами (поднимать
	// ленту под делителем — подход к ней среди лент: «не сойти с ленты», стадия 32 на 260647970)
	if err := buildSorted(ctx, h, belts, []string{"transport-belt", "underground-belt"}); err != nil {
		return err
	}
	return buildSorted(ctx, h, splitters, []string{"splitter"})
}

// coalFeedDry — на лентах подвода list нет угля (стоящих; не стоит ни одной — не пуст: нечего
// судить, подвод достроится).
func coalFeedDry(h *act.Hands, list []layout.Placement) (bool, error) {
	var belts []layout.Placement
	for _, pl := range list {
		if strings.HasSuffix(pl.Name, "transport-belt") {
			belts = append(belts, pl)
		}
	}
	states, err := entityStates(h, belts)
	if err != nil {
		return false, err
	}
	built := 0
	for _, st := range states {
		if st == nil {
			continue
		}
		built++
		if st.Belt["coal"] > 0 {
			return false, nil
		}
	}
	return built > 0, nil
}

// coalMines — добыча угля не станции (coalMine) и из неё живая (alive): хоть половина стоящих
// буров на руде — иначе на подводе лишь остатки угля, застрявшие перед полной шиной
// (267145981: подвод переложили на ту же выработанную ленту).
func coalMines(h *act.Hands, plan *ClusterPlan) (coalMine, alive map[string]bool, err error) {
	mines := stationMines(plan)
	coalMine = map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Mine && c.Product == "coal" && !mines[c.Name] {
			coalMine[c.Name] = true
		}
	}
	var drills []layout.Placement
	for _, pl := range plan.Layout {
		if h.Protos.Entities[pl.Name].Type == "mining-drill" && coalMine[strings.TrimPrefix(pl.Tag, "c/")] {
			drills = append(drills, pl)
		}
	}
	states, err := entityStates(h, drills)
	if err != nil {
		return nil, nil, err
	}
	total, gone := map[string]int{}, map[string]int{}
	for i, st := range states {
		if st == nil {
			continue
		}
		name := strings.TrimPrefix(drills[i].Tag, "c/")
		total[name]++
		if st.Status == "no_minable_resources" {
			gone[name]++
		}
	}
	alive = map[string]bool{}
	for name := range coalMine {
		if total[name] > 0 && gone[name]*2 < total[name] {
			alive[name] = true
		}
	}
	return coalMine, alive, nil
}

// laneAliveFn — жива ли лента (тайл t): буры, что кормят её (вверх по лентам плана до
// рядов блока), хоть наполовину на руде. У блока две ленты — выработана бывает одна (267145981:
// подвод клали на первую ленту coal-2, вторая рядом полная). Состояние буров — один раз.
func laneAliveFn(h *act.Hands, plan *ClusterPlan, at map[world.Tile]layout.Placement, pair map[world.Tile]world.Tile) (func(t world.Tile) bool, error) {
	var drills []layout.Placement
	for _, pl := range plan.Layout {
		if h.Protos.Entities[pl.Name].Type == "mining-drill" {
			drills = append(drills, pl)
		}
	}
	states, err := entityStates(h, drills)
	if err != nil {
		return nil, err
	}
	type cnt struct{ total, gone int }
	byOut := map[world.Tile]cnt{}
	for i, st := range states {
		if st == nil {
			continue
		}
		o := layout.DrillOutput(h.Protos, drills[i])
		c := byOut[o]
		c.total++
		if st.Status == "no_minable_resources" {
			c.gone++
		}
		byOut[o] = c
	}
	return func(t world.Tile) bool {
		total, gone := 0, 0
		for u := range upstream(at, pair, t) {
			total += byOut[u].total
			gone += byOut[u].gone
		}
		return total > 0 && gone*2 < total
	}, nil
}

// coalFeedSourceTile — тайл ленты плана под делителем-источником подвода станции list; false —
// не найти.
func coalFeedSourceTile(h *act.Hands, plan *ClusterPlan, list []layout.Placement) (world.Tile, bool) {
	at, _ := beltTiles(plan.Layout)
	for _, pl := range list {
		if !strings.HasSuffix(pl.Name, "splitter") {
			continue
		}
		for _, t := range layout.Tiles(h.Protos, pl) {
			if q, ok := at[t]; ok && strings.HasPrefix(q.Tag, cluster.LogisticsTag+"/") {
				return t, true
			}
		}
	}
	return world.Tile{}, false
}

// coalFeedJoin — подвод list старого вида (один делитель — у источника, в ленту станции боком):
// отвод поднять, проложить заново от того же делителя во вход делителя на ленте станции; новый —
// как есть.
func coalFeedJoin(ctx context.Context, h *act.Hands, plan *ClusterPlan, list []layout.Placement) ([]layout.Placement, error) {
	var src *layout.Placement
	n := 0
	for i, pl := range list {
		if pl.Name == "splitter" {
			n++
			src = &list[i]
		}
	}
	if n != 1 {
		return list, nil
	}
	h.Say("уголь станции: подвод боком (уголь на одну полосу) — перекладываю во вход делителя")
	var es []bridge.Entity
	for _, pl := range list {
		if pl.Name == "splitter" {
			continue
		}
		if e, err := h.B.EntityAt(pl.Name, pl.Pos); err == nil && e != nil {
			es = append(es, *e)
		}
	}
	if err := h.PickupAll(ctx, es); err != nil {
		return nil, err
	}
	res, err := coalFeed(h, plan, src)
	if err != nil {
		return nil, err
	}
	return res, writeState(coalFeedFile, res)
}

// stationTags — теги построек станций плана (ряд и логистика).
func stationTags(plan *ClusterPlan) (row, feed map[string]bool) {
	row, feed = map[string]bool{}, map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Power {
			row[cluster.Tag(c.Name)], feed[cluster.LogisticsTag+"/"+c.Name] = true, true
		}
	}
	return row, feed
}

// stationMines — добыча угля, чьи ленты входят в подачу станции (ленты кластера вплотную к
// лентам логистики станции).
func stationMines(plan *ClusterPlan) map[string]bool {
	_, feed := stationTags(plan)
	near := map[world.Tile]bool{}
	for _, pl := range plan.Layout {
		if feed[pl.Tag] && isBelt(pl) {
			t := world.TileOf(pl.Pos)
			for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
				near[layout.Step(t, d)] = true
			}
		}
	}
	res := map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Mine || c.Product != "coal" {
			continue
		}
		for _, pl := range plan.Layout {
			if pl.Tag == cluster.Tag(c.Name) && isBelt(pl) && near[world.TileOf(pl.Pos)] {
				res[c.Name] = true
				break
			}
		}
	}
	return res
}

// coalHungry — станция голодает: четверть стоящих бойлеров и больше без угля или половина буров
// её добычи на выработанном.
func coalHungry(h *act.Hands, plan *ClusterPlan) (bool, string, error) {
	row, _ := stationTags(plan)
	mines := stationMines(plan)
	var boilers, drills []layout.Placement
	for _, pl := range plan.Layout {
		switch {
		case pl.Name == "boiler" && row[pl.Tag]:
			boilers = append(boilers, pl)
		case h.Protos.Entities[pl.Name].Type == "mining-drill" && mines[strings.TrimPrefix(pl.Tag, "c/")]:
			drills = append(drills, pl)
		}
	}
	states, err := entityStates(h, append(slices.Clone(boilers), drills...))
	if err != nil {
		return false, "", err
	}
	built, empty := 0, 0
	for _, st := range states[:len(boilers)] {
		if st == nil {
			continue
		}
		built++
		coal := 0
		for _, inv := range st.Inventories {
			coal += inv["coal"]
		}
		if coal == 0 {
			empty++
		}
	}
	if built > 0 && empty*4 >= built {
		return true, fmt.Sprintf("без угля %d бойлеров из %d", empty, built), nil
	}
	n, gone := 0, 0
	for _, st := range states[len(boilers):] {
		if st == nil {
			continue
		}
		n++
		if st.Status == "no_minable_resources" {
			gone++
		}
	}
	if n > 0 && gone*2 >= n {
		return true, fmt.Sprintf("добыча станции выработана (%d буров из %d)", gone, n), nil
	}
	return false, "", nil
}

// downstream — тайлы лент at, куда уходит предмет с тайла t: по ходу ленты, через подземки
// (вход — к своему выходу) и делители (в оба выхода).
func downstream(at map[world.Tile]layout.Placement, pair map[world.Tile]world.Tile, t world.Tile) map[world.Tile]bool {
	seen := map[world.Tile]bool{}
	queue := []world.Tile{t}
	for len(queue) > 0 && len(seen) < 20000 {
		t := queue[0]
		queue = queue[1:]
		pl, ok := at[t]
		if seen[t] || !ok || !isBelt(pl) {
			continue
		}
		seen[t] = true
		next := []world.Tile{layout.Step(t, pl.Dir)}
		if o, ok := pair[t]; ok {
			next = append(next, layout.Step(o, pl.Dir))
		}
		if strings.HasSuffix(pl.Name, "underground-belt") && pl.Recipe == "input" {
			next = nil
			b := t
			for k := 0; k < 10; k++ {
				b = layout.Step(b, pl.Dir)
				if q, ok := at[b]; ok && strings.HasSuffix(q.Name, "underground-belt") && q.Recipe == "output" && q.Dir == pl.Dir {
					next = []world.Tile{b}
					break
				}
			}
		}
		for _, n := range next {
			if q, ok := at[n]; ok && q.Dir != (pl.Dir+8)%16 {
				queue = append(queue, n)
			}
		}
	}
	return seen
}

// straight — на тайле t прямая лента: позади — лента того же хода, что в неё входит.
func straight(at map[world.Tile]layout.Placement, t world.Tile) bool {
	pl, ok := at[t]
	if !ok || pl.Name != "transport-belt" {
		return false
	}
	b, ok := at[layout.Step(t, (pl.Dir+8)%16)]
	return ok && b.Name == "transport-belt" && b.Dir == pl.Dir
}

// coalFeed — раскладка подвода: делитель на прямой ленте подвода угля другой добычи (второй
// тайл — сбоку, свободен; keep — этот, уже стоящий) и лента от его второго выхода во второй вход
// делителя на начале ленты угля станции (лента, с которой уголь доходит до большего числа лент
// ряда станций): уголь — на обе полосы (боком — только на одну).
func coalFeed(h *act.Hands, plan *ClusterPlan, keep *layout.Placement) ([]layout.Placement, error) {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return nil, err
	}
	at, pair := beltTiles(plan.Layout)
	row, feed := stationTags(plan)
	coalMine, _, err := coalMines(h, plan)
	if err != nil {
		return nil, err
	}
	laneOK, err := laneAliveFn(h, plan, at, pair)
	if err != nil {
		return nil, err
	}
	// стоит ли в игре
	var belts []layout.Placement
	for _, pl := range plan.Layout {
		if pl.Name == "transport-belt" {
			belts = append(belts, pl)
		}
	}
	states, err := entityStates(h, belts)
	if err != nil {
		return nil, err
	}
	built := map[world.Tile]bool{}
	coal := map[world.Tile]bool{} // на ленте лежит уголь
	for i, st := range states {
		if st != nil {
			t := world.TileOf(belts[i].Pos)
			built[t], coal[t] = true, st.Belt["coal"] > 0
		}
	}
	// цели: ленты подачи станций — с наибольшим охватом ряда
	type cand struct {
		t   world.Tile
		pl  layout.Placement
		cov int
	}
	var targets []cand
	best := 0
	for _, pl := range belts {
		t := world.TileOf(pl.Pos)
		if !feed[pl.Tag] || !built[t] || !straight(at, t) {
			continue
		}
		cov := 0
		for q := range downstream(at, pair, t) {
			if row[at[q].Tag] {
				cov++
			}
		}
		if cov == 0 {
			continue
		}
		targets = append(targets, cand{t, pl, cov})
		best = max(best, cov)
	}
	targets = slices.DeleteFunc(targets, func(c cand) bool { return c.cov < best })
	if len(targets) == 0 {
		return nil, fmt.Errorf("уголь станции: нет ленты подачи станции в игре")
	}
	// источники: прямые ленты подводов угля другой добычи в шину, на них уголь
	l := layout.New(h.Protos, h.World)
	for _, pl := range plan.Layout {
		l.Add(pl)
	}
	// источники: делитель на прямой ленте подвода угля другой добычи в шину (на ней уголь),
	// второй тайл — сбоку, свободен; keep — уже стоящий
	type source struct {
		s, o world.Tile // тайл ленты подвода, второй тайл делителя
		sp   layout.Placement
	}
	var sources []source
	if keep != nil {
		ts := layout.Tiles(h.Protos, *keep)
		s, o := ts[0], ts[1]
		if _, ok := at[s]; !ok {
			s, o = o, s
		}
		l.Add(*keep)
		sources = append(sources, source{s, o, *keep})
	}
	// сначала ленты живой добычи (laneAliveFn); нет таких — любые с углём (остатки)
	for pass := 0; pass < 2 && len(sources) == 0; pass++ {
		for _, pl := range belts {
			t := world.TileOf(pl.Pos)
			name, ok := strings.CutPrefix(pl.Tag, cluster.LogisticsTag+"/")
			if keep != nil || !ok || !built[t] || !coal[t] || !straight(at, t) {
				continue
			}
			parts := strings.Split(name, "/") // шина/добыча
			if len(parts) != 2 || !coalMine[parts[1]] || pass == 0 && !laneOK(t) {
				continue
			}
			for _, side := range []int{(pl.Dir + 4) % 16, (pl.Dir + 12) % 16} {
				if o := layout.Step(t, side); l.Free(o, layout.AnyLand) {
					sources = append(sources, source{t, o, layout.Placement{Name: "splitter", Dir: pl.Dir, Tag: coalFeedTag,
						Pos: bridge.Pos{X: (float64(t.X+o.X) + 1) / 2, Y: (float64(t.Y+o.Y) + 1) / 2}}})
				}
			}
		}
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("уголь станции: нет подвода угля другой добычи в игре")
	}
	// цели: делитель на ленте станции, второй тайл сбоку — его вход (позади — конец отвода, по
	// ходу), выход перед ним свободен (там ничего не будет: всё — в ленту станции)
	type join struct {
		g, to world.Tile
		sp    layout.Placement
	}
	var joins []join
	for _, g := range targets {
		d := g.pl.Dir
		for _, side := range []int{(d + 4) % 16, (d + 12) % 16} {
			o := layout.Step(g.t, side)
			to := layout.Step(o, (d+8)%16)
			if l.Free(o, layout.AnyLand) && l.Free(layout.Step(o, d), layout.AnyLand) && l.Free(to, layout.AnyLand) {
				joins = append(joins, join{g.t, to, layout.Placement{Name: "splitter", Dir: d, Tag: coalFeedTag,
					Pos: bridge.Pos{X: (float64(g.t.X+o.X) + 1) / 2, Y: (float64(g.t.Y+o.Y) + 1) / 2}}})
			}
		}
	}
	if len(joins) == 0 {
		return nil, fmt.Errorf("уголь станции: на ленте станции нет места под делитель входа")
	}
	type try struct {
		s source
		j join
		d int
	}
	var tries []try
	for _, s := range sources {
		for _, j := range joins {
			tries = append(tries, try{s, j, abs(s.o.X-j.to.X) + abs(s.o.Y-j.to.Y)})
		}
	}
	sort.Slice(tries, func(i, j int) bool { return tries[i].d < tries[j].d })
	for k, tr := range tries {
		if k >= 60 {
			break
		}
		d := tr.s.sp.Dir
		mark := l.Mark()
		if keep == nil {
			l.Add(tr.s.sp)
		}
		l.Add(tr.j.sp)
		if _, err := l.RouteBelt(layout.Step(tr.s.o, d), d, tr.j.to, tr.j.sp.Dir, coalFeedTag); err == nil {
			res := slices.Clone(l.List[mark:])
			if keep != nil {
				res = append([]layout.Placement{*keep}, res...)
			}
			h.Log("уголь станции: делитель в (%.1f, %.1f), отвод %d построек до делителя в (%.1f, %.1f)", tr.s.sp.Pos.X, tr.s.sp.Pos.Y, len(res)-2, tr.j.sp.Pos.X, tr.j.sp.Pos.Y)
			return res, nil
		}
		l.Rollback(mark)
	}
	return nil, fmt.Errorf("уголь станции: отвод не проложить (источников %d, целей %d)", len(sources), len(targets))
}

// healFluids — в цистернах складов жидкость не по плану (вода в нефти: поворот подземных труб
// воды рядом с трубой нефти — стадия 35 на 1028292478) — слить всю сеть труб кнопкой в окне
// цистерны (решение пользователя 28.10); своя жидкость натечёт сама. Сеть — один раз: после
// слива остальные её цистерны уже чисты.
func healFluids(ctx context.Context, h *act.Hands) error {
	plan, err := LoadPlan()
	if err != nil {
		return nil
	}
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Tank {
			continue
		}
		var tanks []layout.Placement
		for _, pl := range plan.Layout {
			if pl.Tag == cluster.Tag(c.Name) && pl.Name == "storage-tank" {
				tanks = append(tanks, pl)
			}
		}
		for {
			states, err := entityStates(h, tanks)
			if err != nil {
				return err
			}
			var bad *bridge.EntityState
			alien := ""
			for _, st := range states {
				if st == nil {
					continue
				}
				for f, v := range st.Fluids {
					if f != c.Product && v > 0 {
						bad, alien = st, f
					}
				}
				if bad != nil {
					break
				}
			}
			if bad == nil {
				break
			}
			if err := h.FlushPipeline(ctx, bad.Entity, alien); err != nil {
				return err
			}
		}
	}
	return nil
}
