//go:build e2e

// Интерфейс: окна, рука, исследования, рецепт и фильтр — ввод в окна игры и подтверждение
// модом; меню и карта не мешают действиям.
package e2e

import (
	"slices"
	"sort"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// Защиты: меню игры и карта (мод/ввод не доходят до мира) — бот сам их закрывает.
func TestBase_Guards(t *testing.T) {
	t.Run("menu", checkMenuGuard)
	t.Run("map", checkMapGuard)
}

// Окно инвентаря: E открывает окно персонажа, Esc закрывает (меню игры не открывается).
func TestBase_InventoryWindow(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 30*time.Second)
	if err := h.OpenInventory(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.B.Player(); p.OpenedGUI != "controller" {
		t.Fatalf("открыто %q, ждали инвентарь", p.OpenedGUI)
	}
	if err := h.CloseGUI(ctx); err != nil {
		t.Fatal(err)
	}
	clean(t, h)
}

// Рука: взять из инвентаря предмет, который там есть (хоткей панели или клик в окне
// инвентаря с привязкой к панели) — он в руке; Q — рука пуста, в инвентаре столько же.
func TestBase_PickFromInventory(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 30*time.Second)
	have(t, h, "wood", 1)
	p0, _ := h.B.Player()
	// что-то не с панели (проверить и поиск в окне инвентаря), нет — что угодно
	var items []string
	for it := range p0.Inventory {
		items = append(items, it)
	}
	sort.Strings(items)
	item := items[0]
	for _, it := range items {
		if !slices.Contains(p0.Quickbar, it) {
			item = it
			break
		}
	}
	if err := h.Pick(ctx, item); err != nil {
		t.Fatal(err)
	}
	p1, _ := h.B.Player()
	if p1.Cursor == nil || p1.Cursor.Name != item {
		t.Fatalf("в руке %+v, ждали %s", p1.Cursor, item)
	}
	if err := h.ClearCursor(ctx); err != nil {
		t.Fatal(err)
	}
	p2, _ := h.B.Player()
	if p2.Cursor != nil || p2.Inventory[item] != p0.Inventory[item] {
		t.Errorf("после Q: рука %+v, %s %d (было %d)", p2.Cursor, item, p2.Inventory[item], p0.Inventory[item])
	}
	clean(t, h)
}

// Исследование: окно технологий → поиск → «Start research»; в игре оно текущее или в
// очереди. Берётся доступная наукой технология (предпосылки изучены).
func TestBase_StartResearch(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{}
	for _, r := range f.Researched {
		done[r] = true
	}
	var names []string
	for n := range h.Protos.Technologies {
		names = append(names, n)
	}
	sort.Strings(names)
	tech := ""
	for _, n := range names {
		te := h.Protos.Technologies[n]
		if done[n] || te.Hidden || !te.Enabled || te.Trigger != nil || len(te.UnitIngredients) == 0 || n == f.Current || slices.Contains(f.Queue, n) {
			continue
		}
		ok := true
		for _, pre := range te.Prerequisites {
			ok = ok && done[pre]
		}
		if ok {
			tech = n
			break
		}
	}
	if tech == "" {
		t.Skip("нет доступной технологии")
	}
	if err := h.StartResearch(ctxT(t, time.Minute), tech); err != nil {
		t.Fatal(err)
	}
	f2, _ := h.B.Force()
	if f2.Current != tech && !slices.Contains(f2.Queue, tech) {
		t.Errorf("%s не запущено: текущее %q, очередь %v", tech, f2.Current, f2.Queue)
	}
	clean(t, h)
}

// placeNear — поставить item на свободном месте рядом (для окон сущности).
func placeNear(t *testing.T, h *act.Hands, item string, size int) bridge.Entity {
	t.Helper()
	c := freeArea(t, h, size+2)
	e, err := h.Place(ctxT(t, time.Minute), item, c, act.North)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Сборщик: поставить, задать рецепт (клик → окно выбора → поиск → рецепт), поменять на
// другой (кнопка смены рецепта); подтверждение — мод; поднять.
func TestBase_SetRecipe(t *testing.T) {
	h := handsT(t)
	have(t, h, "assembling-machine-1", 1)
	e := placeNear(t, h, "assembling-machine-1", 3)
	ctx := ctxT(t, 2*time.Minute)
	for _, rec := range []string{"iron-gear-wheel", "copper-cable"} {
		if err := h.SetRecipe(ctx, e, rec); err != nil {
			t.Fatalf("%s: %v", rec, err)
		}
		if st, err := h.B.GetEntity(e.Name, e.Position); err != nil || st == nil || st.Recipe != rec {
			t.Errorf("рецепт в игре: %+v (%v), ждали %s", st, err, rec)
		}
	}
	if err := h.Pickup(ctx, e); err != nil {
		t.Fatal(err)
	}
	clean(t, h)
}

// Фильтр инсертера: окно инсертера → «Use filters» → ячейка → поиск → предмет;
// подтверждение — мод.
func TestBase_SetFilter(t *testing.T) {
	h := handsT(t)
	have(t, h, "inserter", 1)
	e := placeNear(t, h, "inserter", 1)
	ctx := ctxT(t, time.Minute)
	if err := h.SetFilter(ctx, e, "coal"); err != nil {
		t.Fatal(err)
	}
	if st, err := h.B.GetEntity(e.Name, e.Position); err != nil || st == nil || len(st.Filters) == 0 || st.Filters[0] != "coal" {
		t.Errorf("фильтр в игре: %+v (%v)", st, err)
	}
	if err := h.Pickup(ctx, e); err != nil {
		t.Fatal(err)
	}
	clean(t, h)
}
