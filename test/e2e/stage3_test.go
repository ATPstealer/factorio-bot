//go:build e2e

// Стадия 3 — действия через ввод: крафт, рука/быстрая панель, постройка, вложить/забрать,
// подобрать, поворот, путь A*. Критерий: камень → печь → поставить → уголь и руда → пластины.
package e2e

import (
	"math"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// clean — после действия бот ничего не держит: нет окон, рука пуста, не идёт и не копает.
func clean(t *testing.T, h *act.Hands) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	p, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	if p.OpenedGUI != "none" || p.RenderMode != "game" || p.Cursor != nil || p.Character.Walking || p.Character.Mining {
		t.Errorf("после действия: окно %q, вид %q, рука %+v, walking=%v, mining=%v", p.OpenedGUI, p.RenderMode, p.Cursor, p.Character.Walking, p.Character.Mining)
	}
	if open, err := h.MenuOpen(); err != nil || open {
		t.Errorf("открыто меню игры (%v)", err)
	}
}

func TestStage3_Craft(t *testing.T) {
	h := handsT(t)
	have(t, h, "wood", 2)
	p0, _ := h.B.Player()
	if err := h.CraftWait(ctxT(t, 30*time.Second), "wooden-chest", 1); err != nil {
		t.Fatal(err)
	}
	p1, _ := h.B.Player()
	if d := p1.Inventory["wooden-chest"] - p0.Inventory["wooden-chest"]; d != 1 {
		t.Errorf("wooden-chest +%d", d)
	}
	if d := p0.Inventory["wood"] - p1.Inventory["wood"]; d != 2 {
		t.Errorf("wood −%d, ждали −2", d)
	}
	clean(t, h)
}

// Путь A* в обход озера: точки по разные стороны воды.
func TestStage3_PathAroundLake(t *testing.T) {
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

// Критерий стадии: добыть камень → скрафтить печь → поставить → уголь и руда → забрать пластины.
func TestStage3_FurnaceScenario(t *testing.T) {
	scenario(t)
	h := handsT(t)
	have(t, h, "stone", 5)
	have(t, h, "coal", 3)
	have(t, h, "iron-ore", 5)
	ctx := ctxT(t, 3*time.Minute)

	p0, _ := h.B.Player()
	if err := h.CraftWait(ctx, "stone-furnace", 1); err != nil {
		t.Fatal(err)
	}
	spot := freeArea(t, h, 2)
	f, err := h.Place(ctx, "stone-furnace", spot, act.North)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Insert(ctx, f, "coal", 2); err != nil {
		t.Fatal(err)
	}
	if err := h.Insert(ctx, f, "iron-ore", 5); err != nil {
		t.Fatal(err)
	}
	// 5 × 3.2 с плавки при скорости 1
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := h.B.GetEntity(f.Name, f.Position)
		if err != nil {
			t.Fatal(err)
		}
		if st.Inventories["crafter_output"]["iron-plate"] >= 5 {
			break
		}
		time.Sleep(time.Second)
	}
	got, err := h.Take(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got["iron-plate"] < 5 {
		t.Errorf("забрано %v, ждали iron-plate ≥5", got)
	}
	p1, _ := h.B.Player()
	if d := p1.Inventory["iron-plate"] - p0.Inventory["iron-plate"]; d < 5 {
		t.Errorf("iron-plate +%d", d)
	}
	t.Logf("печь в %v, забрано %v", spot, got)
	clean(t, h)
}

// Меню игры (Esc без окна) мод не видит: бот распознаёт его по снимку и закрывает.
// Регрессия: тесты висели под открытым меню.
func checkMenuGuard(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 30*time.Second)
	if err := h.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	if open, err := h.MenuOpen(); err != nil || open {
		t.Fatalf("меню открыто до теста (%v)", err)
	}
	if err := h.Focus(); err != nil {
		t.Fatal(err)
	}
	if err := h.In.Tap("escape"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if open, err := h.MenuOpen(); err != nil || !open {
		t.Fatalf("меню не распознано после Esc (%v)", err)
	}
	// действие под меню: WalkTo должен сам закрыть меню и дойти
	target := walkTarget(t, h)
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	clean(t, h)
}

// Регрессия: бот оказался на карте (Ctrl+F без окна открывает поиск по карте) и не вышел.
// Карту мод видит (render_mode) — guard закрывает её перед действием.
func checkMapGuard(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 30*time.Second)
	if err := h.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Focus(); err != nil {
		t.Fatal(err)
	}
	if err := h.In.Tap("tab"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.B.Player(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if p, _ := h.B.Player(); p.RenderMode == "game" {
		t.Fatalf("Tab не открыл карту: %q", p.RenderMode)
	}
	target := walkTarget(t, h)
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.B.Player(); p.RenderMode != "game" {
		t.Errorf("карта не закрыта: %q", p.RenderMode)
	}
	clean(t, h)
}

// walkTarget — проходимая точка в нескольких тайлах от игрока (центр свободного тайла).
func walkTarget(t *testing.T, h *act.Hands) bridge.Pos {
	t.Helper()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	for r := 3; r <= 8; r++ {
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}} {
			c := world.TileOf(bridge.Pos{X: p.Position.X + float64(d[0]*r), Y: p.Position.Y + float64(d[1]*r)})
			if pts, err := h.World.Path(p.Position, c.Center(), 0.5, 20000); err == nil && len(pts) > 0 {
				return c.Center()
			}
		}
	}
	t.Skip("рядом нет достижимой точки")
	return bridge.Pos{}
}

// Защиты: меню игры и карта (мод/ввод не доходят до мира) — бот сам их закрывает.
func TestStage3_Guards(t *testing.T) {
	t.Run("menu", checkMenuGuard)
	t.Run("map", checkMapGuard)
}
