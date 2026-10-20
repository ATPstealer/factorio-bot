//go:build e2e

// Ходьба: до точки, в обход воды, из кольца своих построек.
package e2e

import (
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
	"math"
	"testing"
	"time"
)

// Ходьба туда и обратно с точностью 0.5 тайла.
func TestWalk(t *testing.T) {
	h := handsT(t)
	start, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t, 30*time.Second)
	for _, target := range []bridge.Pos{
		{X: start.Position.X + 6, Y: start.Position.Y + 3},
		start.Position,
	} {
		if err := h.WalkTo(ctx, target, 0.5); err != nil {
			t.Fatal(err)
		}
		p, err := h.B.Player()
		if err != nil {
			t.Fatal(err)
		}
		d := dist(p.Position, target)
		t.Logf("цель (%.2f, %.2f), стою (%.2f, %.2f), ошибка %.2f", target.X, target.Y, p.Position.X, p.Position.Y, d)
		if d > 0.5 {
			t.Errorf("до цели %.2f > 0.5", d)
		}
		if p.Character.Walking {
			t.Error("персонаж не остановился")
		}
	}
}

// Выход из ловушки: персонажа обступили кольцом из 8 сундуков, дальше одна команда —
// «иди туда» за кольцо; как выйти, бот решает сам. Проверка — дошёл.
func TestUnstuck(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 5*time.Minute)
	have(t, h, "wooden-chest", 8)
	c := freeArea(t, h, 7)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	var ring []bridge.Pos
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx != 0 || dy != 0 {
				ring = append(ring, world.Tile{X: ct.X + dx, Y: ct.Y + dy}.Center())
			}
		}
	}
	if _, err := h.PlaceAll(ctx, "wooden-chest", ring, act.North); err != nil {
		t.Fatal(err)
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	if p, _ = h.B.Player(); !h.World.Enclosed(p.Position) {
		t.Fatalf("кольцо не замкнуто: персонаж в (%.2f, %.2f)", p.Position.X, p.Position.Y)
	}
	target := bridge.Pos{X: ct.Center().X + 8, Y: ct.Center().Y}
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	p, _ = h.B.Player()
	if d := math.Hypot(p.Position.X-target.X, p.Position.Y-target.Y); d > 0.5 {
		t.Errorf("до цели осталось %.2f", d)
	}
	left := 0
	for _, pos := range ring {
		if e, _ := h.B.EntityAt("wooden-chest", pos); e != nil {
			left++
		}
	}
	t.Logf("сундуков кольца на месте: %d из %d", left, len(ring))
	clean(t, h)
}

// Путь A* в обход ближайшего к игроку водоёма (не меньше 100 тайлов, открыт целиком): точки по разные
// стороны воды, путь с поворотами, дойти по нему.
func TestPathAroundLake(t *testing.T) {
	h := handsT(t)
	w := h.World
	if _, err := w.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	// водоёмы — связные (4 соседа) области воды
	water := map[world.Tile]bool{}
	for _, c := range w.Chunks {
		for i := range c.Tiles {
			if c.Tiles[i] == world.Water {
				water[world.Tile{X: c.Pos.X*world.ChunkSize + i%world.ChunkSize, Y: c.Pos.Y*world.ChunkSize + i/world.ChunkSize}] = true
			}
		}
	}
	seen := map[world.Tile]bool{}
	best, bestD := []world.Tile(nil), math.Inf(1)
	for t0 := range water {
		if seen[t0] {
			continue
		}
		lake, queue := []world.Tile{}, []world.Tile{t0}
		seen[t0] = true
		near, open := math.Inf(1), false
		for len(queue) > 0 {
			q := queue[0]
			queue = queue[1:]
			lake = append(lake, q)
			near = math.Min(near, math.Hypot(float64(q.X)-p0.Position.X, float64(q.Y)-p0.Position.Y))
			for _, d := range []world.Tile{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
				n := world.Tile{X: q.X + d.X, Y: q.Y + d.Y}
				if water[n] && !seen[n] {
					seen[n] = true
					queue = append(queue, n)
				}
				if w.TileAt(n) == world.Unknown {
					open = true // уходит за край открытой карты — берега не видно
				}
			}
		}
		if len(lake) >= 100 && !open && near < bestD {
			best, bestD = lake, near
		}
	}
	if best == nil {
		t.Skip("нет водоёма")
	}
	// крайние тайлы воды слева и справа — точки на берегу за ними
	wt, et := best[0], best[0]
	for _, q := range best {
		if q.X < wt.X {
			wt = q
		}
		if q.X > et.X {
			et = q
		}
	}
	t.Logf("водоём: %d тайлов, ближе всего %.0f тайлов, X %d…%d", len(best), bestD, wt.X, et.X)
	west, okW := w.FreeSpot(bridge.Pos{X: float64(wt.X) - 3, Y: float64(wt.Y) + 0.5}, 1, 1, 10, nil)
	east, okE := w.FreeSpot(bridge.Pos{X: float64(et.X) + 4, Y: float64(et.Y) + 0.5}, 1, 1, 10, nil)
	if !okW || !okE {
		t.Skip("нет проходимых точек у озера")
	}
	pts, err := w.Path(west, east, 0.5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) < 2 {
		t.Errorf("путь через озеро без поворотов: %v", pts)
	}
	ctx := ctxT(t, 3*time.Minute)
	for _, target := range []bridge.Pos{west, east} {
		if err := h.WalkTo(ctx, target, 0.5); err != nil {
			t.Fatal(err)
		}
		p, _ := h.B.Player()
		if d := math.Hypot(p.Position.X-target.X, p.Position.Y-target.Y); d > 0.5 {
			t.Errorf("до %v осталось %.2f", target, d)
		}
	}
	clean(t, h)
}

// Разведка: идти к краю открытой карты, пока не откроется ещё 10 чанков.
func TestExplore(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	start := len(h.World.Chunks)
	err := h.Explore(ctxT(t, 5*time.Minute), func() bool { return len(h.World.Chunks) >= start+10 })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("открыто чанков: %d → %d", start, len(h.World.Chunks))
	clean(t, h)
}
