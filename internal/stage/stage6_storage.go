package stage

import (
	"context"
	"fmt"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 6 (stages.md): кластер хранения (склад бота) из плана стадии 3.

func init() {
	register(Stage{N: 6, Name: "хранение", Run: runStorage, Check: checkStorage})
}

// s6Layout — постройки кластера хранения из плана.
func s6Layout() ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	var res []layout.Placement
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Storage {
			continue
		}
		for _, pl := range plan.Layout {
			if pl.Tag == cluster.Tag(c.Name) {
				res = append(res, pl)
			}
		}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("в плане нет кластера хранения")
	}
	return res, nil
}

func runStorage(ctx context.Context, h *act.Hands) error {
	list, err := s6Layout()
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	return (&s2{h: h, sites: sites}).build(ctx, list)
}

// build — поставить недостающее из list: по видам в порядке первого появления, каждый вид —
// скрафтить разом и поставить.
func (s *s2) build(ctx context.Context, list []layout.Placement) error {
	var names []string
	for _, pl := range list {
		if !slices.Contains(names, pl.Name) {
			names = append(names, pl.Name)
		}
	}
	for _, name := range names {
		var todo []layout.Placement
		for _, pl := range list {
			if pl.Name != name {
				continue
			}
			if e, err := s.entity(pl); err != nil {
				return err
			} else if e == nil {
				todo = append(todo, pl)
			}
		}
		if len(todo) == 0 {
			continue
		}
		if err := s.have(ctx, name, len(todo)); err != nil {
			return err
		}
		for _, pl := range todo {
			if _, err := s.h.Place(ctx, pl.Name, pl.Pos, pl.Dir); err != nil {
				return fmt.Errorf("поставить %s в (%.1f, %.1f): %w", pl.Name, pl.Pos.X, pl.Pos.Y, err)
			}
		}
	}
	return nil
}

func checkStorage(h *act.Hands) error {
	list, err := s6Layout()
	if err != nil {
		return err
	}
	return checkBuilt(h, list)
}

// checkBuilt — всё из list стоит.
func checkBuilt(h *act.Hands, list []layout.Placement) error {
	for _, pl := range list {
		e, err := h.B.EntityAt(pl.Name, pl.Pos)
		if err != nil {
			return err
		}
		if e == nil {
			return fmt.Errorf("не стоит %s в (%.1f, %.1f)", pl.Name, pl.Pos.X, pl.Pos.Y)
		}
	}
	return nil
}
