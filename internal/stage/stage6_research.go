package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 6 (stages.md): 2 лаборатории на месте кластера лабораторий из плана (начало
// кластера), электричество — столбами от станции по коридорам (столбы логистики плана, не
// через чужие кластеры), красная наука руками, изучить s6Techs.

const (
	s6Plan = "state-stage6.json"
	s6Labs = 2
	s6Pack = "automation-science-pack"
)

var s6Techs = []string{"automation", "electric-mining-drill", "logistics"}

func init() {
	register(Stage{N: 6, Name: "первые исследования", Run: runResearch, Check: checkResearch})
}

// s6Layout — лаборатории и столбы до них: кратчайшая цепочка столбов плана (логистика,
// станция, лаборатории) от столбов станции до столба, питающего каждую лабораторию.
func s6Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s6Plan)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	station, err := s5Layout(h)
	if err != nil {
		return nil, err
	}
	var labName, powerName, busName string
	for _, c := range plan.Clusters {
		switch c.Kind {
		case cluster.Bus: // клиенты шины (лаборатории) — в её площади, столбы — по её полосе
			busName = c.Name
		case cluster.Lab:
			labName = c.Name
		case cluster.Power:
			powerName = c.Name
		}
	}
	var labs []layout.Placement
	for _, pl := range plan.Layout {
		if pl.Tag == cluster.Tag(labName) && pl.Name == "lab" && len(labs) < s6Labs {
			labs = append(labs, pl)
		}
	}
	if len(labs) < s6Labs {
		return nil, fmt.Errorf("в плане нет %d лабораторий", s6Labs)
	}
	var start []bridge.Pos
	for _, pl := range station {
		if pl.Name == "small-electric-pole" {
			start = append(start, pl.Pos)
		}
	}
	// столбы — по коридорам до каждой лаборатории (следующей — от уже проведённых)
	var list []layout.Placement
	for _, lab := range labs {
		path, err := corridorPoles(h, plan, list, start, []string{powerName, labName, busName}, func(p bridge.Pos) bool { return powers(h, p, lab) })
		if err != nil {
			return nil, fmt.Errorf("лаборатория в (%.1f, %.1f): %w", lab.Pos.X, lab.Pos.Y, err)
		}
		for _, pl := range path {
			list = append(list, pl)
			start = append(start, pl.Pos)
		}
	}
	list = append(list, labs...)
	data, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return nil, err
	}
	return list, os.WriteFile(runPath(s6Plan), data, 0o644)
}

func runResearch(ctx context.Context, h *act.Hands) error {
	list, err := s6Layout(h)
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites}
	if err := s.build(ctx, list); err != nil {
		return err
	}
	// рецепт красной науки открывается крафтом лаборатории (trigger)
	if err := s6WaitRecipe(ctx, h, s6Pack); err != nil {
		return err
	}
	for _, tech := range s6Techs {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		if slices.Contains(f.Researched, tech) {
			continue
		}
		t := h.Protos.Technologies[tech]
		n := int(t.UnitCount)
		labs, err := s6Labs2(h, list)
		if err != nil {
			return err
		}
		inLabs := 0
		for _, l := range labs {
			if st, err := h.B.GetEntity("lab", l.Pos); err == nil && st != nil {
				inLabs += st.Count(s6Pack)
			}
		}
		if n > inLabs {
			if err := s.have(ctx, s6Pack, n-inLabs); err != nil {
				return err
			}
		}
		if err := h.StartResearch(ctx, tech); err != nil {
			return err
		}
		for i, l := range labs {
			k := s.inv(s6Pack) / (len(labs) - i)
			if k == 0 {
				continue
			}
			e, err := s.entity(l)
			if err != nil || e == nil {
				return fmt.Errorf("нет лаборатории в (%.1f, %.1f): %v", l.Pos.X, l.Pos.Y, err)
			}
			if err := h.Insert(ctx, *e, s6Pack, k); err != nil {
				return err
			}
		}
		limit := time.Duration(t.UnitCount*t.UnitTime/float64(len(labs))*1.5+120) * time.Second
		h.Say("исследую %s (до %.0f с)", tech, limit.Seconds())
		deadline := time.Now().Add(limit)
		for {
			f, err := h.B.Force()
			if err != nil {
				return err
			}
			if slices.Contains(f.Researched, tech) {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("исследование %s не закончилось за %.0f с (текущее %q, %.0f%%)", tech, limit.Seconds(), f.Current, f.Progress*100)
			}
			if err := sleepCtx(ctx, 3*time.Second); err != nil {
				return err
			}
		}
	}
	return nil
}

func s6Labs2(h *act.Hands, list []layout.Placement) ([]layout.Placement, error) {
	var res []layout.Placement
	for _, pl := range list {
		if pl.Name == "lab" {
			res = append(res, pl)
		}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("нет лабораторий в раскладке")
	}
	return res, nil
}

// s6WaitRecipe — дождаться открытия рецепта (trigger-технология после крафта).
func s6WaitRecipe(ctx context.Context, h *act.Hands, recipe string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		if slices.Contains(f.Recipes, recipe) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("рецепт %s не открылся (изучено: %s)", recipe, strings.Join(f.Researched, ", "))
		}
		if err := sleepCtx(ctx, time.Second); err != nil {
			return err
		}
	}
}

func checkResearch(h *act.Hands) error {
	list, err := s6Layout(h)
	if err != nil {
		return err
	}
	station, err := s5Layout(h)
	if err != nil {
		return err
	}
	eng, err := h.B.GetEntity("steam-engine", s5Find(station, "steam-engine").Pos)
	if err != nil || eng == nil {
		return fmt.Errorf("нет парового двигателя станции: %v", err)
	}
	labs := 0
	for _, pl := range list {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("не стоит %s в (%.1f, %.1f)", pl.Name, pl.Pos.X, pl.Pos.Y)
		}
		if pl.Name == "lab" {
			labs++
			if st.Network == 0 || st.Network != eng.Network {
				return fmt.Errorf("лаборатория в (%.1f, %.1f) не в сети станции (%d ≠ %d)", pl.Pos.X, pl.Pos.Y, st.Network, eng.Network)
			}
		}
	}
	if labs < s6Labs {
		return fmt.Errorf("лабораторий %d, нужно %d", labs, s6Labs)
	}
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	for _, t := range s6Techs {
		if !slices.Contains(f.Researched, t) {
			return fmt.Errorf("не изучено %s", t)
		}
	}
	return nil
}
