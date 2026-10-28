package act

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"slices"
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
	InvSlot1   point // центр первой ячейки инвентаря (окно E)
	CraftSlot1 point // центр первой ячейки крафта (окно E, после поиска)
	ChestSlot1 point // центр первой ячейки сундука (окно сундука, справа; по Cols в строке)
	SlotStep   int   // шаг ячеек
	// FlushX — столбец красных кнопок-корзин окна цистерны (слить её содержимое; ниже — слить
	// всю сеть труб): по нему кнопки ищутся по пикселям — окно по высоте разное
	FlushX   int
	Cols     int       // ячеек в строке (инвентарь и крафт)
	Quickbar [10]point // ряд 1 быстрой панели: клавиши 1..0
	// Меню игры (Esc без открытого окна) мод не видит — узнаём по зелёной кнопке Resume
	// (два оттенка: обычный и под курсором).
	MenuProbe  []point
	MenuColors []color.RGBA
	// Focus — куда ставить курсор перед клавишами: в окне, но не над меню/окнами
	// (центр окна — кнопка «Save game» меню игры).
	Focus point
	// Окно технологий (T) мод не видит (opened_gui_type 0) — узнаём по пикселям: кнопка
	// закрытия и тёмный заголовок «Research queue».
	TechProbe   []point
	TechColors  []color.RGBA
	TechFirst   point // первая технология в списке (после поиска)
	RecipeFirst point // первый рецепт в окне выбора рецепта сборщика (после поиска)
	// RecipeChange — кнопка смены рецепта в окне сборщика с рецептом; RecipeFirstChange — первый
	// рецепт в окне выбора, открытом этой кнопкой (оно выше на 20 пикселей)
	RecipeChange      point
	RecipeFirstChange point
	TechStart         point // кнопка «Start research» карточки выбранной технологии
	// окно инсертера (обычного): флажок «Use filters», первая ячейка фильтра, первая
	// найденная в окне «Set the filter» (после поиска)
	UseFilters, FilterSlot, FilterFirst point
	// AltButton — кнопка ALT на панели быстрого доступа: оранжевая — Alt-режим включён
	AltButton point
	AltOn     color.RGBA
	// Panels — панели интерфейса поверх мира (быстрый доступ, оружие, миникарта): клик туда
	// не по миру — курсор на сущность там не ставить
	Panels []rect
}

// rect — прямоугольник в координатах окна.
type rect struct{ X, Y, W, H int }

var Layout1856 = Layout{
	InvSlot1:   point{530, 350},
	CraftSlot1: point{966, 390},
	ChestSlot1: point{966, 505},
	SlotStep:   40,
	FlushX:     1103,
	Cols:       10,
	Quickbar: [10]point{{745, 946}, {785, 946}, {826, 946}, {866, 946}, {906, 946},
		{950, 946}, {990, 946}, {1030, 946}, {1070, 946}, {1110, 946}},
	MenuProbe:         []point{{880, 413}, {927, 400}},
	MenuColors:        []color.RGBA{{94, 182, 99, 255}, {146, 232, 151, 255}},
	Focus:             point{60, 300},
	TechProbe:         []point{{1831, 20}, {270, 22}},
	TechColors:        []color.RGBA{{226, 226, 226, 255}, {49, 48, 49, 255}},
	TechFirst:         point{51, 535},
	RecipeFirst:       point{747, 449},
	RecipeChange:      point{1325, 506},
	RecipeFirstChange: point{747, 429},
	TechStart:         point{477, 422},
	UseFilters:        point{953, 556},
	FilterSlot:        point{1165, 568},
	FilterFirst:       point{1189, 730},
	AltButton:         point{1160, 948},
	AltOn:             color.RGBA{229, 181, 96, 255},
	Panels:            []rect{{0, 915, 192, 101}, {668, 918, 540, 98}, {1600, 0, 256, 592}},
}

// underPanel — точка экрана (sx, sy) под панелью интерфейса.
func (h *Hands) underPanel(sx, sy int) bool {
	x, y := sx-h.Win.X, sy-h.Win.Y
	for _, r := range h.Layout.Panels {
		if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
			return true
		}
	}
	return false
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

// altOn — включён ли Alt-режим: кнопка ALT на панели оранжевая.
func (h *Hands) altOn() (on, visible bool, err error) {
	p := h.Layout.AltButton
	img, err := h.In.Screenshot(input.Rect{X: h.Win.X + p.X - 10, Y: h.Win.Y + p.Y, W: 21, H: 1})
	if err != nil {
		return false, false, err
	}
	// на кнопке надпись ALT — пиксели разные; ровный цвет — кнопку закрыло окно
	for x := 1; x < 21; x++ {
		if img.RGBAAt(x, 0) != img.RGBAAt(0, 0) {
			visible = true
		}
	}
	return near(img.RGBAAt(10, 0), h.Layout.AltOn), visible, nil
}

// firstSlotY — середина первой ячейки окна выбора рецепта: от верха поля поиска (светло-
// серое, правее заголовка) на 133 px вниз, если в окне вкладки групп (у сборщика), иначе на
// 62 (нефтезавод, химзавод: одна группа — окно ниже). Окно по высоте подстраивается под число
// рецептов машины, поэтому постоянной точки нет. Не нашлось — false.
func (h *Hands) firstSlotY(tabs bool) (int, bool) {
	// полоса — у правого края поля (поле 910–1009): набранное слово с кареткой слева до неё не
	// доходит (при x = 960 «inserter» доставал — поле не находилось ни разу, клик — на ряд ниже:
	// стадия 32 на 859088572)
	const x, y0, n, w = 980, 200, 400, 24
	img, err := h.In.Screenshot(input.Rect{X: h.Win.X + x, Y: h.Win.Y + y0, W: w, H: n})
	if err != nil {
		return 0, false
	}
	// поле поиска — ровная светлая полоса (серая, а пока вводим — бежевая) сразу под тёмной
	// рамкой заголовка; одинаковая по ширине — не пятно местности
	light := func(y int) bool {
		c := img.RGBAAt(0, y)
		return max(c.R, c.G, c.B) > 100 && img.RGBAAt(w/2, y) == c && img.RGBAAt(w-1, y) == c
	}
	dark := func(y int) bool {
		c := img.RGBAAt(0, y)
		return max(c.R, c.G, c.B) < 70 && img.RGBAAt(w-1, y) == c
	}
	for y := 3; y+10 < n; y++ {
		if light(y) && light(y+5) && light(y+10) && (dark(y-1) || dark(y-2) || dark(y-3)) {
			if tabs {
				return y0 + y + 133, true
			}
			return y0 + y + 62, true
		}
	}
	return 0, false
}

// closeStray — закрыть окно, которого мод не видит (фильтр слота быстрого доступа и т.п.):
// Esc; если вместо этого открылось меню игры — ещё Esc.
func (h *Hands) closeStray(ctx context.Context) error {
	if err := h.Focus(); err != nil {
		return err
	}
	if err := h.In.Tap("escape"); err != nil {
		return err
	}
	if err := sleep(ctx, 400*time.Millisecond); err != nil {
		return err
	}
	if open, err := h.MenuOpen(); err != nil || !open {
		return err
	}
	if err := h.In.Tap("escape"); err != nil {
		return err
	}
	return sleep(ctx, 400*time.Millisecond)
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

// guard возвращает обычный вид: закрывает карту (удалённый вид) и меню игры — под ними
// ввод не доходит до мира (WASD двигает карту, буквы уходят в поиск). Esc жмём по одному,
// с проверкой после каждого: лишний Esc в обычном виде откроет меню.
func (h *Hands) guard(ctx context.Context) error {
	// заставка (крушение в начале новой карты): персонажа ещё нет — Tab её пропускает
	for i := 0; i < 5; i++ {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		if p.ControllerName != "cutscene" {
			break
		}
		if i == 0 {
			h.Say("заставка — пропускаю (Tab)")
		}
		if err := h.Focus(); err != nil {
			return err
		}
		if err := h.In.Tap("tab"); err != nil {
			return err
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	for i := 0; i < 4; i++ {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		if p.RenderMode == "" || p.RenderMode == "game" {
			break
		}
		if i == 0 {
			h.Say("открыта карта — закрываю")
			h.debugShot("map")
		}
		if err := h.Focus(); err != nil {
			return err
		}
		if err := h.In.Tap("escape"); err != nil {
			return err
		}
		if err := sleep(ctx, 400*time.Millisecond); err != nil {
			return err
		}
		if i == 3 {
			return errors.New("act: карта не закрывается")
		}
	}
	// Alt-режим (просьба пользователя): на постройках видно, что в них и что делается.
	// Включён ли — по кнопке ALT на экране (как видит игрок): значение из мода после
	// перезапуска сервера может не совпадать с клиентом, и нажатие выключило бы режим
	if p, err := h.B.Player(); err == nil && p.ControllerName == "character" && !h.altChecked {
		on, visible, err := h.altOn()
		if err != nil {
			return err
		}
		if !visible {
			on = true // кнопку закрыло окно — проверить в другой раз (не нажимать вслепую)
		}
		if !on {
			h.Say("включаю Alt-режим (содержимое построек)")
			if err := h.Focus(); err != nil {
				return err
			}
			if err := h.In.Tap("alt"); err != nil {
				return err
			}
			if err := sleep(ctx, 2*inputLatency); err != nil {
				return err
			}
			on, _, _ = h.altOn()
		}
		h.altChecked = on && visible
	}
	// стоит на ленте (снимок, прошлое действие) — лента несёт: сойти, пока действие не началось
	if h.World != nil {
		if p, err := h.B.Player(); err == nil && p.ControllerName == "character" {
			if _, err := h.World.Sync(h.B, false); err != nil {
				return err
			}
			if h.World.OnBelt(p.Position) {
				h.Say("стою на ленте — схожу")
				if err := h.stepOffBelt(ctx, p.Position); err != nil {
					return err
				}
			}
		}
	}
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
	// одно нажатие E (повтор опасен: если окно откроется с опозданием, второй E его закроет)
	if err := h.Focus(); err != nil {
		return err
	}
	if err := h.In.Tap("e"); err != nil {
		return err
	}
	if _, err := h.waitFor(ctx, 2500*time.Millisecond, func(p bridge.Player) bool { return p.OpenedGUI == "controller" }); err != nil {
		h.debugShot("inventory")
		return fmt.Errorf("act: инвентарь не открылся: %w", err)
	}
	return nil
}

// ErrFocus — окно, в которое бот собирался печатать, не открыто: текст ушёл бы в игру
// (буквы — хоткеи, Ctrl+F без окна открывает поиск по карте).
var ErrFocus = errors.New("act: нет окна для ввода текста")

// inInventory — открыт инвентарь в обычном виде (не карта).
func (h *Hands) inInventory() error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if p.OpenedGUI != "controller" || (p.RenderMode != "" && p.RenderMode != "game") {
		h.debugShot("focus")
		return fmt.Errorf("%w: окно %q, вид %q", ErrFocus, p.OpenedGUI, p.RenderMode)
	}
	return nil
}

// search — Ctrl+F в открытом инвентаре и текст поиска (старый заменяется). Перед каждым
// шагом проверяем, что инвентарь всё ещё открыт: иначе ни одной буквы не печатаем.
func (h *Hands) search(ctx context.Context, text string) error {
	if err := h.inInventory(); err != nil {
		return err
	}
	if err := h.In.Tap("f", "ctrl"); err != nil {
		return err
	}
	if err := sleep(ctx, inputLatency); err != nil {
		return err
	}
	if err := h.inInventory(); err != nil {
		return err
	}
	if err := h.In.Tap("a", "ctrl"); err != nil {
		return err
	}
	if err := h.In.Type(text); err != nil {
		return err
	}
	if err := sleep(ctx, 250*time.Millisecond); err != nil {
		return err
	}
	return h.inInventory()
}

// retype — в поле поиска, где уже идёт поиск (search), — другой текст (без Ctrl+F).
func (h *Hands) retype(ctx context.Context, text string) error {
	if err := h.inInventory(); err != nil {
		return err
	}
	if err := h.In.Tap("a", "ctrl"); err != nil {
		return err
	}
	if err := h.In.Type(text); err != nil {
		return err
	}
	if err := sleep(ctx, 250*time.Millisecond); err != nil {
		return err
	}
	return h.inInventory()
}

// tabWord — слово имени доступного рецепта, все совпадения которого — на вкладке group (поиск
// по нему переводит окно на неё); нет такого — "".
func (h *Hands) tabWord(available []string, shown func(knowledge.Recipe) bool, group string) string {
	for _, name := range slices.Sorted(slices.Values(available)) {
		if r, ok := h.Protos.Recipes[name]; !ok || r.Group != group || r.Hidden || !shown(r) {
			continue
		}
		for _, w := range strings.Split(name, "-") {
			if _, _, err := craftSlot(h.Protos, available, shown, name, w); len(w) >= 3 && err == nil {
				return w
			}
		}
	}
	return ""
}

// searchWord — что набирать в поиске рецепта: слово имени, которое встречается в наименьшем
// числе рецептов (при равенстве — длиннее), из тех, где рецепт окажется на первой вкладке
// результатов (fits; «burner» — сначала burner-inserter из «логистики», а бур — в
// «производстве»). Целиком нельзя: английские названия бывают с дефисом («Long-handed
// inserter»), «long handed» не найдётся; место рецепта среди найденных считает craftSlot.
func (h *Hands) searchWord(name string, fits func(word string) bool) string {
	best, bestN := SearchText(name), 1<<30
	for _, w := range strings.Split(name, "-") {
		if len(w) < 3 || !fits(w) {
			continue
		}
		n := 0
		for r := range h.Protos.Recipes {
			if strings.Contains(r, w) {
				n++
			}
		}
		if n < bestN || n == bestN && len(w) > len(best) {
			best, bestN = w, n
		}
	}
	return best
}

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
		return fmt.Errorf("%w: %s", ErrNoItem, item)
	}
	if err := h.CloseGUI(ctx); err != nil {
		return err
	}
	if err := h.ClearCursor(ctx); err != nil {
		return err
	}
	for i, q := range p.Quickbar {
		if q != item {
			continue
		}
		// второе нажатие — только если рука всё ещё пуста: с предметом в руке клавиша
		// панели вернула бы его обратно
		for try := 0; try < 2; try++ {
			if err := h.Focus(); err != nil {
				return err
			}
			if err := h.In.Tap(quickbarKeys[i]); err != nil {
				return err
			}
			p, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool { return cursorName(p) == item })
			if err == nil {
				return nil
			}
			if !errors.Is(err, errTimeout) || p.Cursor != nil {
				return fmt.Errorf("act: клавиша %s не дала %s в руку (в руке %+v): %w", quickbarKeys[i], item, p.Cursor, err)
			}
			// клавиши мог перехватить окно, которого мод не видит (фильтр слота) — закрыть
			if err := h.closeStray(ctx); err != nil {
				return err
			}
		}
		return fmt.Errorf("act: клавиша %s не дала %s в руку", quickbarKeys[i], item)
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
		// клик потерялся (стадия 13 на 794457638, сразу после крафта 409 лент) — ещё раз, с
		// заново открытым окном и свежими ячейками
		h.debugShot("pick-" + item)
		if err := h.ClearCursor(ctx); err != nil {
			return err
		}
		if err := h.CloseGUI(ctx); err != nil {
			return err
		}
		if err := h.OpenInventory(ctx); err != nil {
			return err
		}
		if p, err = h.B.Player(); err != nil {
			return err
		}
		for _, s := range p.InventorySlots {
			if s.Name == item {
				slot = s.Slot
				break
			}
		}
		i = slot - 1
		if err := h.at(point{L.InvSlot1.X + (i%L.Cols)*L.SlotStep, L.InvSlot1.Y + (i/L.Cols)*L.SlotStep}); err != nil {
			return err
		}
		if err := h.In.Click(input.Left); err != nil {
			return err
		}
		if _, err := h.waitFor(ctx, 2*time.Second, func(p bridge.Player) bool { return cursorName(p) == item }); err != nil {
			return fmt.Errorf("act: клик по ячейке %d не дал %s в руку: %w", slot, item, err)
		}
	}
	// на панель — в следующий раз хватит клавиши: в свободный слот, а свободных нет — по кругу
	// вместо того, что там было (номер следующего — в памяти, Hands.qbNext)
	q := slices.Index([]string(p.Quickbar), "")
	if q < 0 && len(p.Quickbar) > 0 {
		q = h.qbNext % len(p.Quickbar)
		h.qbNext = q + 1
	}
	if q >= 0 && q < len(L.Quickbar) {
		if err := h.bindQuickbar(ctx, q, item, p.Quickbar[q] != ""); err != nil {
			return err
		}
	}
	return h.CloseGUI(ctx)
}

// bindQuickbar — предмет в руке (item) — на слот i быстрой панели; busy — слот занят другим:
// сначала очистить его средней кнопкой (рука не меняется), потом ЛКМ. ЛКМ или ПКМ по
// занятому слоту с предметом в руке не заменяют его, а берут в руку то, что в слоте
// (проверено в игре, 2.0.77).
func (h *Hands) bindQuickbar(ctx context.Context, i int, item string, busy bool) error {
	L := h.Layout
	h.Say("привязываю %s к клавише %s", item, quickbarKeys[i])
	bound := func(p bridge.Player) bool {
		return len(p.Quickbar) > i && p.Quickbar[i] == item && cursorName(p) == item
	}
	if err := h.at(L.Quickbar[i]); err != nil {
		return err
	}
	if busy {
		if err := h.In.Click(input.Middle); err != nil {
			return err
		}
		if _, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool { return len(p.Quickbar) > i && p.Quickbar[i] == "" }); err != nil {
			return fmt.Errorf("act: слот панели %d не очистился: %w", i+1, err)
		}
	}
	if err := h.In.Click(input.Left); err != nil {
		return err
	}
	if _, err := h.waitFor(ctx, time.Second, bound); err != nil {
		return fmt.Errorf("act: не привязался слот панели %d: %w", i+1, err)
	}
	return nil
}

// Крафт ---------------------------------------------------------------------------

// craftSlot — ячейка рецепта в окне крафта после поиска text. Видны все доступные рецепты, для
// которых shown (в окне персонажа — и те, что руками не скрафтить: красные); каждая подгруппа —
// с новой строки, внутри — по order. Совпадения — только на одной вкладке: в 2.1 поиск остаётся
// на текущей вкладке, если на ней есть совпадения (крафт iron-chest по «iron» кликнул
// iron-plate на вкладке промежуточных). Совпадение — по английскому названию ≈ имени без дефисов.
func craftSlot(protos *knowledge.Prototypes, available []string, shown func(knowledge.Recipe) bool, recipe, text string) (row, col int, err error) {
	return craftSlotIn(protos, available, shown, recipe, text, "")
}

// craftSlotIn — craftSlot, когда окно уже на вкладке group (не пусто): совпадения на других
// вкладках не мешают.
func craftSlotIn(protos *knowledge.Prototypes, available []string, shown func(knowledge.Recipe) bool, recipe, text, group string) (row, col int, err error) {
	type rec struct {
		name string
		r    knowledge.Recipe
	}
	if !slices.Contains(available, recipe) {
		return 0, 0, fmt.Errorf("act: рецепт %s не открыт", recipe)
	}
	key := strings.ReplaceAll(text, " ", "-")
	var match []rec
	for _, name := range available {
		r, ok := protos.Recipes[name]
		if !ok || r.Hidden || !shown(r) || group != "" && r.Group != group {
			continue
		}
		if strings.Contains(name, key) {
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
		return 0, 0, fmt.Errorf("act: рецепт %s недоступен здесь", recipe)
	}
	group = match[0].r.Group
	for _, m := range match {
		if m.r.Group != group {
			return 0, 0, fmt.Errorf("act: поиск %q — совпадения на разных вкладках (%s, %s)", text, group, m.r.Group)
		}
	}
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
	if r, ok := h.Protos.Recipes[recipe]; !ok || !handcraft[r.Category] {
		return fmt.Errorf("act: рецепт %s руками не скрафтить", recipe)
	}
	all := func(knowledge.Recipe) bool { return true }
	text := h.searchWord(recipe, func(w string) bool {
		_, _, err := craftSlot(h.Protos, f.Recipes, all, recipe, w)
		return err == nil
	})
	row, col, err := craftSlot(h.Protos, f.Recipes, all, recipe, text)
	tab := "" // слово, что есть только на вкладке рецепта
	if err != nil {
		// совпадения на разных вкладках при любом слове (насос: «pump» — ещё помпа и качалка на
		// «производстве», стадия 34): сначала поиск слова только с вкладки рецепта — окно
		// переходит на неё, потом text — поиск остаётся на ней, раз там есть совпадения (2.1)
		g := h.Protos.Recipes[recipe].Group
		if tab = h.tabWord(f.Recipes, all, g); tab == "" {
			return err
		}
		if row, col, err = craftSlotIn(h.Protos, f.Recipes, all, recipe, text, g); err != nil {
			return err
		}
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
	if tab != "" {
		if err := h.search(ctx, tab); err != nil {
			return err
		}
		if err := h.retype(ctx, text); err != nil {
			return err
		}
	} else if err := h.search(ctx, text); err != nil {
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
	deadline, hard := time.Now().Add(wait), time.Now().Add(10*time.Minute)
	var full time.Time
	for time.Now().Before(deadline) && time.Now().Before(hard) {
		// очередь ещё крафтит (полуфабрикаты: шестерни, схемы) — ждём дальше
		if p, err := h.B.Player(); err == nil && len(p.CraftingQueue) > 0 {
			// инвентарь полон — готовое некуда положить, крафт стоит
			if p.InventorySize == 0 || len(p.InventorySlots) < p.InventorySize {
				full = time.Time{}
			} else if full.IsZero() {
				full = time.Now()
			} else if time.Since(full) > 20*time.Second {
				return fmt.Errorf("act: крафт %s ×%d стоит — инвентарь полон", recipe, n)
			}
			deadline = time.Now().Add(wait)
		}
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

// techOpen — открыто ли окно технологий (по пикселям).
func (h *Hands) techOpen() (bool, error) {
	for i, p := range h.Layout.TechProbe {
		img, err := h.In.Screenshot(input.Rect{X: h.Win.X + p.X, Y: h.Win.Y + p.Y, W: 1, H: 1})
		if err != nil {
			return false, err
		}
		if !near(img.RGBAAt(0, 0), h.Layout.TechColors[i]) {
			return false, nil
		}
	}
	return true, nil
}

// researching — tech исследуется или стоит в очереди.
func (h *Hands) researching(tech string) (bool, error) {
	f, err := h.B.Force()
	if err != nil {
		return false, err
	}
	if f.Current == tech {
		return true, nil
	}
	for _, q := range f.Queue {
		if q == tech {
			return true, nil
		}
	}
	return false, nil
}

// StartResearch — запустить исследование tech через окно технологий: T → поиск по
// названию → первая в списке (доступные идут первыми) → «Start research» → Esc.
// Подтверждение — tech в текущем исследовании или в очереди (мод).
func (h *Hands) StartResearch(ctx context.Context, tech string) error {
	if ok, err := h.researching(tech); err != nil || ok {
		return err
	}
	h.Say("запускаю исследование %s", tech)
	if err := h.Clean(ctx); err != nil {
		return err
	}
	if err := h.Focus(); err != nil {
		return err
	}
	if err := h.In.Tap("t"); err != nil {
		return err
	}
	defer h.closeTech(ctx)
	open := false
	for i := 0; i < 10 && !open; i++ {
		if err := sleep(ctx, inputLatency); err != nil {
			return err
		}
		var err error
		if open, err = h.techOpen(); err != nil {
			return err
		}
	}
	if !open {
		h.debugShot("tech-open")
		return fmt.Errorf("act: окно технологий не открылось")
	}
	// поиск: буквы печатать только при открытом окне (иначе уйдут в игру хоткеями)
	if err := h.In.Tap("f", "ctrl"); err != nil {
		return err
	}
	if err := sleep(ctx, inputLatency); err != nil {
		return err
	}
	if ok, err := h.techOpen(); err != nil || !ok {
		return fmt.Errorf("act: окно технологий закрылось (%v)", err)
	}
	if err := h.In.Tap("a", "ctrl"); err != nil {
		return err
	}
	if err := h.In.Type(SearchText(tech)); err != nil {
		return err
	}
	if err := sleep(ctx, 400*time.Millisecond); err != nil {
		return err
	}
	for _, p := range []point{h.Layout.TechFirst, h.Layout.TechStart} {
		if err := h.at(p); err != nil {
			return err
		}
		if err := sleep(ctx, 300*time.Millisecond); err != nil {
			return err
		}
		if err := h.In.Click(input.Left); err != nil {
			return err
		}
	}
	for i := 0; i < 10; i++ {
		if err := sleep(ctx, inputLatency); err != nil {
			return err
		}
		if ok, err := h.researching(tech); err != nil || ok {
			return err
		}
	}
	h.debugShot("research-" + tech)
	return fmt.Errorf("act: исследование %s не запустилось", tech)
}

// closeTech — закрыть окно технологий Esc (лишний Esc откроет меню — guard закроет).
func (h *Hands) closeTech(ctx context.Context) {
	for i := 0; i < 3; i++ {
		if ok, _ := h.techOpen(); !ok {
			break
		}
		h.In.Tap("escape")
		sleep(ctx, 2*inputLatency)
	}
	h.guard(ctx)
}

// SetRecipe — задать рецепт сборщику: клик по нему → окно выбора рецепта (если рецепт уже
// задан другой — кнопка смены рецепта в окне сборщика) → Ctrl+F → название → ячейка
// рецепта среди найденных (порядок как в игре: группа, подгруппа, order — craftSlot);
// подтверждение — рецепт из мода.
// SetFilter — фильтр инсертера e «брать только item»: клик по инсертеру → флажок «Use
// filters» → ячейка фильтра → поиск по названию → найденная. Найдено несколько (inserter:
// burner-inserter первым) — следующая ячейка. Подтверждение — filters из мода.
func (h *Hands) SetFilter(ctx context.Context, e bridge.Entity, item string) error {
	cur := func() (*bridge.EntityState, error) {
		st, err := h.B.GetEntity(e.Name, e.Position)
		if err == nil && st == nil {
			err = fmt.Errorf("act: %s пропал", e.Name)
		}
		return st, err
	}
	st, err := cur()
	if err != nil {
		return err
	}
	if len(st.Filters) > 0 && st.Filters[0] == item {
		return nil
	}
	h.Say("фильтр %s у %s", item, e.Name)
	if err := h.Clean(ctx); err != nil {
		return err
	}
	if err := h.reach(ctx, e, buildReach); err != nil {
		return err
	}
	if err := h.waitStill(ctx); err != nil {
		return err
	}
	defer h.CloseGUI(ctx)
	if err := h.openEntity(ctx, e, "filter-open"); err != nil {
		return err
	}
	opened := func(p bridge.Player) bool { return p.OpenedGUIType != nil && *p.OpenedGUIType != 0 }
	click := func(p point, pause time.Duration) error {
		if err := h.at(p); err != nil {
			return err
		}
		if err := sleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		if err := h.In.Click(input.Left); err != nil {
			return err
		}
		return sleep(ctx, pause)
	}
	if len(st.Filters) == 0 { // фильтр выключен — включить (ячейки заданы — значит включён)
		if err := click(h.Layout.UseFilters, 300*time.Millisecond); err != nil {
			return err
		}
	}
	text := strings.ReplaceAll(item, "-", " ")
	for k := 0; k < 5; k++ {
		if err := click(h.Layout.FilterSlot, 400*time.Millisecond); err != nil {
			return err
		}
		// буквы — только при открытом окне (иначе уйдут в игру хоткеями)
		if p, err := h.B.Player(); err != nil || !opened(p) {
			return fmt.Errorf("act: окно %s закрылось", e.Name)
		}
		if err := h.In.Tap("f", "ctrl"); err != nil {
			return err
		}
		if err := sleep(ctx, inputLatency); err != nil {
			return err
		}
		if err := h.In.Tap("a", "ctrl"); err != nil {
			return err
		}
		if err := h.In.Type(text); err != nil {
			return err
		}
		if err := sleep(ctx, 400*time.Millisecond); err != nil {
			return err
		}
		f := h.Layout.FilterFirst
		if err := click(point{f.X + k*h.Layout.SlotStep, f.Y}, 300*time.Millisecond); err != nil {
			return err
		}
		if st, err = cur(); err != nil {
			return err
		}
		if len(st.Filters) > 0 && st.Filters[0] == item {
			return nil
		}
		h.Log("фильтр: ячейка %d дала %v", k, st.Filters)
	}
	h.debugShot("filter")
	return fmt.Errorf("act: фильтр %s у %s не встал (%v)", item, e.Name, st.Filters)
}

func (h *Hands) SetRecipe(ctx context.Context, e bridge.Entity, recipe string) error {
	cur := func() (string, error) {
		st, err := h.B.GetEntity(e.Name, e.Position)
		if err != nil || st == nil {
			return "", err
		}
		return st.Recipe, nil
	}
	was, err := cur()
	if err != nil || was == recipe {
		return err
	}
	h.Say("рецепт %s в %s", recipe, e.Name)
	if err := h.Clean(ctx); err != nil {
		return err
	}
	if err := h.reach(ctx, e, buildReach); err != nil {
		return err
	}
	if err := h.waitStill(ctx); err != nil {
		return err
	}
	defer h.CloseGUI(ctx)
	if err := h.openEntity(ctx, e, "recipe-open"); err != nil {
		return err
	}
	opened := func(p bridge.Player) bool { return p.OpenedGUIType != nil && *p.OpenedGUIType != 0 }
	first := h.Layout.RecipeFirst
	if was != "" {
		// рецепт уже есть (не тот) — кнопка смены рецепта открывает окно выбора
		h.Say("в %s рецепт %s — меняю", e.Name, was)
		if err := h.at(h.Layout.RecipeChange); err != nil {
			return err
		}
		if err := sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
		if err := h.In.Click(input.Left); err != nil {
			return err
		}
		if err := sleep(ctx, 400*time.Millisecond); err != nil {
			return err
		}
		first = h.Layout.RecipeFirstChange
	}
	// буквы — только при открытом окне (иначе уйдут в игру хоткеями)
	if err := h.In.Tap("f", "ctrl"); err != nil {
		return err
	}
	if err := sleep(ctx, inputLatency); err != nil {
		return err
	}
	if p, err := h.B.Player(); err != nil || !opened(p) {
		return fmt.Errorf("act: окно %s закрылось", e.Name)
	}
	// где рецепт среди найденных: доступные рецепты категорий этого сборщика
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	cats := map[string]bool{}
	for _, c := range h.Protos.Entities[e.Name].CraftingCategories {
		cats[c] = true
	}
	inCats := func(r knowledge.Recipe) bool { return cats[r.Category] }
	text := h.searchWord(recipe, func(w string) bool {
		_, _, err := craftSlot(h.Protos, f.Recipes, inCats, recipe, w)
		return err == nil
	})
	if err := h.In.Tap("a", "ctrl"); err != nil {
		return err
	}
	if err := h.In.Type(text); err != nil {
		return err
	}
	if err := sleep(ctx, 400*time.Millisecond); err != nil {
		return err
	}
	row, col, err := craftSlot(h.Protos, f.Recipes, inCats, recipe, text)
	if err != nil {
		return err
	}
	// окно выбора по высоте подстраивается под число рецептов машины (у нефтезавода их
	// мало — окно ниже, ячейки выше): первая ячейка — по оранжевой рамке найденного
	groups := map[string]bool{}
	for _, r := range f.Recipes {
		// parameter-N (категория parameters) в окне выбора не показываются
		if rec, ok := h.Protos.Recipes[r]; ok && cats[rec.Category] && !rec.Hidden && rec.Category != "parameters" {
			groups[rec.Group] = true
		}
	}
	if y, ok := h.firstSlotY(len(groups) > 1); ok {
		first.Y = y
	} else {
		h.Log("окно рецептов: поле поиска не найдено (групп %d)", len(groups))
	}
	step := h.Layout.SlotStep
	h.Log("рецепт %s: ячейка %d,%d → (%d, %d)", recipe, row, col, first.X+col*step, first.Y+row*step)
	if err := h.at(point{first.X + col*step, first.Y + row*step}); err != nil {
		return err
	}
	if err := sleep(ctx, 200*time.Millisecond); err != nil {
		return err
	}
	// клик по ячейке бывает теряется (окно выбора открыто, ячейка под курсором, рецепт пуст —
	// стадия 23 на 859088572) — ещё раз, пока окно выбора открыто
	for try := 0; try < 3; try++ {
		if try > 0 {
			p, err := h.B.Player()
			if err != nil {
				return err
			}
			if p.OpenedGUIType == nil || *p.OpenedGUIType == 0 {
				break // окно закрылось — клик дошёл (или нечего кликать)
			}
			h.Log("рецепт %s не встал — кликаю ещё раз", recipe)
		}
		if err := h.In.Click(input.Left); err != nil {
			return err
		}
		for i := 0; i < 10; i++ {
			if err := sleep(ctx, inputLatency); err != nil {
				return err
			}
			if r, err := cur(); err != nil || r == recipe {
				return err
			}
		}
	}
	h.debugShot("recipe-" + recipe)
	r, _ := cur()
	return fmt.Errorf("act: рецепт %s не задался (стоит %q)", recipe, r)
}

// SearchText — что набирать в поиске: английское название ≈ имя без дефисов.
func SearchText(name string) string { return strings.ReplaceAll(name, "-", " ") }

// TakeItem — взять из сундука e не меньше n предмета item (пачками; меньше есть — всё, что есть),
// остальное содержимое — в сундуке: окно сундука, Shift+клик по ячейкам с item (ячейки — от
// мода), пока у игрока не прибавится n. Не Take (Ctrl+клик по сундуку — всё содержимое:
// инвентарь забивался, стадия 23 на 859088572).
func (h *Hands) TakeItem(ctx context.Context, e bridge.Entity, item string, n int) error {
	st, err := h.B.GetEntity(e.Name, e.Position)
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("act: %s пропал", e.Name)
	}
	var slots []bridge.Slot
	total := 0
	for _, s := range st.Slots {
		if s.Name == item {
			slots, total = append(slots, s), total+s.Count
		}
	}
	if total == 0 {
		return fmt.Errorf("act: в %s нет %s", e.Name, item)
	}
	n = min(n, total)
	h.Say("беру %s ×%d из %s", item, n, e.Name)
	if err := h.Clean(ctx); err != nil {
		return err
	}
	if err := h.reach(ctx, e, buildReach); err != nil {
		return err
	}
	if err := h.waitStill(ctx); err != nil {
		return err
	}
	defer h.CloseGUI(ctx)
	if err := h.openEntity(ctx, e, "chest-open"); err != nil {
		return err
	}
	p0, err := h.B.Player()
	if err != nil {
		return err
	}
	got := func(p bridge.Player) int { return p.Inventory[item] - p0.Inventory[item] }
	L := h.Layout
	before := 0
	for _, s := range slots {
		i := s.Slot - 1
		if err := h.at(point{L.ChestSlot1.X + (i%L.Cols)*L.SlotStep, L.ChestSlot1.Y + (i/L.Cols)*L.SlotStep}); err != nil {
			return err
		}
		if err := h.In.KeyDown("shift"); err != nil {
			return err
		}
		err := h.In.Click(input.Left)
		h.In.KeyUp("shift")
		if err != nil {
			return err
		}
		// пачка перешла к игроку (не дошёл клик — следующая ячейка, итог проверится в конце)
		p, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool { return got(p) > before })
		if err != nil && !errors.Is(err, errTimeout) {
			return err
		}
		if before = got(p); before >= n {
			return nil
		}
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if got(p) < n {
		if got(p) > 0 && len(p.InventorySlots) >= p.InventorySize { // инвентарь полон — взято, сколько влезло
			h.Log("из %s взято %s ×%d из %d: инвентарь полон", e.Name, item, got(p), n)
			return nil
		}
		h.debugShot("chest-take")
		return fmt.Errorf("act: из %s взято %s ×%d из %d", e.Name, item, got(p), n)
	}
	return nil
}

// FlushPipeline — слить всю сеть труб, к которой подключена цистерна e (в ней чужая жидкость,
// решение пользователя 28.10: кнопка в окне): окно цистерны, нижняя красная кнопка-корзина
// («Pipeline contents»; верхняя — содержимое самой цистерны). Подтверждение — в цистерне больше
// нет жидкости fluid (своя натечёт сразу).
func (h *Hands) FlushPipeline(ctx context.Context, e bridge.Entity, fluid string) error {
	h.Say("сливаю сеть труб у %s в (%.1f, %.1f): в ней %s", e.Name, e.Position.X, e.Position.Y, fluid)
	if err := h.reach(ctx, e, buildReach); err != nil {
		return err
	}
	if err := h.waitStill(ctx); err != nil {
		return err
	}
	if err := h.ClearCursor(ctx); err != nil {
		return err
	}
	if err := h.openEntity(ctx, e, "flush"); err != nil {
		return err
	}
	defer h.CloseGUI(ctx)
	y, ok := h.flushButton()
	if !ok {
		h.debugShot("flush")
		return fmt.Errorf("act: в окне %s кнопки слива сети нет", e.Name)
	}
	if err := h.at(point{h.Layout.FlushX, y}); err != nil {
		return err
	}
	if err := h.In.Click(input.Left); err != nil {
		return err
	}
	for i := 0; i < 10; i++ {
		if err := sleep(ctx, inputLatency); err != nil {
			return err
		}
		st, err := h.B.GetEntity(e.Name, e.Position)
		if err != nil {
			return err
		}
		if st == nil || st.Fluids[fluid] == 0 {
			return nil
		}
	}
	h.debugShot("flush")
	return fmt.Errorf("act: %s в сети %s не слилась", fluid, e.Name)
}

// flushButton — y середины нижней красной кнопки в столбце FlushX открытого окна.
func (h *Hands) flushButton() (int, bool) {
	const y0, n = 400, 400
	img, err := h.In.Screenshot(input.Rect{X: h.Win.X + h.Layout.FlushX, Y: h.Win.Y + y0, W: 1, H: n})
	if err != nil {
		return 0, false
	}
	red := func(y int) bool {
		c := img.RGBAAt(0, y)
		return c.R > 180 && c.G < 120 && c.B < 120
	}
	last, top := -1, -1
	for y := 0; y < n; y++ {
		if red(y) {
			if y == 0 || !red(y-1) {
				top = y
			}
			last = (top + y) / 2
		}
	}
	if last < 0 {
		return 0, false
	}
	return y0 + last, true
}

// openEntity — открыть окно постройки e кликом. Не открылось, а под курсором дерево (крона
// закрывает постройку: стадия 14 на 794457638, фильтр инсертера) — срубить деревья, чья рамка
// выделения накрывает e, и ещё раз.
func (h *Hands) openEntity(ctx context.Context, e bridge.Entity, shot string) error {
	opened := func(p bridge.Player) bool { return p.OpenedGUIType != nil && *p.OpenedGUIType != 0 }
	for try := 0; ; try++ {
		if err := h.Hover(ctx, e); err != nil && (try > 0 || !errors.Is(err, ErrHover)) {
			return err
		} else if err == nil {
			if err := h.In.Click(input.Left); err != nil {
				return err
			}
			if _, err := h.waitFor(ctx, time.Second, opened); err == nil {
				return nil
			}
		}
		if try > 0 || h.World == nil {
			h.debugShot(shot)
			return fmt.Errorf("act: окно %s не открылось", e.Name)
		}
		if n, err := h.cutCrowns(ctx, e); err != nil {
			return err
		} else if n == 0 {
			h.debugShot(shot)
			return fmt.Errorf("act: окно %s не открылось", e.Name)
		}
	}
}

// cutCrowns — срубить деревья, чья рамка выделения (крона) накрывает постройку e; сколько.
func (h *Hands) cutCrowns(ctx context.Context, e bridge.Entity) (int, error) {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return 0, err
	}
	n := 0
	for _, o := range h.World.ObjectsNear(e.Position, 6) {
		if o.Type != "tree" {
			continue
		}
		b := h.World.SelectionBox(o)
		if !inBox(e.Position, b, 0.6) {
			continue
		}
		t, err := h.B.EntityAt(o.Name, o.Pos)
		if err != nil {
			return n, err
		}
		if t == nil {
			continue
		}
		h.Say("%s под кроной %s — рублю", e.Name, o.Name)
		if err := h.WalkToMine(ctx, *t); err != nil && !errors.Is(err, ErrNotReached) {
			return n, err
		}
		if err := h.Mine(ctx, *t, 20*time.Second); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
