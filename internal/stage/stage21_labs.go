package stage

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 21 (stages.md): кластер лабораторий по плану стадии 5 полностью — пакеты красной
// науки отводом с шины, буфер входа, инсертеры в лаборатории, питание по коридорам. Бот
// только запускает исследование; критерий — оно идёт само (пакеты приходят лентой).

const labsFile = "state-labs.json"

func init() {
	register(Stage{N: 21, Name: "кластер лабораторий", Run: runLabs, Check: checkLabs})
}

// labsLayout — внутренность кластера лабораторий, отводы к нему, делители, столбы; один раз
// на карту (файл).
func labsLayout(h *act.Hands) ([]layout.Placement, error) {
	return onceLayout(labsFile, func() ([]layout.Placement, error) {
		plan, err := LoadPlan()
		if err != nil {
			return nil, err
		}
		c := clusterOf(plan, cluster.Lab, "")
		if c == nil {
			return nil, fmt.Errorf("в плане нет кластера лабораторий")
		}
		res := withSplitters(h, plan, tagged(plan, cluster.Tag(c.Name), cluster.LogisticsTag+"/"+c.Name))
		poles, err := connectPower(h, plan, res, []string{c.Name})
		return append(res, poles...), err
	})
}

// labsTech — исследование на одной красной науке, доступное (предпосылки изучены), не
// изученное; самое дорогое: дешёвое (gun-turret) кончалось из буфера пакетов раньше проверки
// «исследование идёт».
func labsTech(h *act.Hands) (string, error) {
	f, err := h.B.Force()
	if err != nil {
		return "", err
	}
	if f.Current != "" {
		return f.Current, nil
	}
	var cands []string
	for name, t := range h.Protos.Technologies {
		if t.Hidden || !t.Enabled || t.Trigger != nil || slices.Contains(f.Researched, name) || len(t.UnitIngredients) != 1 ||
			t.UnitIngredients[0].Name != redPack {
			continue
		}
		ok := true
		for _, p := range t.Prerequisites {
			ok = ok && slices.Contains(f.Researched, p)
		}
		if ok {
			cands = append(cands, name)
		}
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("нет исследований на одной красной науке")
	}
	if slices.Contains(cands, greenPack) { // первым — рецепт зелёной науки (stages.md, стадия 21)
		return greenPack, nil
	}
	cost := func(n string) float64 { t := h.Protos.Technologies[n]; return t.UnitCount * t.UnitTime }
	sort.Slice(cands, func(i, j int) bool {
		if cost(cands[i]) != cost(cands[j]) {
			return cost(cands[i]) > cost(cands[j])
		}
		return cands[i] < cands[j]
	})
	return cands[0], nil
}

func runLabs(ctx context.Context, h *act.Hands) error {
	list, err := labsLayout(h)
	if err != nil {
		return err
	}
	order := []string{"lab", chest, "inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	// фильтры; зелёные пакеты ещё не открыты — заглушка, настоящий фильтр — стадия 27
	if err := configure(ctx, h, list); err != nil {
		return err
	}
	tech, err := labsTech(h)
	if err != nil {
		return err
	}
	if err := h.StartResearch(ctx, tech); err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Minute)
	for checkLabs(h) != nil && time.Now().Before(deadline) {
		// исследование кончилось — следующее
		if f, err := h.B.Force(); err == nil && f.Current == "" {
			if tech, err := labsTech(h); err == nil {
				if err := h.StartResearch(ctx, tech); err != nil {
					return err
				}
			}
		}
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// checkLabs — всё стоит, фильтры заданы; исследование идёт само: за 30 с без бота прогресс
// растёт (или изучено новое), в лабораториях — пакеты с ленты.
func checkLabs(h *act.Hands) error {
	list, err := labsLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	locked, err := lockedItems(h)
	if err != nil {
		return err
	}
	fed := 0
	for _, pl := range list {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		if pl.Name == "inserter" && pl.Recipe != "" && !slices.Contains(st.Filters, pl.Recipe) &&
			!(locked[pl.Recipe] && slices.Contains(st.Filters, filterHold)) {
			return fmt.Errorf("инсертер в (%.1f, %.1f) без фильтра %s", pl.Pos.X, pl.Pos.Y, pl.Recipe)
		}
		if pl.Name == "lab" && st.Count(redPack) > 0 {
			fed++
		}
	}
	if fed == 0 {
		return fmt.Errorf("в лабораториях нет пакетов")
	}
	f0, err := h.B.Force()
	if err != nil {
		return err
	}
	if f0.Current == "" {
		return fmt.Errorf("исследование не идёт")
	}
	time.Sleep(30 * time.Second)
	f1, err := h.B.Force()
	if err != nil {
		return err
	}
	if f1.Current == f0.Current && f1.Progress <= f0.Progress {
		return fmt.Errorf("исследование %s не продвигается (%.0f%%)", f0.Current, f0.Progress*100)
	}
	return nil
}
