package stage

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Второй уровень по стадиям (stages.md, стадии 22 и 42; cluster.md §7): инсертеры — быстрые с
// cluster.FastFrom, печи и сборщики — второго уровня с cluster.Tier2From, ленты рядов шин, которым
// жёлтой мало (cluster.md §5), — тоже с cluster.Tier2From. План держит каждую постройку тем
// уровнем, каким она стоит к стадии; остальные ленты — первого уровня (их замена — стадия 68),
// столбы и сундуки — только первого.

// bonusTechs — бонус захвата инсертеров (stages.md, общее): обычный и быстрый носят по 2; в
// очередь — как изучены красные схемы (bulk-inserter их требует).
var bonusTechs = []string{"bulk-inserter", "inserter-capacity-bonus-1", "inserter-capacity-bonus-2"}

// withBonus — techs, а перед ними — бонус захвата, если красные схемы изучены.
func withBonus(researched, techs []string) []string {
	if !slices.Contains(researched, "advanced-circuit") {
		return techs
	}
	var res []string
	for _, t := range bonusTechs {
		if !slices.Contains(researched, t) {
			res = append(res, t)
		}
	}
	return append(res, techs...)
}

// tierFrom — с какой стадии постройка name первого уровня — второго; 0 — не меняется (ленты — по
// рядам: upFrom).
func tierFrom(name string) int {
	switch name {
	case "inserter":
		return cluster.FastFrom
	case "stone-furnace", "assembling-machine-1":
		return cluster.Tier2From
	}
	return 0
}

// upFrom — с какой стадии постройка pl плана первого уровня — второго; 0 — не меняется. Лента,
// подземка, делитель — с cluster.Tier2From только в ряду шины, которому жёлтой мало (fast —
// fastRows), остальные — стадия 68.
func upFrom(pl layout.Placement, fast bool) int {
	base := baseTier(pl.Name)
	if fastTier[base] != "" {
		if fast {
			return cluster.Tier2From
		}
		return 0
	}
	return tierFrom(base)
}

// baseTier — постройка первого уровня, которую заменяет name (fast-inserter → inserter); name —
// если она не замена.
func baseTier(name string) string {
	for b, up := range cluster.Tier2 {
		if up == name {
			return b
		}
	}
	return name
}

// busRow — ряд шины: тег шины и Y.
type busRow struct {
	tag string
	y   int
}

// fastRows — номера в plan.Layout лент, подземок и делителей, которым жёлтой мало (cluster.md
// §5): ряды шин выше bus-1 — по cluster.FastLanes; выходы добычи больше 15/с — половина ленты
// сбора, подвод и ряд bus-1, куда он входит (mineLoads). В ряду шины лента и подземка — с тегом
// шины, делитель — любой, что стоит в ряду (подвод, отвод, балансировка), в пределах ленты.
func fastRows(plan *ClusterPlan) map[int]bool {
	fast := cluster.FastLanes(plan.Clusters)
	res, rows := mineLoads(plan)
	span := map[busRow][2]float64{} // ряд → центры лент от и до по X
	for _, pl := range plan.Layout {
		if baseTier(pl.Name) != "transport-belt" || !strings.HasPrefix(pl.Tag, "c/bus-") {
			continue
		}
		r := busRow{pl.Tag, int(math.Floor(pl.Pos.Y))}
		if !rows[r] && !fast[strings.TrimPrefix(pl.Tag, "c/")][pl.Recipe] {
			continue
		}
		s, ok := span[r]
		if !ok {
			s = [2]float64{pl.Pos.X, pl.Pos.X}
		}
		span[r] = [2]float64{min(s[0], pl.Pos.X), max(s[1], pl.Pos.X)}
	}
	for i, pl := range plan.Layout {
		base := baseTier(pl.Name)
		if fastTier[base] == "" {
			continue
		}
		for r, s := range span {
			if pl.Pos.X < s[0]-1.5 || pl.Pos.X > s[1]+1.5 {
				continue
			}
			if base == "splitter" {
				if pl.Dir == layout.E && (int(math.Floor(pl.Pos.Y-0.5)) == r.y || int(math.Floor(pl.Pos.Y+0.5)) == r.y) {
					res[i] = true
				}
			} else if pl.Tag == r.tag && int(math.Floor(pl.Pos.Y)) == r.y {
				res[i] = true
			}
		}
	}
	return res
}

// mineLoads — выходы блоков добычи (cluster.md §5): выпуск выхода — по бурам рядов, что идут в
// его половину ленты сбора; по подводу от выхода — до ряда bus-1 (боком в чужой подвод — дальше по
// нему, по сумме). Номера построек, которым жёлтой мало (половины ленты сбора, участки подводов),
// и ряды bus-1, куда входит больше 15/с.
func mineLoads(plan *ClusterPlan) (map[int]bool, map[busRow]bool) {
	at := map[world.Tile]int{} // лента или подземка на тайле
	for i, pl := range plan.Layout {
		if b := baseTier(pl.Name); b == "transport-belt" || b == "underground-belt" {
			at[world.TileOf(pl.Pos)] = i
		}
	}
	load := map[int]float64{}
	rowLoad := map[busRow]float64{}
	res := map[int]bool{}
	// walk — r от тайла t по лентам подвода (теги логистики) до ряда шины
	walk := func(t world.Tile, r float64) {
		seen := map[world.Tile]bool{}
		for !seen[t] {
			seen[t] = true
			i, ok := at[t]
			if !ok {
				return
			}
			pl := plan.Layout[i]
			if strings.HasPrefix(pl.Tag, "c/bus-") {
				rowLoad[busRow{pl.Tag, t.Y}] += r
				return
			}
			if !strings.HasPrefix(pl.Tag, cluster.LogisticsTag+"/") {
				return // в кластер (уголь станции)
			}
			load[i] += r
			if baseTier(pl.Name) == "underground-belt" && pl.Recipe == "input" {
				out := false
				for k, cur := 1, t; k <= 10 && !out; k++ {
					cur = layout.Step(cur, pl.Dir)
					if j, ok := at[cur]; ok && baseTier(plan.Layout[j].Name) == "underground-belt" &&
						plan.Layout[j].Recipe == "output" && plan.Layout[j].Dir == pl.Dir {
						load[j] += r
						t, out = cur, true
					}
				}
				if !out {
					return
				}
			}
			t = layout.Step(t, pl.Dir)
		}
	}
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Mine || c.IsOil() || len(c.Units) == 0 {
			continue
		}
		var outs []cluster.Port
		for _, p := range c.Ports {
			if !p.In {
				outs = append(outs, p)
			}
		}
		own := func(tag string) bool { return tag == cluster.Tag(c.Name) || tag == cluster.LaterTag(c.Name) }
		// onLine — тайл на линии выходов (лента сбора — вдоль неё)
		onLine := func(t world.Tile, p cluster.Port) bool {
			if p.Dir == layout.N || p.Dir == layout.S {
				return t.X == p.T.X
			}
			return t.Y == p.T.Y
		}
		// буры — по выходам: куда течёт лента сбора напротив ряда бура
		drills := make([]int, len(outs))
		for _, pl := range plan.Layout {
			if pl.Name != c.Units[0].Machine || !own(pl.Tag) || len(outs) == 0 {
				continue
			}
			drop := layout.Step(layout.Step(world.TileOf(pl.Pos), pl.Dir), pl.Dir) // лента ряда
			ct := world.Tile{X: outs[0].T.X, Y: drop.Y}
			if outs[0].Dir != layout.N && outs[0].Dir != layout.S {
				ct = world.Tile{X: drop.X, Y: outs[0].T.Y}
			}
			if j, ok := at[ct]; ok && own(plan.Layout[j].Tag) {
				for k, p := range outs {
					if plan.Layout[j].Dir == p.Dir {
						drills[k]++
					}
				}
			}
		}
		total, out := 0, 0.0
		for _, n := range drills {
			total += n
		}
		for _, f := range c.Out {
			if f.Item == c.Product {
				out += f.Rate
			}
		}
		for k, p := range outs {
			r := out / float64(len(outs))
			if total > 0 {
				r = out * float64(drills[k]) / float64(total)
			}
			if r > cluster.YellowRate+1e-9 {
				for j, pl := range plan.Layout {
					if own(pl.Tag) && baseTier(pl.Name) == "transport-belt" && pl.Dir == p.Dir && onLine(world.TileOf(pl.Pos), p) {
						res[j] = true
					}
				}
			}
			walk(p.T, r)
		}
	}
	for i, r := range load {
		if r > cluster.YellowRate+1e-9 {
			res[i] = true
		}
	}
	rows := map[busRow]bool{}
	for row, r := range rowLoad {
		if r > cluster.YellowRate+1e-9 {
			rows[row] = true
		}
	}
	return res, rows
}

// planTiers — уровни построек плана: постройка стадии st — второго уровня, если st или done
// (последняя пройденная стадия: стоящее к ней уже заменено) — не раньше порога её вида. Сколько
// переименовано.
func planTiers(plan *ClusterPlan, done int) int {
	st := planStages(plan)
	fast := fastRows(plan)
	k := 0
	for i, pl := range plan.Layout {
		base := baseTier(pl.Name)
		from := upFrom(pl, fast[i])
		if from == 0 {
			continue
		}
		name := base
		if st[pl] >= from || done >= from {
			name = cluster.Tier2[base]
		}
		if name != pl.Name {
			plan.Layout[i].Name = name
			k++
		}
	}
	return k
}

// upgradeBuilt — постройки плана прошлых стадий (до n), у которых в плане второй уровень, а стоит
// первый, — вторым поверх, на месте (act.TierUp: рецепт и содержимое остаются); рецепт второго
// уровня не открыт — потом.
func upgradeBuilt(ctx context.Context, h *act.Hands, plan *ClusterPlan, n int) error {
	st := planStages(plan)
	var want, old []layout.Placement
	for _, pl := range plan.Layout {
		if base := baseTier(pl.Name); base != pl.Name && st[pl] > 0 && st[pl] < n {
			o := pl
			o.Name = base
			want, old = append(want, pl), append(old, o)
		}
	}
	states, err := entityStates(h, old)
	if err != nil {
		return err
	}
	locked, err := lockedItems(h)
	if err != nil {
		return err
	}
	var todo []layout.Placement
	names := map[string]int{}
	for i, s := range states {
		if s != nil && !locked[h.Protos.ItemFor(want[i].Name)] {
			todo = append(todo, want[i])
			names[want[i].Name]++
		}
	}
	if len(todo) == 0 {
		return nil
	}
	h.Say("второй уровень поверх стоящих: %v", names)
	return buildSorted(ctx, h, todo, []string{"steel-furnace", "assembling-machine-2", "fast-inserter",
		"fast-transport-belt", "fast-underground-belt", "fast-splitter"})
}

// tiersAt — план к стадии n по уровням (сохранённые планы — до правила) и стоящее ниже уровня
// плана — поверх (stages.md, общее: инсертер по плану — быстрый поверх обычного).
func tiersAt(ctx context.Context, h *act.Hands, n int) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	if k := planTiers(plan, n-1); k > 0 {
		h.Say("план: %d построек — уровнем стадии %d", k, n)
		if err := writeState(planFile, plan); err != nil {
			return err
		}
	}
	return upgradeBuilt(ctx, h, plan, n)
}

// tierStage — замена на стадии n (22 — инсертеры, 42 — печи и сборщики): исследования techs — в
// очередь и ждать, план — всё прошлое вторым уровнем, стоящее — поверх.
func tierStage(ctx context.Context, h *act.Hands, n int, techs ...string) error {
	if err := queueTechs(ctx, h, techs...); err != nil {
		return err
	}
	if err := waitResearch(ctx, h, techs...); err != nil {
		return err
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	if k := planTiers(plan, n); k > 0 {
		h.Say("план: %d построек — второго уровня (стадия %d)", k, n)
		if err := writeState(planFile, plan); err != nil {
			return err
		}
	}
	return upgradeBuilt(ctx, h, plan, n+1)
}

// noBaseTier — проверка стадии замены: у построек плана до n с порогом не позже n первого уровня
// не стоит.
func noBaseTier(h *act.Hands, n int, names ...string) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	st := planStages(plan)
	var old []layout.Placement
	for _, pl := range plan.Layout {
		if base := baseTier(pl.Name); base != pl.Name && slices.Contains(names, base) && st[pl] > 0 && st[pl] <= n {
			o := pl
			o.Name = base
			old = append(old, o)
		}
	}
	states, err := entityStates(h, old)
	if err != nil {
		return err
	}
	left := 0
	for _, s := range states {
		if s != nil {
			left++
		}
	}
	if left > 0 {
		return fmt.Errorf("первого уровня осталось %d (%v)", left, names)
	}
	return nil
}
