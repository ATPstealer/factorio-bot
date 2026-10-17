//go:build e2e

// Стадия 3 — действия через ввод: крафт, рука/быстрая панель, постройка, вложить/забрать,
// подобрать, поворот, путь A*. Критерий: камень → печь → поставить → уголь и руда → пластины.
package e2e

import (
	"context"
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

// ensure — добыть, пока предмета меньше n (target — что копать: tree, stone, coal…).
func ensure(t *testing.T, h *act.Hands, item, target string, n int) {
	t.Helper()
	ctx := ctxT(t, 3*time.Minute) // общий лимит: под открытым меню или без целей — не висеть
	for i := 0; i < 30; i++ {
		p, err := h.B.Player()
		if err != nil {
			t.Fatal(err)
		}
		if p.Inventory[item] >= n {
			return
		}
		k := n - p.Inventory[item]
		if target == "tree" || target == "rock" {
			k = 1
		}
		if _, err := h.Gather(ctx, target, k, 150); err != nil {
			t.Fatalf("добыть %s: %v", item, err)
		}
	}
	t.Fatalf("не набрал %s ×%d", item, n)
}

func freeSpot(t *testing.T, h *act.Hands, size int) bridge.Pos {
	t.Helper()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	near := bridge.Pos{X: p.Position.X + 3, Y: p.Position.Y}
	spot, ok := h.World.FreeSpot(near, size, size, 12, &p.Position)
	if !ok {
		t.Fatal("нет свободного места рядом")
	}
	return spot
}

func TestStage3_Craft(t *testing.T) {
	h := handsT(t)
	ensure(t, h, "wood", "tree", 2)
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

// Крафт с промежуточными: бур требует шестерни — игра сама ставит их в очередь.
func TestStage3_CraftWithIntermediates(t *testing.T) {
	h := handsT(t)
	p0, _ := h.B.Player()
	if p0.Inventory["iron-plate"] < 9 || p0.Inventory["stone"] < 5 {
		t.Skipf("нужно 9 железных пластин и 5 камня, есть %d и %d", p0.Inventory["iron-plate"], p0.Inventory["stone"])
	}
	if err := h.CraftWait(ctxT(t, 60*time.Second), "burner-mining-drill", 1); err != nil {
		t.Fatal(err)
	}
	p1, _ := h.B.Player()
	if p1.Inventory["burner-mining-drill"] != p0.Inventory["burner-mining-drill"]+1 {
		t.Errorf("бур: %d → %d", p0.Inventory["burner-mining-drill"], p1.Inventory["burner-mining-drill"])
	}
	clean(t, h)
}

func TestStage3_PickQuickbar(t *testing.T) {
	h := handsT(t)
	ensure(t, h, "wood", "tree", 1)
	ctx := ctxT(t, 30*time.Second)
	for i := 0; i < 2; i++ { // первый раз — из инвентаря с привязкой, второй — хоткеем
		if err := h.Pick(ctx, "wood"); err != nil {
			t.Fatal(err)
		}
		p, _ := h.B.Player()
		if p.Cursor == nil || p.Cursor.Name != "wood" {
			t.Fatalf("в руке %+v", p.Cursor)
		}
		found := false
		for _, q := range p.Quickbar {
			found = found || q == "wood"
		}
		if !found {
			t.Errorf("wood не на быстрой панели: %v", p.Quickbar)
		}
		if err := h.ClearCursor(ctx); err != nil {
			t.Fatal(err)
		}
	}
	clean(t, h)
}

// Поставить с поворотом, проверить направление, подобрать.
func TestStage3_PlaceRotatePickup(t *testing.T) {
	h := handsT(t)
	p0, _ := h.B.Player()
	if p0.Inventory["burner-mining-drill"] == 0 {
		t.Skip("нет burner-mining-drill")
	}
	ctx := ctxT(t, 60*time.Second)
	// в 2.0 бур ставится только на ресурсы: площадка 2×2 целиком на руде, ближайшая к игроку
	var spot bridge.Pos
	found := false
	for _, pt := range h.World.Patches() {
		if pt.Name == "crude-oil" || len(pt.Tiles) < 20 {
			continue
		}
		onOre := func(tl world.Tile) bool { _, ok := h.World.ResourceAt(tl); return ok }
		if s, ok := h.World.FreeSpotWhere(pt.Center, 2, 2, 20, &p0.Position, onOre); ok {
			spot, found = s, true
			break
		}
	}
	if !found {
		t.Skip("нет свободной площадки 2×2 на руде")
	}
	e, err := h.Place(ctx, "burner-mining-drill", spot, act.East)
	if err != nil {
		t.Fatal(err)
	}
	st, err := h.B.GetEntity(e.Name, e.Position)
	if err != nil || st == nil {
		t.Fatalf("постройки нет: %v", err)
	}
	if st.Position != spot || st.Direction == nil || *st.Direction != act.East || !st.Own {
		t.Errorf("поставлено %+v, ждали в %v на восток", st.Entity, spot)
	}
	if err := h.Pickup(ctx, e); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.B.GetEntity(e.Name, e.Position); st != nil {
		t.Error("постройка не подобрана")
	}
	p1, _ := h.B.Player()
	if p1.Inventory["burner-mining-drill"] != p0.Inventory["burner-mining-drill"] {
		t.Errorf("бур в инвентаре %d → %d", p0.Inventory["burner-mining-drill"], p1.Inventory["burner-mining-drill"])
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
	h := handsT(t)
	ensure(t, h, "stone", "stone", 5)
	ensure(t, h, "coal", "coal", 3)
	ensure(t, h, "iron-ore", "iron-ore", 5)
	ctx := ctxT(t, 3*time.Minute)

	p0, _ := h.B.Player()
	if err := h.CraftWait(ctx, "stone-furnace", 1); err != nil {
		t.Fatal(err)
	}
	spot := freeSpot(t, h, 2)
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
	if err := h.Pickup(ctx, f); err != nil {
		t.Fatal(err)
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
func TestStage3_MenuGuard(t *testing.T) {
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

// Регрессия: копая руду рядом со своей печью, бот не должен её разобрать
// (ПКМ над постройкой добывает постройку).
func TestStage3_MineOreNextToBuilding(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 3*time.Minute)
	p0, _ := h.B.Player()
	if p0.Inventory["stone-furnace"] == 0 {
		ensure(t, h, "stone", "stone", 5)
		if err := h.CraftWait(ctx, "stone-furnace", 1); err != nil {
			t.Fatal(err)
		}
	}
	// площадка 2×2 на железной руде
	var spot bridge.Pos
	found := false
	for _, pt := range h.World.Patches() {
		if pt.Name != "iron-ore" || len(pt.Tiles) < 50 {
			continue
		}
		onOre := func(tl world.Tile) bool { _, ok := h.World.ResourceAt(tl); return ok }
		if s, ok := h.World.FreeSpotWhere(pt.Center, 2, 2, 25, &p0.Position, onOre); ok {
			spot, found = s, true
			break
		}
	}
	if !found {
		t.Skip("нет площадки на железной руде")
	}
	f, err := h.Place(ctx, "stone-furnace", spot, act.North)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Pickup(context.Background(), f)
	// встать вплотную к печи и копать ближайшую руду — она вокруг печи
	if err := h.WalkTo(ctx, bridge.Pos{X: spot.X, Y: spot.Y + 2}, 0.5); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Gather(ctx, "iron-ore", 6, 10); err != nil {
		t.Fatal(err)
	}
	if st, err := h.B.GetEntity(f.Name, f.Position); err != nil || st == nil {
		t.Fatalf("печь рядом с рудой разобрана (%v)", err)
	}
	clean(t, h)
}

// Регрессия: бот оказался на карте (Ctrl+F без окна открывает поиск по карте) и не вышел.
// Карту мод видит (render_mode) — guard закрывает её перед действием.
func TestStage3_MapGuard(t *testing.T) {
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
	for _, d := range []bridge.Pos{{X: 4}, {X: -4}, {Y: 4}, {Y: -4}, {X: 3, Y: 3}, {X: -3, Y: -3}} {
		c := world.TileOf(bridge.Pos{X: p.Position.X + d.X, Y: p.Position.Y + d.Y})
		if pts, err := h.World.Path(p.Position, c.Center(), 0.5, 2000); err == nil && len(pts) > 0 {
			return c.Center()
		}
	}
	t.Skip("рядом нет достижимой точки")
	return bridge.Pos{}
}
