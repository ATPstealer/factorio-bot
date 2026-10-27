package stage

import (
	"context"
	"errors"
	"fmt"
	"math"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// Стадия 10 (stages.md): с новой карты после заставки — 50 дерева, 200 камня с больших камней
// (лучше huge-rock: камень + уголь), разобрать обломки корабля. Ходить меньше: каждый раз —
// ближайшая из нужных целей.

const (
	startWood  = 50
	startStone = 200
	startR     = 200 // радиус поиска целей, тайлов
)

func init() {
	register(Stage{N: 10, Name: "старт и добывающие точки", Run: runStartOutposts, Check: checkOutposts})
}

// runStartOutposts — стадия 10 (stages.md): сначала к центру размеченной базы (голова bus-1 —
// рядом месторождения; решение пользователя 27.10), потом старт (дерево, камень, обломки — те,
// что рядом) и добывающие точки вне зон базы.
func runStartOutposts(ctx context.Context, h *act.Hands) error {
	if c, ok := baseCenter(); ok {
		h.Say("к центру базы — голова bus-1 (%d, %d)", c.X, c.Y)
		if err := h.WalkTo(ctx, c.Center(), 3); err != nil && !errors.Is(err, act.ErrNotReached) {
			return err
		}
	}
	if err := runStart(ctx, h); err != nil {
		return err
	}
	return runOutposts(ctx, h)
}

// baseCenter — центр размеченной базы: у головы bus-1 (stages.md, стадия 10: туда первым делом,
// точки — рядом). false — разметки ещё нет.
func baseCenter() (world.Tile, bool) {
	r, err := LoadRough()
	if err != nil {
		return world.Tile{}, false
	}
	for _, z := range r.Zones {
		if z.Name == "bus-1" {
			return world.Tile{X: z.Rect.Min.X - 4, Y: (z.Rect.Min.Y + z.Rect.Max.Y) / 2}, true
		}
	}
	return world.Tile{}, false
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
