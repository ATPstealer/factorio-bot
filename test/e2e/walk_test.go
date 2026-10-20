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

// Выход из ловушки: персонажа обступили кольцом из 8 сундуков — идти наружу: поднять
// сундук, выйти, поставить назад; все сундуки на месте.
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
	for _, pos := range ring {
		if e, _ := h.B.EntityAt("wooden-chest", pos); e == nil {
			t.Errorf("сундука в %v нет — не поставлен назад", pos)
		}
	}
	clean(t, h)
}

// Путь A* в обход озера: точки по разные стороны воды.
func TestPathAroundLake(t *testing.T) {
	h := handsT(t)
	w := h.World
	// самый большой водоём открытой карты
	var minX, maxX, sumY, n = math.MaxInt, math.MinInt, 0, 0
	for _, c := range w.Chunks {
		for i := range c.Tiles {
			if c.Tiles[i] == world.Water {
				x := c.Pos.X*world.ChunkSize + i%world.ChunkSize
				minX, maxX = min(minX, x), max(maxX, x)
				sumY += c.Pos.Y*world.ChunkSize + i/world.ChunkSize
				n++
			}
		}
	}
	if n < 100 {
		t.Skip("нет водоёма")
	}
	cy := float64(sumY) / float64(n)
	west, okW := w.FreeSpot(bridge.Pos{X: float64(minX) - 4, Y: cy}, 1, 1, 10, nil)
	east, okE := w.FreeSpot(bridge.Pos{X: float64(maxX) + 4, Y: cy}, 1, 1, 10, nil)
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
