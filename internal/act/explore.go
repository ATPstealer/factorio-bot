package act

import (
	"context"
	"errors"
	"fmt"
	"math"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// exploreSight — на сколько чанков вокруг персонаж открывает карту (по наблюдению ~3),
// exploreHop — длина перехода (путь строится только по открытой карте).
const exploreSight, exploreHop = 3, 48

// Explore — разведка: идти к краю открытой карты кольцами вокруг (0, 0) (World.ExploreRing,
// на exploreSight чанков глубже края), пока done не вернёт true. Точку, к которой не пройти
// (вода, утёсы), пропускает. Всё открыто — ошибка.
func (h *Hands) Explore(ctx context.Context, done func() bool) error {
	if h.World == nil {
		return fmt.Errorf("act: разведка без модели карты")
	}
	w := h.World
	skip := map[bridge.ChunkPos]bool{}
	start, ring0 := len(w.Chunks), -1
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
		ring, pts := w.ExploreRing(exploreSight, 128, skip)
		if ring == 0 {
			return fmt.Errorf("act: разведка: всё открыто")
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		dist := func(a bridge.Pos) float64 { return math.Hypot(a.X-p.Position.X, a.Y-p.Position.Y) }
		target := pts[0]
		for _, pt := range pts {
			if dist(pt) < dist(target) {
				target = pt
			}
		}
		if ring != ring0 {
			ring0 = ring
			h.Say("разведка: квадрат ±%d вокруг (0, 0)", ring*world.ChunkSize)
		}
		// шаг к цели по открытой земле
		d := dist(target)
		var spot bridge.Pos
		ok := false
		for _, hop := range []float64{exploreHop, 32, 16, 8} {
			k := math.Min(1, hop/d)
			pt := bridge.Pos{X: p.Position.X + (target.X-p.Position.X)*k, Y: p.Position.Y + (target.Y-p.Position.Y)*k}
			if spot, ok = w.FreeSpot(pt, 1, 1, 6, nil); ok {
				break
			}
		}
		moved := false
		if ok {
			if err := h.WalkTo(ctx, spot, 3); err != nil && !errors.Is(err, ErrNotReached) {
				if ctx.Err() != nil {
					return err
				}
				h.Log("разведка: %v", err) // застрял — другой точкой
			}
			q, err := h.B.Player()
			moved = err == nil && math.Hypot(q.Position.X-p.Position.X, q.Position.Y-p.Position.Y) >= 4
		}
		if !moved { // не пройти — эту точку пропустить
			skip[world.ChunkOf(world.TileOf(target))] = true
		}
	}
}
