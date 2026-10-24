package stage

import (
	"context"
	"fmt"
	"slices"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Общее для стадий стройки: построить раскладку по порядку, задать рецепты и фильтры.

// buildSorted — построить list (уже стоящее пропускается) в порядке order (чего нет в order — в
// конце), недостающее — из хранения. Сборщики и инсертеры настраиваются сразу, как поставлены
// (configure): инсертеры — раньше лент, иначе буфер без фильтра набирает чужое.
func buildSorted(ctx context.Context, h *act.Hands, list []layout.Placement, order []string) error {
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites, useStore: true}
	s.placed = func(ctx context.Context, name string) error {
		var these []layout.Placement
		for _, pl := range list {
			if pl.Name == name {
				these = append(these, pl)
			}
		}
		return configure(ctx, h, these)
	}
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b layout.Placement) int {
		ia, ib := slices.Index(order, a.Name), slices.Index(order, b.Name)
		if ia < 0 {
			ia = len(order)
		}
		if ib < 0 {
			ib = len(order)
		}
		return ia - ib
	})
	return s.build(ctx, sorted)
}

// filterHold — фильтр-заглушка, пока нужный предмет не открыт: инсертер с ним стоит (уголь на
// лентах пакетов и продуктов не бывает); без фильтра он перекладывал бы всё подряд. Нужный
// фильтр добавляется позже (белый список «уголь + предмет» — берёт только предмет).
const filterHold = "coal"

// lockedItems — предметы, чей рецепт ещё не открыт: в 2.1 окно выбора фильтра их не показывает
// («Nothing found» — стадия 21 на 859088572, фильтр зелёных пакетов до исследования).
func lockedItems(h *act.Hands) (map[string]bool, error) {
	f, err := h.B.Force()
	if err != nil {
		return nil, err
	}
	res := map[string]bool{}
	for name := range h.Protos.Recipes {
		if _, item := h.Protos.Items[name]; item && !slices.Contains(f.Recipes, name) {
			res[name] = true
		}
	}
	return res, nil
}

// configure — рецепты сборщиков и фильтры инсертеров list по плану (Recipe). Фильтр на ещё не
// открытый предмет — заглушка filterHold, настоящий ставит стадия, которая предмет вводит;
// рецепт сборщика ещё не открыт — пропустить (waitOpen, потом configure ещё раз).
func configure(ctx context.Context, h *act.Hands, list []layout.Placement) error {
	locked, err := lockedItems(h)
	if err != nil {
		return err
	}
	for _, pl := range list {
		if pl.Recipe == "" || pl.Name != "assembling-machine-1" && pl.Name != "inserter" {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет %s в (%.1f, %.1f): %v", pl.Name, pl.Pos.X, pl.Pos.Y, err)
		}
		switch item := pl.Recipe; {
		case pl.Name == "assembling-machine-1":
			if st.Recipe != pl.Recipe && !locked[pl.Recipe] {
				err = h.SetRecipe(ctx, st.Entity, pl.Recipe)
			}
		case !slices.Contains(st.Filters, pl.Recipe):
			if locked[item] {
				item = filterHold
			}
			if !slices.Contains(st.Filters, item) {
				err = h.SetFilter(ctx, st.Entity, item)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// openWait — сколько ждать исследования рецепта (зелёная наука: в очереди с обслуживания).
const openWait = 40 * time.Minute

// waitOpen — рецепты сборщиков list открыты; нет — доставлять в очередь нужные исследования
// и ждать (страховка: обычно изучено заранее).
func waitOpen(ctx context.Context, h *act.Hands, list []layout.Placement) error {
	deadline := time.Now().Add(openWait)
	for said := false; ; said = true {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		var miss []string
		for _, pl := range list {
			if pl.Name == "assembling-machine-1" && pl.Recipe != "" && !slices.Contains(f.Recipes, pl.Recipe) && !slices.Contains(miss, pl.Recipe) {
				miss = append(miss, pl.Recipe)
			}
		}
		if len(miss) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("рецепт %v не открыт за %v (идёт %s, %.0f%%)", miss, openWait, f.Current, f.Progress*100)
		}
		if !said {
			h.Say("жду исследования рецепта %v", miss)
		}
		if err := queueResearch(ctx, h); err != nil {
			return err
		}
		if err := sleepCtx(ctx, 30*time.Second); err != nil {
			return err
		}
	}
}
