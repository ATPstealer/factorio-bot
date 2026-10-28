package stage

import (
	"context"
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
// буров её добычи на выработанном); подвод уже посчитан — достроить недостающее.
func healCoal(ctx context.Context, h *act.Hands) error {
	if _, err := os.Stat(runPath(coalFeedFile)); err != nil {
		plan, err := LoadBluePlan()
		if err != nil {
			if plan, err = LoadPlan(); err != nil {
				return nil // плана нет — нечего
			}
		}
		hungry, why, err := coalHungry(h, plan)
		if err != nil || !hungry {
			return err
		}
		h.Say("уголь станции: %s — подвожу уголь другой добычи", why)
	}
	list, err := onceLayout(coalFeedFile, func() ([]layout.Placement, error) {
		plan, err := LoadBluePlan()
		if err != nil {
			if plan, err = LoadPlan(); err != nil {
				return nil, err
			}
		}
		return coalFeed(h, plan)
	})
	if err != nil {
		return err
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
	// сначала отвод, делитель — последним (на месте ленты подвода: её поднять)
	if err := buildSorted(ctx, h, belts, []string{"transport-belt", "underground-belt"}); err != nil {
		return err
	}
	for _, pl := range splitters {
		for _, t := range layout.Tiles(h.Protos, pl) {
			c := bridge.Pos{X: float64(t.X) + 0.5, Y: float64(t.Y) + 0.5}
			if e, err := h.B.EntityAt("transport-belt", c); err == nil && e != nil {
				if err := h.Pickup(ctx, *e); err != nil {
					return err
				}
			}
		}
	}
	return buildSorted(ctx, h, splitters, []string{"splitter"})
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
// тайл — сбоку, свободен) и лента от его второго выхода боком в начало ленты угля станции
// (лента, с которой уголь доходит до большего числа лент ряда станций).
func coalFeed(h *act.Hands, plan *ClusterPlan) ([]layout.Placement, error) {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return nil, err
	}
	at, pair := beltTiles(plan.Layout)
	row, feed := stationTags(plan)
	mines := stationMines(plan)
	coalMine := map[string]bool{} // добыча угля не станции
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Mine && c.Product == "coal" && !mines[c.Name] {
			coalMine[c.Name] = true
		}
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
	var sources []cand
	for _, pl := range belts {
		t := world.TileOf(pl.Pos)
		name, ok := strings.CutPrefix(pl.Tag, cluster.LogisticsTag+"/")
		if !ok || !built[t] || !coal[t] || !straight(at, t) {
			continue
		}
		parts := strings.Split(name, "/") // шина/добыча
		if len(parts) != 2 || !coalMine[parts[1]] {
			continue
		}
		sources = append(sources, cand{t: t, pl: pl})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("уголь станции: нет подвода угля другой добычи в игре")
	}
	type try struct {
		s, g cand
		d    int
	}
	var tries []try
	for _, s := range sources {
		for _, g := range targets {
			tries = append(tries, try{s, g, abs(s.t.X-g.t.X) + abs(s.t.Y-g.t.Y)})
		}
	}
	sort.Slice(tries, func(i, j int) bool { return tries[i].d < tries[j].d })
	l := layout.New(h.Protos, h.World)
	for _, pl := range plan.Layout {
		l.Add(pl)
	}
	for k, tr := range tries {
		if k >= 60 {
			break
		}
		s, d := tr.s.t, tr.s.pl.Dir
		for _, side := range []int{(d + 4) % 16, (d + 12) % 16} {
			o := layout.Step(s, side)
			if !l.Free(o, layout.AnyLand) || !l.Free(layout.Step(o, d), layout.AnyLand) {
				continue
			}
			sp := layout.Placement{Name: "splitter", Dir: d, Tag: coalFeedTag,
				Pos: bridge.Pos{X: (float64(s.X+o.X) + 1) / 2, Y: (float64(s.Y+o.Y) + 1) / 2}}
			for _, gs := range []int{(tr.g.pl.Dir + 4) % 16, (tr.g.pl.Dir + 12) % 16} {
				to := layout.Step(tr.g.t, gs)
				mark := l.Mark()
				l.Add(sp)
				if _, err := l.RouteBelt(layout.Step(o, d), d, to, (gs+8)%16, coalFeedTag); err == nil {
					res := slices.Clone(l.List[mark:])
					h.Log("уголь станции: делитель в (%.1f, %.1f), отвод %d построек до (%d, %d)", sp.Pos.X, sp.Pos.Y, len(res)-1, tr.g.t.X, tr.g.t.Y)
					return res, nil
				}
				l.Rollback(mark)
			}
		}
	}
	return nil, fmt.Errorf("уголь станции: отвод не проложить (источников %d, целей %d)", len(sources), len(targets))
}
