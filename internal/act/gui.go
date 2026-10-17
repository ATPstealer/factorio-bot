package act

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
	"factorio-bot/internal/knowledge"
)

// Интерфейс — самая хрупкая часть: где можно — хоткеи, клики только в фиксированные
// точки, каждый шаг подтверждается состоянием из мода.

type point struct{ X, Y int }

// Layout — координаты элементов интерфейса относительно окна клиента.
// Сняты со скриншотов (bot shot) для окна 1856x1016, UI scale 1.
type Layout struct {
	InvSlot1   point     // центр первой ячейки инвентаря (окно E)
	CraftSlot1 point     // центр первой ячейки крафта (окно E, после поиска)
	SlotStep   int       // шаг ячеек
	Cols       int       // ячеек в строке (инвентарь и крафт)
	Quickbar   [10]point // ряд 1 быстрой панели: клавиши 1..0
	// Меню игры (Esc без открытого окна) мод не видит — узнаём по зелёной кнопке Resume
	// (два оттенка: обычный и под курсором).
	MenuProbe  []point
	MenuColors []color.RGBA
	// Focus — куда ставить курсор перед клавишами: в окне, но не над меню/окнами
	// (центр окна — кнопка «Save game» меню игры).
	Focus point
}

var Layout1856 = Layout{
	InvSlot1:   point{530, 350},
	CraftSlot1: point{966, 390},
	SlotStep:   40,
	Cols:       10,
	Quickbar: [10]point{{745, 946}, {785, 946}, {826, 946}, {866, 946}, {906, 946},
		{950, 946}, {990, 946}, {1030, 946}, {1070, 946}, {1110, 946}},
	MenuProbe:  []point{{880, 413}, {927, 400}},
	MenuColors: []color.RGBA{{94, 182, 99, 255}, {146, 232, 151, 255}},
	Focus:      point{60, 300},
}

var quickbarKeys = [10]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}

// at — курсор в точку окна клиента.
func (h *Hands) at(p point) error { return h.In.Move(h.Win.X+p.X, h.Win.Y+p.Y) }

// waitFor опрашивает игрока, пока cond не станет true (не дольше d).
func (h *Hands) waitFor(ctx context.Context, d time.Duration, cond func(bridge.Player) bool) (bridge.Player, error) {
	deadline := time.Now().Add(d)
	for {
		p, err := h.B.Player()
		if err != nil {
			return p, err
		}
		if cond(p) {
			return p, nil
		}
		if time.Now().After(deadline) {
			return p, errTimeout
		}
		if err := sleep(ctx, pollEvery); err != nil {
			return p, err
		}
	}
}

var errTimeout = errors.New("act: не дождался")

// Меню игры -------------------------------------------------------------------

func near(a, b color.RGBA) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(a.R, b.R) < 12 && d(a.G, b.G) < 12 && d(a.B, b.B) < 12
}

// MenuOpen — открыто ли меню игры (по пикселям кнопки Resume).
func (h *Hands) MenuOpen() (bool, error) {
	for _, p := range h.Layout.MenuProbe {
		img, err := h.In.Screenshot(input.Rect{X: h.Win.X + p.X, Y: h.Win.Y + p.Y, W: 1, H: 1})
		if err != nil {
			return false, err
		}
		c, ok := img.RGBAAt(0, 0), false
		for _, mc := range h.Layout.MenuColors {
			ok = ok || near(c, mc)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// guard закрывает меню игры, если оно открыто: под ним ввод не доходит до мира.
func (h *Hands) guard(ctx context.Context) error {
	open, err := h.MenuOpen()
	if err != nil || !open {
		return err
	}
	h.Say("открыто меню игры — закрываю")
	h.debugShot("menu")
	if err := h.Focus(); err != nil {
		return err
	}
	if err := h.In.Tap("escape"); err != nil {
		return err
	}
	if err := sleep(ctx, 300*time.Millisecond); err != nil {
		return err
	}
	if open, _ := h.MenuOpen(); open {
		return errors.New("act: меню игры не закрывается")
	}
	return nil
}

// Clean — исходное состояние перед действием: нет меню, нет окон, пустая рука.
func (h *Hands) Clean(ctx context.Context) error {
	if err := h.guard(ctx); err != nil {
		return err
	}
	if err := h.CloseGUI(ctx); err != nil {
		return err
	}
	return h.ClearCursor(ctx)
}

// Инвентарь и рука -----------------------------------------------------------------

// OpenInventory открывает окно персонажа (E).
func (h *Hands) OpenInventory(ctx context.Context) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if p.OpenedGUI == "controller" {
		return nil
	}
	if err := h.CloseGUI(ctx); err != nil {
		return err
	}
	// E иногда теряется (сразу после закрытия окна) — до 3 попыток; перед повтором
	// проверяем, не открылось ли с опозданием: E переключает окно
	for try := 0; try < 3; try++ {
		if err := h.Focus(); err != nil {
			return err
		}
		if err := h.In.Tap("e"); err != nil {
			return err
		}
		_, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool { return p.OpenedGUI == "controller" })
		if err == nil {
			return nil
		}
		if !errors.Is(err, errTimeout) {
			return err
		}
		h.Log("инвентарь не открылся, повторяю E")
	}
	return errors.New("act: инвентарь не открывается")
}

// search — Ctrl+F в открытом окне и текст поиска (старый заменяется).
func (h *Hands) search(ctx context.Context, text string) error {
	if err := h.In.Tap("f", "ctrl"); err != nil {
		return err
	}
	if err := h.In.Tap("a", "ctrl"); err != nil {
		return err
	}
	if err := h.In.Type(text); err != nil {
		return err
	}
	return sleep(ctx, 250*time.Millisecond)
}

// SearchText — что набирать в поиске: английское название ≈ имя без дефисов.
func SearchText(name string) string { return strings.ReplaceAll(name, "-", " ") }

func cursorName(p bridge.Player) string {
	if p.Cursor == nil {
		return ""
	}
	return p.Cursor.Name
}

// ClearCursor — убрать предмет из руки в инвентарь (Q). Q с пустой рукой — пипетка,
// поэтому жмём только когда рука занята.
func (h *Hands) ClearCursor(ctx context.Context) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if p.Cursor == nil {
		return nil
	}
	if err := h.Focus(); err != nil {
		return err
	}
	if err := h.In.Tap("q"); err != nil {
		return err
	}
	if _, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool { return p.Cursor == nil }); err != nil {
		return fmt.Errorf("act: рука не освободилась: %w", err)
	}
	return nil
}

// Pick берёт предмет в руку: клавишей быстрой панели, а если предмета на панели нет —
// один раз кликом из инвентаря с привязкой к свободному слоту панели (дальше — хоткей).
// Окна после Pick закрыты.
func (h *Hands) Pick(ctx context.Context, item string) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if cursorName(p) == item {
		return h.CloseGUI(ctx)
	}
	if p.Inventory[item] == 0 {
		return fmt.Errorf("act: нет %s в инвентаре", item)
	}
	if err := h.CloseGUI(ctx); err != nil {
		return err
	}
	if err := h.ClearCursor(ctx); err != nil {
		return err
	}
	for i, q := range p.Quickbar {
		if q == item {
			if err := h.Focus(); err != nil {
				return err
			}
			if err := h.In.Tap(quickbarKeys[i]); err != nil {
				return err
			}
			if _, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool { return cursorName(p) == item }); err != nil {
				return fmt.Errorf("act: клавиша %s не дала %s в руку: %w", quickbarKeys[i], item, err)
			}
			return nil
		}
	}
	return h.pickFromInventory(ctx, item)
}

func (h *Hands) pickFromInventory(ctx context.Context, item string) error {
	if err := h.OpenInventory(ctx); err != nil {
		return err
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	slot := 0
	for _, s := range p.InventorySlots {
		if s.Name == item {
			slot = s.Slot
			break
		}
	}
	if slot == 0 {
		return fmt.Errorf("act: %s нет в ячейках инвентаря", item)
	}
	L := h.Layout
	i := slot - 1
	if err := h.at(point{L.InvSlot1.X + (i%L.Cols)*L.SlotStep, L.InvSlot1.Y + (i/L.Cols)*L.SlotStep}); err != nil {
		return err
	}
	if err := h.In.Click(input.Left); err != nil {
		return err
	}
	if _, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool { return cursorName(p) == item }); err != nil {
		return fmt.Errorf("act: клик по ячейке %d не дал %s в руку: %w", slot, item, err)
	}
	// привязать к первому свободному слоту панели — в следующий раз хватит клавиши
	for i, q := range p.Quickbar {
		if q != "" {
			continue
		}
		h.Say("привязываю %s к клавише %s", item, quickbarKeys[i])
		if err := h.at(L.Quickbar[i]); err != nil {
			return err
		}
		if err := h.In.Click(input.Left); err != nil {
			return err
		}
		if _, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool {
			return len(p.Quickbar) > i && p.Quickbar[i] == item && cursorName(p) == item
		}); err != nil {
			return fmt.Errorf("act: не привязался слот панели %d: %w", i+1, err)
		}
		break
	}
	return h.CloseGUI(ctx)
}

// Крафт ---------------------------------------------------------------------------

// craftSlot — ячейка рецепта в окне крафта после поиска text. Окно показывает вкладку
// первой группы с совпадениями; каждая подгруппа — с новой строки, внутри — по order.
// Совпадение считаем по английскому названию ≈ имени без дефисов.
func craftSlot(protos *knowledge.Prototypes, available []string, handcraft map[string]bool, recipe, text string) (row, col int, err error) {
	type rec struct {
		name string
		r    knowledge.Recipe
	}
	var match []rec
	for _, name := range available {
		r, ok := protos.Recipes[name]
		if !ok || r.Hidden || !handcraft[r.Category] {
			continue
		}
		if strings.Contains(SearchText(name), text) {
			match = append(match, rec{name, r})
		}
	}
	sort.Slice(match, func(i, j int) bool {
		a, b := match[i].r, match[j].r
		if a.GroupOrder != b.GroupOrder {
			return a.GroupOrder < b.GroupOrder
		}
		if a.SubgroupOrder != b.SubgroupOrder {
			return a.SubgroupOrder < b.SubgroupOrder
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return match[i].name < match[j].name
	})
	if len(match) == 0 {
		return 0, 0, fmt.Errorf("act: рецепт %s недоступен для ручного крафта", recipe)
	}
	group := match[0].r.Group
	sub, inSub := "", 0
	row = -1
	for _, m := range match {
		if m.r.Group != group {
			break
		}
		if m.r.Subgroup != sub || inSub == 10 {
			row++
			sub, inSub = m.r.Subgroup, 0
		}
		if m.name == recipe {
			return row, inSub, nil
		}
		inSub++
	}
	return 0, 0, fmt.Errorf("act: %s не на первой вкладке результатов поиска %q", recipe, text)
}

// queued — сколько крафтов рецепта в очереди.
func queued(p bridge.Player, recipe string) int {
	n := 0
	for _, q := range p.CraftingQueue {
		if q.Recipe == recipe {
			n += q.Count
		}
	}
	return n
}

// Craft ставит в очередь n крафтов рецепта: E → поиск → клики по ячейке рецепта
// (ПКМ — 5 штук, ЛКМ — 1). Не ждёт окончания (см. CraftWait).
func (h *Hands) Craft(ctx context.Context, recipe string, n int) error {
	if h.Protos == nil {
		return errors.New("act: Craft без справочника (Hands.Protos)")
	}
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	handcraft := map[string]bool{}
	if ch, ok := h.Protos.Entities["character"]; ok {
		for _, c := range ch.CraftingCategories {
			handcraft[c] = true
		}
	}
	text := SearchText(recipe)
	row, col, err := craftSlot(h.Protos, f.Recipes, handcraft, recipe, text)
	if err != nil {
		return err
	}
	if err := h.ClearCursor(ctx); err != nil {
		return err
	}
	h.Say("крафчу %s ×%d", recipe, n)
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		return err
	}
	p0, err := h.B.Player()
	if err != nil {
		return err
	}
	if err := h.OpenInventory(ctx); err != nil {
		return err
	}
	if err := h.search(ctx, text); err != nil {
		return err
	}
	L := h.Layout
	if err := h.at(point{L.CraftSlot1.X + col*L.SlotStep, L.CraftSlot1.Y + row*L.SlotStep}); err != nil {
		return err
	}
	click := func(k int) error {
		for left := k; left > 0; {
			b := input.Left
			if left >= 5 {
				b = input.Right // craft-5
				left -= 5
			} else {
				left--
			}
			if err := h.In.Click(b); err != nil {
				return err
			}
		}
		return nil
	}
	if err := click(n); err != nil {
		return err
	}
	// очередь + уже готовые с начала == n; чужой рецепт в очереди — ошибка ячейки
	check := func(p bridge.Player) (int, error) {
		for _, q := range p.CraftingQueue {
			if !q.Prerequisite && q.Recipe != recipe && queued(p0, q.Recipe) < q.Count {
				return 0, fmt.Errorf("act: вместо %s в очереди %s — ячейка крафта не та", recipe, q.Recipe)
			}
		}
		evs, _, err := h.B.PollEvents(seq0)
		if err != nil {
			return 0, err
		}
		done := 0
		for _, e := range evs {
			if e.Type == "crafted_item" && e.Recipe == recipe {
				done++
			}
		}
		return queued(p, recipe) + done - queued(p0, recipe), nil
	}
	var got int
	var cerr error
	wait := func() {
		h.waitFor(ctx, time.Second, func(p bridge.Player) bool {
			got, cerr = check(p)
			return cerr != nil || got >= n
		})
	}
	wait()
	if cerr == nil && got == 0 {
		// ни один клик не дошёл (бывает сразу после открытия окна) — повторить один раз
		h.Log("крафт %s не начался — повторяю клик", recipe)
		if err := click(n); err != nil {
			return err
		}
		wait()
	}
	if cerr != nil || got < n {
		h.debugShot("craft-" + recipe)
	}
	if cerr != nil {
		h.CloseGUI(ctx)
		return cerr
	}
	if err := h.CloseGUI(ctx); err != nil {
		return err
	}
	if got < n {
		return fmt.Errorf("act: %s: в очереди %d из %d (не хватает ингредиентов?)", recipe, got, n)
	}
	return nil
}

// CraftWait — Craft и ожидание, пока все n крафтов закончатся.
func (h *Hands) CraftWait(ctx context.Context, recipe string, n int) error {
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		return err
	}
	if err := h.Craft(ctx, recipe, n); err != nil {
		return err
	}
	wait := 10 * time.Second
	if r, ok := h.Protos.Recipes[recipe]; ok {
		wait += time.Duration(r.Energy*float64(n)*1.5) * time.Second
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		evs, _, err := h.B.PollEvents(seq0)
		if err != nil {
			return err
		}
		done := 0
		for _, e := range evs {
			if e.Type == "crafted_item" && e.Recipe == recipe {
				done++
			}
		}
		if done >= n {
			return nil
		}
		if err := sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("act: крафт %s ×%d не закончился за %v", recipe, n, wait)
}
