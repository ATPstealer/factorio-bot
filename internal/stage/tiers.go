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

// fastRows — номера в plan.Layout лент, подземок и делителей рядов шин, которым жёлтой мало
// (cluster.FastLanes, cluster.md §5): лента и подземка — с тегом шины в ряду её ленты, делитель —
// любой, что стоит в ряду (подвод, отвод, балансировка), в пределах ленты.
func fastRows(plan *ClusterPlan) map[int]bool {
	fast := cluster.FastLanes(plan.Clusters)
	type row struct {
		tag string
		y   int
	}
	span := map[row][2]float64{} // ряд → центры лент от и до по X
	for _, pl := range plan.Layout {
		if baseTier(pl.Name) != "transport-belt" || !fast[strings.TrimPrefix(pl.Tag, "c/")][pl.Recipe] {
			continue
		}
		r := row{pl.Tag, int(math.Floor(pl.Pos.Y))}
		s, ok := span[r]
		if !ok {
			s = [2]float64{pl.Pos.X, pl.Pos.X}
		}
		span[r] = [2]float64{min(s[0], pl.Pos.X), max(s[1], pl.Pos.X)}
	}
	res := map[int]bool{}
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
