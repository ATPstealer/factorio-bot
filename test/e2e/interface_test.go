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
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Защиты: меню игры и карта (мод/ввод не доходят до мира) — бот сам их закрывает.
func TestGuards(t *testing.T) {
	t.Run("menu", checkMenuGuard)
	t.Run("map", checkMapGuard)
}

// Окно инвентаря: E открывает окно персонажа, Esc закрывает (меню игры не открывается).
func TestInventoryWindow(t *testing.T) {
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
func TestPickFromInventory(t *testing.T) {
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

// Рука при заполненной быстрой панели (стадия 10: «клик по ячейке 2 не дал inserter в
// руку»): все предметы инвентаря по кругу, трижды — каждый в руке; кому нет места на панели,
// берётся кликом в окне инвентаря.
func TestPickFullQuickbar(t *testing.T) {
	h := handsT(t)
	p0, _ := h.B.Player()
	var items []string
	for it := range p0.Inventory {
		if h.Protos.Items[it].PlaceResult != "" || len(items) < 12 {
			items = append(items, it)
		}
	}
	sort.Strings(items)
	if len(items) <= len(p0.Quickbar) {
		t.Skipf("в инвентаре %d видов — панель (%d) не заполнить", len(items), len(p0.Quickbar))
	}
	ctx := ctxT(t, 5*time.Minute)
	for round := 0; round < 3; round++ {
		for _, it := range items {
			if err := h.Pick(ctx, it); err != nil {
				t.Fatalf("круг %d, %s: %v", round+1, it, err)
			}
			p, _ := h.B.Player()
			if p.Cursor == nil || p.Cursor.Name != it {
				t.Fatalf("круг %d: в руке %+v, ждали %s", round+1, p.Cursor, it)
			}
			if err := h.ClearCursor(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	clean(t, h)
}

// Полная быстрая панель (стадия 12, пользователь: «каждый раз берёт ленту из инвентаря»):
// предмет не с панели — в руку из инвентаря и на панель по кругу вместо того, что там было;
// второй раз — клавишей (окно инвентаря не открывается). Три таких предмета подряд — в
// соседние слоты по кругу.
func TestQuickbarRotation(t *testing.T) {
	h := handsT(t)
	p0, _ := h.B.Player()
	var off []string // в инвентаре, не на панели
	for it := range p0.Inventory {
		if !slices.Contains(p0.Quickbar[:], it) {
			off = append(off, it)
		}
	}
	sort.Strings(off)
	full := true
	for _, q := range p0.Quickbar {
		full = full && q != ""
	}
	if !full || len(off) < 3 {
		t.Skipf("панель не полна или в инвентаре мало видов не с панели (%d)", len(off))
	}
	ctx := ctxT(t, 3*time.Minute)
	var slots []int
	for _, it := range off[:3] {
		if err := h.Pick(ctx, it); err != nil {
			t.Fatalf("%s: %v", it, err)
		}
		if err := h.ClearCursor(ctx); err != nil {
			t.Fatal(err)
		}
		p, _ := h.B.Player()
		i := slices.Index(p.Quickbar[:], it)
		if i < 0 {
			t.Fatalf("%s не попал на панель: %v", it, p.Quickbar)
		}
		slots = append(slots, i)
		// второй раз — клавишей: окно инвентаря не открывается
		opened := false
		done := make(chan struct{})
		go func() {
			defer close(done)
			for k := 0; k < 20; k++ {
				if q, err := h.B.Player(); err == nil && q.OpenedGUIType != nil && *q.OpenedGUIType != 0 {
					opened = true
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
		if err := h.Pick(ctx, it); err != nil {
			t.Fatalf("%s второй раз: %v", it, err)
		}
		<-done
		if opened {
			t.Errorf("%s второй раз — через окно инвентаря, а не клавишей", it)
		}
		if err := h.ClearCursor(ctx); err != nil {
			t.Fatal(err)
		}
	}
	n := len(p0.Quickbar)
	for k := 1; k < len(slots); k++ {
		if slots[k] != (slots[k-1]+1)%n {
			t.Errorf("слоты не по кругу: %v", slots)
		}
	}
	t.Logf("слоты %v", slots)
	clean(t, h)
}

// Исследование: окно технологий → поиск → «Start research»; в игре оно текущее или в
// очереди. Берётся доступная наукой технология (предпосылки изучены).
func TestStartResearch(t *testing.T) {
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
func TestSetRecipe(t *testing.T) {
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
func TestSetFilter(t *testing.T) {
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

// Рецепт со словом поиска «inserter» (стадия 32 на 859088572: набранное слово с кареткой
// доставало до полосы, по которой ищется поле поиска, — поле не находилось, клик на ряд ниже;
// при трёх вкладках совпадало случайно, при четырёх — мимо): инсертер и длинный инсертер.
func TestSetRecipeInserter(t *testing.T) {
	h := handsT(t)
	have(t, h, "assembling-machine-1", 1)
	e := placeNear(t, h, "assembling-machine-1", 3)
	ctx := ctxT(t, 2*time.Minute)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, rec := range []string{"inserter", "long-handed-inserter", "fast-inserter"} {
		if !slices.Contains(f.Recipes, rec) {
			continue
		}
		n++
		if err := h.SetRecipe(ctx, e, rec); err != nil {
			t.Fatalf("%s: %v", rec, err)
		}
		if st, err := h.B.GetEntity(e.Name, e.Position); err != nil || st == nil || st.Recipe != rec {
			t.Errorf("рецепт в игре: %+v (%v), ждали %s", st, err, rec)
		}
	}
	if n == 0 {
		t.Skip("рецептов инсертеров нет")
	}
	if err := h.Pickup(ctx, e); err != nil {
		t.Fatal(err)
	}
	clean(t, h)
}

// Слить сеть труб кнопкой в окне цистерны (стадия 34 на 1028292478: вода в цистернах и трубах
// нефти — сеть закрыта для нефти): цистерна с водой от помпы, труба к помпе снята — сливаю, в
// цистерне пусто. Нужны цистерна (fluid-handling), помпа и вода рядом — нет: пропуск.
func TestFlushPipeline(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	if p.Inventory["storage-tank"] < 1 && !slices.Contains(f.Recipes, "storage-tank") {
		t.Skip("цистерны нет (fluid-handling) — что добавить на карты")
	}
	have(t, h, "storage-tank", 1)
	have(t, h, "offshore-pump", 1)
	have(t, h, "pipe", 10)
	ctx := ctxT(t, 6*time.Minute)
	l := layout.New(h.Protos, h.World)
	pump, err := l.WaterPump(world.TileOf(p.Position), "test", nil)
	if err != nil {
		t.Skipf("нет воды рядом: %v", err)
	}
	conn := layout.Connections(h.Protos, pump)[0]
	pe, err := h.Place(ctx, "offshore-pump", pump.Pos, pump.Dir)
	if err != nil {
		t.Fatalf("помпа: %v", err)
	}
	// труба от выхода помпы — 3 тайла по ходу, цистерна за ней
	var pipes []bridge.Pos
	tl := conn.To
	for i := 0; i < 3; i++ {
		pipes = append(pipes, tl.Center())
		tl = layout.Step(tl, conn.Dir)
	}
	if _, err := h.PlaceAll(ctx, "pipe", pipes, act.North); err != nil {
		t.Fatalf("трубы: %v", err)
	}
	tank := layout.Attach(h.Protos, layout.Conn{At: layout.Step(tl, (conn.Dir+8)%16), To: tl, Dir: conn.Dir}, "storage-tank", "", "test")
	if len(tank) == 0 {
		t.Skip("цистерна не встаёт у трубы")
	}
	te, err := h.Place(ctx, "storage-tank", tank[0].Pos, tank[0].Dir)
	if err != nil {
		t.Skipf("цистерна не встала: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, _ := h.B.GetEntity(te.Name, te.Position)
		if st != nil && st.Fluids["water"] > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("вода в цистерну не пришла")
		}
		time.Sleep(time.Second)
	}
	// отрезать помпу: снять трубу у неё, иначе вода сразу натечёт снова
	if e, _ := h.B.EntityAt("pipe", pipes[0]); e != nil {
		if err := h.Pickup(ctx, *e); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.FlushPipeline(ctx, te, "water"); err != nil {
		t.Fatalf("слить: %v", err)
	}
	if st, _ := h.B.GetEntity(te.Name, te.Position); st == nil || st.Fluids["water"] > 0 {
		t.Errorf("в цистерне осталось: %+v", st)
	}
	_ = pe
	clean(t, h)
}
