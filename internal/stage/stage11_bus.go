package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадия 11 (stages.md): пластины железа и меди с плавки — в их ленты шины (по плану стадии 3).
// Больше ничего.

const s11Plan = "state-stage11.json"

func init() {
	register(Stage{N: 11, Name: "пластины на шину", Run: runBusPlates, Check: checkBusPlates})
}

// s11Layout — s11Base; один раз на карту (файл).
func s11Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s11Plan)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	res := s11Base(h, plan)
	if len(res) == 0 {
		return nil, fmt.Errorf("в плане нет лент пластин шины")
	}
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(runPath(s11Plan), data, 0o644)
}

func runBusPlates(ctx context.Context, h *act.Hands) error {
	list, err := s11Layout(h)
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites, useStore: true}
	order := []string{"transport-belt", "underground-belt", "splitter"}
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b layout.Placement) int {
		return slices.Index(order, a.Name) - slices.Index(order, b.Name)
	})
	if err := s.build(ctx, sorted); err != nil {
		return err
	}
	// пластины дошли до концов своих лент
	deadline := time.Now().Add(8 * time.Minute)
	for checkBusPlates(h) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// checkBusPlates — всё стоит; на последней ленте каждой ленты пластин шины — её пластины.
func checkBusPlates(h *act.Hands) error {
	list, err := s11Layout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	bus := busOf(plan)
	for _, it := range []string{"iron-plate", "copper-plate"} {
		var last *layout.Placement
		for i, pl := range plan.Layout {
			if bus != nil && pl.Tag == "c/"+bus.Name && pl.Recipe == it {
				last = &plan.Layout[i] // ленты шины — по ходу, последняя — конец
			}
		}
		if last == nil {
			return fmt.Errorf("в плане нет ленты %s шины", it)
		}
		st, err := h.B.GetEntity(last.Name, last.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет конца ленты %s в (%.1f, %.1f): %v", it, last.Pos.X, last.Pos.Y, err)
		}
		if st.Belt[it] == 0 {
			return fmt.Errorf("%s не дошли до конца ленты шины (%.1f, %.1f)", it, last.Pos.X, last.Pos.Y)
		}
	}
	return nil
}
