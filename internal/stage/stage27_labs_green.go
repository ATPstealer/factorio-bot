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

// Стадия 27 (stages.md): второй вход кластера лабораторий — отвод зелёной науки с шины конечных
// (по плану стадии 18: лента сбоку в ленту входа лабораторий), фильтры инсертеров буфера — как
// были (на красную). Критерий — исследование на красной и зелёной науке идёт само.

const s23Plan = "state-stage23.json"

func init() {
	register(Stage{N: 27, Name: "зелёная наука в лаборатории", Run: runLabsGreen, Check: checkLabsGreen})
}

// s23Layout — дописанное стадией 18 в кластер лабораторий и его отводы, лента зелёной науки
// шины конечных с подводом, делители, столбы; один раз на карту (файл).
func s23Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s23Plan)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	added, plan, err := s18Added()
	if err != nil {
		return nil, err
	}
	lab, green := clusterOf(plan, cluster.Lab, ""), clusterOf(plan, cluster.Make, s18Green)
	if lab == nil || green == nil {
		return nil, fmt.Errorf("в плане нет лабораторий или зелёной науки")
	}
	var res []layout.Placement
	for _, pl := range added {
		if pl.Tag == cluster.Tag(lab.Name) || pl.Tag == cluster.LogisticsTag+"/"+lab.Name {
			res = append(res, pl)
		}
	}
	res = withSplitters(h, plan, append(res, busParts(plan, []string{s18Green}, []string{green.Name})...))
	allow := []string{lab.Name}
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
	return res, os.WriteFile(runPath(s23Plan), data, 0o644)
}

// s23Techs — доступные (предпосылки изучены), не изученные исследования на красной и зелёной
// науке; сначала нужные для ракеты, потом самые дорогие.
func s23Techs(h *act.Hands) ([]string, error) {
	f, err := h.B.Force()
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, t := range f.Researched {
		done[t] = true
	}
	path, err := h.Protos.TechPath([]string{"rocket-silo"}, done)
	if err != nil {
		return nil, err
	}
	var cands []string
	for name, t := range h.Protos.Technologies {
		if t.Hidden || !t.Enabled || t.Trigger != nil || done[name] || len(t.UnitIngredients) != 2 {
			continue
		}
		ok := true
		for _, in := range t.UnitIngredients {
			ok = ok && (in.Name == s7Pack || in.Name == s18Green)
		}
		for _, p := range t.Prerequisites {
			ok = ok && done[p]
		}
		if ok {
			cands = append(cands, name)
		}
	}
	cost := func(n string) float64 { t := h.Protos.Technologies[n]; return t.UnitCount * t.UnitTime }
	sort.Slice(cands, func(i, j int) bool {
		ri, rj := slices.Contains(path, cands[i]), slices.Contains(path, cands[j])
		if ri != rj {
			return ri
		}
		if cost(cands[i]) != cost(cands[j]) {
			return cost(cands[i]) > cost(cands[j])
		}
		return cands[i] < cands[j]
	})
	return cands, nil
}

// s23Green — исследование t требует зелёную науку.
func s23Green(h *act.Hands, t string) bool {
	for _, in := range h.Protos.Technologies[t].UnitIngredients {
		if in.Name == s18Green {
			return true
		}
	}
	return false
}

func runLabsGreen(ctx context.Context, h *act.Hands) error {
	list, err := s23Layout(h)
	if err != nil {
		return err
	}
	order := []string{"transport-belt", "underground-belt", "splitter", chest, "inserter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	for _, pl := range list { // фильтры буфера зелёного
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
	// исследование на зелёной — в очередь (очередь красных из обслуживания идёт впереди)
	start := func() error {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		for _, t := range append([]string{f.Current}, f.Queue...) {
			if t != "" && s23Green(h, t) {
				return nil
			}
		}
		techs, err := s23Techs(h)
		if err != nil {
			return err
		}
		if len(techs) == 0 {
			return fmt.Errorf("нет доступных исследований на красной и зелёной науке")
		}
		return h.StartResearch(ctx, techs[0])
	}
	if err := start(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Minute)
	for checkLabsGreen(h) != nil && time.Now().Before(deadline) {
		if err := start(); err != nil { // изучено — следующее
			return err
		}
		if err := sleepCtx(ctx, 20*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// checkLabsGreen — всё стоит; в лабораториях зелёная наука (пришла лентой); идёт исследование
// на зелёной и за 30 с продвигается.
func checkLabsGreen(h *act.Hands) error {
	list, err := s23Layout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	for _, pl := range list {
		if pl.Name != "inserter" || pl.Recipe == "" {
			continue
		}
		if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && !slices.Contains(st.Filters, pl.Recipe) {
			return fmt.Errorf("инсертер в (%.1f, %.1f) без фильтра %s", pl.Pos.X, pl.Pos.Y, pl.Recipe)
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	lab := clusterOf(plan, cluster.Lab, "")
	fed := 0
	for _, pl := range tagged(plan, cluster.Tag(lab.Name)) {
		if pl.Name != "lab" {
			continue
		}
		if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && st.Count(s18Green) > 0 {
			fed++
		}
	}
	if fed == 0 {
		return fmt.Errorf("в лабораториях нет %s", s18Green)
	}
	f0, err := h.B.Force()
	if err != nil {
		return err
	}
	if f0.Current == "" || !s23Green(h, f0.Current) {
		return fmt.Errorf("не идёт исследование на зелёной науке (идёт %q)", f0.Current)
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
