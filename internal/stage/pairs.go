package stage

import (
	"context"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// bonusTechs — бонус захвата инсертеров (stages.md, общее): обычный и быстрый носят по 2; в
// очередь — как изучены красные схемы (bulk-inserter их требует).
var bonusTechs = []string{"bulk-inserter", "inserter-capacity-bonus-1", "inserter-capacity-bonus-2"}

// fastPairs — инсертер пары по плану — быстрый, где обычному не хватает (cluster.md «Сборка»):
// сохранённый план (посчитан до правила) — по правилу, стоящие обычные из плана прошлых стадий
// (до n) — быстрыми поверх (stages.md, общее; стадия 60 на 2448886157: схем 29 % плана).
func fastPairs(ctx context.Context, h *act.Hands, n int) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	if k := cluster.FastPairs(h.Protos, plan.Layout); k > 0 {
		h.Say("план: %d инсертеров пар — быстрые (обычному не хватает)", k)
		if err := writeState(planFile, plan); err != nil {
			return err
		}
	}
	return fastOverBasic(ctx, h, plan, n)
}

// fastCircuits — у кластера зелёных схем все инсертеры — быстрые (stages.md, стадия 28): в плане
// и на карте — поверх обычных.
func fastCircuits(ctx context.Context, h *act.Hands) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	tags := map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Make && c.Product == "electronic-circuit" {
			tags[cluster.Tag(c.Name)] = true
		}
	}
	k := 0
	for i, pl := range plan.Layout {
		if pl.Name == "inserter" && tags[pl.Tag] {
			plan.Layout[i].Name = "fast-inserter"
			k++
		}
	}
	if k > 0 {
		h.Say("план: %d инсертеров кластера зелёных схем — быстрые", k)
		if err := writeState(planFile, plan); err != nil {
			return err
		}
	}
	if err := waitResearch(ctx, h, "fast-inserter"); err != nil {
		return err
	}
	return fastOverBasic(ctx, h, plan, 28)
}

// fastOverBasic — быстрые инсертеры плана прошлых стадий (до n), на месте которых стоит обычный, —
// поверх (замена на месте).
func fastOverBasic(ctx context.Context, h *act.Hands, plan *ClusterPlan, n int) error {
	st := planStages(plan)
	var want, old []layout.Placement
	for _, pl := range plan.Layout {
		if pl.Name == "fast-inserter" && st[pl] > 0 && st[pl] < n {
			o := pl
			o.Name = "inserter"
			want, old = append(want, pl), append(old, o)
		}
	}
	states, err := entityStates(h, old)
	if err != nil {
		return err
	}
	var todo []layout.Placement
	for i, s := range states {
		if s != nil {
			todo = append(todo, want[i])
		}
	}
	if len(todo) == 0 {
		return nil
	}
	h.Say("инсертеры пар: %d быстрых поверх обычных", len(todo))
	return buildSorted(ctx, h, todo, []string{"fast-inserter"})
}

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
