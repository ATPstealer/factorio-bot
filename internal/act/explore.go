package act

import (
	"context"
	"errors"
	"fmt"
	"math"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// exploreHop — длина перехода (путь строится только по открытой карте).
const exploreHop = 48

// ErrNothingToExplore — неоткрытого, куда можно дойти, рядом нет (в пределах 64 чанков или
// области ExploreWhere; недоступное — вода, утёсы — пропущено).
var ErrNothingToExplore = errors.New("act: разведка: неоткрытого, куда дойти, нет")

// Explore — разведка: идти к ближайшему неоткрытому чанку на краю открытой карты
// (World.NearestUnknown), пока done не вернёт true. Дошёл, а чанк не открылся, или не пройти
// (вода, утёсы) — этот чанк пропускается. Неоткрытого рядом нет — ошибка.
func (h *Hands) Explore(ctx context.Context, done func() bool) error {
	return h.ExploreWhere(ctx, nil, done)
}

// ExploreWhere — Explore только к чанкам, для которых inside (nil — любые): обойти область.
func (h *Hands) ExploreWhere(ctx context.Context, inside func(bridge.ChunkPos) bool, done func() bool) error {
	if h.World == nil {
		return fmt.Errorf("act: разведка без модели карты")
	}
	w := h.World
	skip := map[bridge.ChunkPos]bool{}
	start := len(w.Chunks)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := w.Sync(h.B, false); err != nil {
			return err
		}
		if done() {
			h.Log("разведка: открыто чанков %d → %d", start, len(w.Chunks))
			return nil
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		target, ok := w.NearestUnknownWhere(p.Position, 64, skip, inside)
		if !ok {
			return ErrNothingToExplore
		}
		tc := world.ChunkOf(world.TileOf(target))
		h.Say("разведка: к неоткрытому чанку (%.0f, %.0f)", target.X, target.Y)
		// шаг к цели по открытой земле
		dist := func(a bridge.Pos) float64 { return math.Hypot(a.X-p.Position.X, a.Y-p.Position.Y) }
		d := dist(target)
		var spot bridge.Pos
		found := false
		for _, hop := range []float64{exploreHop, 32, 16, 8} {
			k := math.Min(1, hop/d)
			pt := bridge.Pos{X: p.Position.X + (target.X-p.Position.X)*k, Y: p.Position.Y + (target.Y-p.Position.Y)*k}
			if spot, found = w.FreeSpot(pt, 1, 1, 6, nil); found {
				break
			}
		}
		before := len(w.Chunks)
		moved, near := false, false
		if found {
			if err := h.WalkTo(ctx, spot, 3); err != nil && !errors.Is(err, ErrNotReached) {
				if ctx.Err() != nil {
					return err
				}
				h.Log("разведка: %v", err)
			}
			if q, err := h.B.Player(); err == nil {
				moved = dist(q.Position) >= 4
				near = math.Hypot(q.Position.X-target.X, q.Position.Y-target.Y) < exploreHop
			}
		}
		if _, err := w.Sync(h.B, false); err != nil {
			return err
		}
		// не пройти или подошли, а карта не прибавилась — к этому чанку больше не идти
		if !moved || len(w.Chunks) == before && near {
			skip[tc] = true
		}
	}
}
