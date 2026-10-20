package stage

import (
	"context"
	"errors"
	"fmt"
	"math"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// Стадия 1 (stages.md): с новой карты после заставки — 50 дерева, 200 камня с больших камней
// (лучше huge-rock: камень + уголь), разобрать обломки корабля. Ходить меньше: каждый раз —
// ближайшая из нужных целей.

const (
	startWood  = 50
	startStone = 200
	startR     = 200 // радиус поиска целей, тайлов
)

func init() {
	register(Stage{N: 1, Name: "старт: дерево, камень, обломки корабля", Run: runStart, Check: checkStart})
}

// startTargets — какие цели ещё нужны: обломки — всегда (пока есть), камни — пока камня мало,
// деревья — пока мало дерева. Камни: сначала huge-rock (дают и уголь), нет — любые.
func startTargets(h *act.Hands) ([]string, error) {
	p, err := h.B.Player()
	if err != nil {
		return nil, err
	}
	ts := []string{"wreck"}
	if p.Inventory["stone"] < startStone {
		ts = append(ts, "huge-rock", "rock")
	}
	if p.Inventory["wood"] < startWood {
		ts = append(ts, "tree")
	}
	return ts, nil
}

func runStart(ctx context.Context, h *act.Hands) error {
	for i := 0; i < 2000; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		ts, err := startTargets(h)
		if err != nil {
			return err
		}
		// ближайшая из нужных; huge-rock — с запасом расстояния вперёд обычных камней
		var best *bridge.Entity
		bestT, bestD := "", math.Inf(1)
		haveHuge := false
		for _, t := range ts {
			es, err := h.B.FindMinable(t, startR, 1)
			if err != nil {
				return err
			}
			if len(es) == 0 {
				continue
			}
			if t == "huge-rock" {
				haveHuge = true
			}
			if t == "rock" && haveHuge {
				continue // большие камни есть — обычные не берём
			}
			d := es[0].Distance
			if t == "huge-rock" {
				d -= 20 // уголь в придачу — стоит небольшого крюка
			}
			if d < bestD {
				e := es[0]
				best, bestT, bestD = &e, t, d
			}
		}
		if best == nil {
			if len(ts) == 1 { // остались только обломки, и их нет — всё
				return nil
			}
			return fmt.Errorf("в радиусе %d нет целей %v", startR, ts)
		}
		target := best.Name
		if bestT == "wreck" || bestT == "tree" {
			target = bestT
		}
		if _, err := h.Gather(ctx, target, 1, startR); err != nil && !errors.Is(err, act.ErrNoTargets) {
			return err
		}
	}
	return fmt.Errorf("слишком много шагов")
}

func checkStart(h *act.Hands) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if w := p.Inventory["wood"]; w < startWood {
		return fmt.Errorf("дерева %d из %d", w, startWood)
	}
	if s := p.Inventory["stone"]; s < startStone {
		return fmt.Errorf("камня %d из %d", s, startStone)
	}
	if es, err := h.B.FindMinable("wreck", startR, 1); err != nil {
		return err
	} else if len(es) > 0 {
		return fmt.Errorf("остались обломки корабля: %s (%.0f, %.0f)", es[0].Name, es[0].Position.X, es[0].Position.Y)
	}
	return nil
}
