package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 16 (stages.md): кластер лабораторий по плану стадии 5 полностью — пакеты красной
// науки отводом с шины, буфер входа, инсертеры в лаборатории, питание по коридорам. Бот
// только запускает исследование; критерий — оно идёт само (пакеты приходят лентой).

const s16Plan = "state-stage16.json"

func init() {
	register(Stage{N: 16, Name: "кластер лабораторий", Run: runLabs, Check: checkLabs})
}

// s16Layout — внутренность кластера лабораторий, отводы к нему, делители, столбы; один раз
// на карту (файл).
func s16Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s16Plan)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	c := clusterOf(plan, cluster.Lab, "")
	if c == nil {
		return nil, fmt.Errorf("в плане нет кластера лабораторий")
	}
	res := withSplitters(h, plan, tagged(plan, cluster.Tag(c.Name), cluster.LogisticsTag+"/"+c.Name))
	allow := []string{c.Name}
	for _, o := range plan.Clusters {
		if o.Kind == cluster.Bus || o.Kind == cluster.Power {
			allow = append(allow, o.Name)
		}
	}
	poles, err := connectPower(h, plan, res, allow)
	if err != nil {
		return nil, err
	}
	res = append(res, poles...)
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(runPath(s16Plan), data, 0o644)
}

// s16Tech — исследование на одной красной науке, доступное (предпосылки изучены), не
// изученное; самое дорогое: дешёвое (gun-turret) кончалось из буфера пакетов раньше проверки
// «исследование идёт».
func s16Tech(h *act.Hands) (string, error) {
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
			t.UnitIngredients[0].Name != s7Pack {
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
	list, err := s16Layout(h)
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites, useStore: true}
	order := []string{"lab", "transport-belt", "underground-belt", "splitter", chest, "inserter", "small-electric-pole"}
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b layout.Placement) int {
		return slices.Index(order, a.Name) - slices.Index(order, b.Name)
	})
	if err := s.build(ctx, sorted); err != nil {
		return err
	}
	for _, pl := range list {
		if pl.Name != "inserter" || pl.Recipe == "" {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет инсертера в (%.1f, %.1f): %v", pl.Pos.X, pl.Pos.Y, err)
		}
		if !slices.Contains(st.Filters, pl.Recipe) {
			if err := h.SetFilter(ctx, st.Entity, pl.Recipe); err != nil {
				return err
			}
		}
	}
	tech, err := s16Tech(h)
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
			if tech, err := s16Tech(h); err == nil {
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
	list, err := s16Layout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	fed := 0
	for _, pl := range list {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		if pl.Name == "inserter" && pl.Recipe != "" && !slices.Contains(st.Filters, pl.Recipe) {
			return fmt.Errorf("инсертер в (%.1f, %.1f) без фильтра %s", pl.Pos.X, pl.Pos.Y, pl.Recipe)
		}
		if pl.Name == "lab" && st.Count(s7Pack) > 0 {
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
