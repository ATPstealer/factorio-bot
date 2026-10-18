// Package act — элементарные действия персонажа: ввод через input, проверка результата через bridge.
package act

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/camera"
	"factorio-bot/internal/input"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

// WindowName — префикс заголовка окна клиента.
const WindowName = "Factorio"

const pollEvery = 50 * time.Millisecond

type Hands struct {
	B      *bridge.Bridge
	In     *input.Input
	Win    input.Rect
	Layout Layout
	Protos *knowledge.Prototypes // справочник: нужен крафту и постройке
	World  *world.World          // модель карты: нужна поиску пути (nil — ходьба по прямой)
	Log    func(format string, args ...any)
	// DebugDir — куда сохранять снимок экрана при сбое действия ("" — не сохранять).
	DebugDir string
	// Console — дублировать Say в консоль игры (видно в окне клиента).
	Console bool
}

// Say — что бот собирается сделать: в лог и (Console) в консоль игры с префиксом [bot].
func (h *Hands) Say(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	h.Log("%s", msg)
	if h.Console {
		h.B.Say("[bot] " + msg)
	}
}

// debugShot сохраняет снимок окна клиента при сбое: имя файла — время и что сломалось.
func (h *Hands) debugShot(what string) {
	if h.DebugDir == "" {
		return
	}
	img, err := h.In.Screenshot(h.Win)
	if err != nil {
		return
	}
	os.MkdirAll(h.DebugDir, 0o755)
	path := filepath.Join(h.DebugDir, time.Now().Format("150405.000")+"-"+what+".png")
	if f, err := os.Create(path); err == nil {
		png.Encode(f, img)
		f.Close()
		h.Log("снимок сбоя: %s", path)
	}
}

func New(b *bridge.Bridge, in *input.Input) (*Hands, error) {
	win, err := in.FindWindow(WindowName)
	if err != nil {
		return nil, err
	}
	return &Hands{B: b, In: in, Win: win, Layout: Layout1856, Log: func(string, ...any) {}}, nil
}

func (h *Hands) Camera(p bridge.Player) camera.Camera {
	return camera.Camera{
		CenterX: p.Position.X, CenterY: p.Position.Y, Zoom: p.Zoom, Scale: p.Scale,
		WinX: h.Win.X, WinY: h.Win.Y, W: p.Resolution.Width, H: p.Resolution.Height,
	}
}

// Focus — курсор в окно клиента (Layout.Focus — не над кнопками меню): без WM фокус
// клавиатуры следует за указателем.
func (h *Hands) Focus() error {
	return h.at(h.Layout.Focus)
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// Ходьба -------------------------------------------------------------------

const deadZone = 0.3 // по оси ближе — клавишу не держим

// walkKeys — какие клавиши держать, чтобы идти из from в to.
func walkKeys(from, to bridge.Pos) map[string]bool { return walkKeysDZ(from, to, deadZone) }

// walkKeysDZ — то же с мёртвой зоной dz по каждой оси.
func walkKeysDZ(from, to bridge.Pos, dz float64) map[string]bool {
	k := map[string]bool{}
	dx, dy := to.X-from.X, to.Y-from.Y
	if dx > dz {
		k["d"] = true
	}
	if dx < -dz {
		k["a"] = true
	}
	if dy > dz {
		k["s"] = true
	}
	if dy < -dz {
		k["w"] = true
	}
	return k
}

// dirKeys — клавиши ближайшего из 8 направлений на точку to.
func dirKeys(from, to bridge.Pos) map[string]bool {
	a := math.Atan2(to.Y-from.Y, to.X-from.X) // y вниз: π/2 — юг
	sector := (int(math.Round(a/(math.Pi/4))) + 8) % 8
	return [8]map[string]bool{
		{"d": true}, {"d": true, "s": true}, {"s": true}, {"s": true, "a": true},
		{"a": true}, {"a": true, "w": true}, {"w": true}, {"w": true, "d": true},
	}[sector]
}

// sidestep — обход препятствия: направление, перпендикулярное основному.
func sidestep(want map[string]bool, flip bool) map[string]bool {
	k := map[string]bool{}
	if want["a"] || want["d"] {
		k[map[bool]string{false: "w", true: "s"}[flip]] = true
	} else {
		k[map[bool]string{false: "a", true: "d"}[flip]] = true
	}
	return k
}

var ErrStuck = errors.New("act: застрял")

// ErrNotReached — доводка не довела до точки (обычно мешает препятствие у цели).
var ErrNotReached = errors.New("act: не дошёл")

// brakeDist — за сколько тайлов до цели отпускать клавиши: по инерции персонаж
// проходит ещё ~0.6–1.2 тайла (задержка RCON-опроса и ввода).
const brakeDist = 1.0

// WalkTo идёт к target клавишами WASD до расстояния stop (не меньше ~0.45 из-за deadZone).
// С моделью мира (Hands.World) — по пути A* в обход воды и препятствий, иначе по прямой.
// По возвращении персонаж стоит.
func (h *Hands) WalkTo(ctx context.Context, target bridge.Pos, stop float64) error {
	if err := h.guard(ctx); err != nil {
		return err
	}
	// конечная точка на ленте — нельзя (унесёт): ближайшая, где можно стоять
	if h.World != nil && h.World.OnBelt(target) {
		best := math.Inf(1)
		t0 := target
		for dy := -2.0; dy <= 2; dy += 0.5 {
			for dx := -2.0; dx <= 2; dx += 0.5 {
				q := bridge.Pos{X: t0.X + dx, Y: t0.Y + dy}
				if d := dist(t0, q); d < best && h.World.Standable(q) {
					best, target = d, q
				}
			}
		}
	}
	if p, err := h.B.Player(); err == nil && dist(p.Position, target) > 8 {
		h.Say("иду к (%.0f, %.0f), %.0f тайлов", target.X, target.Y, dist(p.Position, target))
	}
	if err := h.Focus(); err != nil {
		return err
	}
	t0 := time.Now()
	defer func() {
		h.Log("ходьба до (%.1f, %.1f): всего %.1f с", target.X, target.Y, time.Since(t0).Seconds())
	}()
	if h.World != nil {
		for replan := 0; ; replan++ {
			err := h.followPath(ctx, target, stop)
			if errors.Is(err, world.ErrNoPath) && replan < 2 {
				// обступили свои постройки — поднять, выйти, поставить назад
				p, _ := h.B.Player()
				if h.World.Enclosed(p.Position) {
					if uerr := h.unstuck(ctx); uerr != nil {
						return uerr
					}
					continue
				}
			}
			if errors.Is(err, world.ErrNoPath) {
				// цель за водой/утёсами: дошли до ближайшей достижимой точки — дальше
				// по прямой нельзя (упрёмся), сообщаем честно
				h.waitStill(ctx)
				p, _ := h.B.Player()
				return fmt.Errorf("%w до (%.1f, %.1f): пути нет, стою в (%.2f, %.2f)", ErrNotReached, target.X, target.Y, p.Position.X, p.Position.Y)
			}
			if err == nil {
				break
			}
			if !errors.Is(err, ErrStuck) || replan >= 2 {
				return err
			}
			h.Say("застрял — перестраиваю путь")
		}
	}
	t1 := time.Now()
	if err := h.run(ctx, target, stop+brakeDist, true); err != nil {
		return err
	}
	t2 := time.Now()
	err := h.approach(ctx, target, stop)
	h.Log("ходьба: маршрут %.1f с, остановка %.1f с, доводка %.1f с", t1.Sub(t0).Seconds(), t2.Sub(t1).Seconds(), time.Since(t2).Seconds())
	return err
}

// treeCost — надбавка за ячейку пути сквозь деревья (≈3 ячейки на ствол): срубить дерево
// по дороге — ~2 с, это ~18 тайлов ходьбы. Обход длиннее — рубим.
const treeCost = 6

// cutAhead — за сколько до дерева на маршруте остановиться и рубить (досягаемость ~2.4 до
// рамки, рамка ствола ±0.4).
const cutAhead = 1.8

// followPath проходит маршрут без остановок, кроме рубки деревьев на нём. Держится линии
// (pure pursuit): цель — точка на отрезке на lookahead впереди проекции персонажа;
// клавиши с малой мёртвой зоной сразу поправляют боковое отклонение — отрезки идут под
// любым углом, а клавиши дают только 8 направлений.
func (h *Hands) followPath(ctx context.Context, target bridge.Pos, stop float64) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if dist(p.Position, target) <= stop+brakeDist {
		return nil
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	tr := time.Now()
	r, perr := h.World.Route(p.Position, target, stop, 0, treeCost)
	h.Log("маршрут %.1f тайлов, %d точек, рубить %d; расчёт %.2f с", r.Length(), len(r.Points), len(r.Cuts), time.Since(tr).Seconds())
	if perr != nil && !(errors.Is(perr, world.ErrNoPath) && len(r.Points) > 1) {
		return perr
	}
	route := r.Points
	if len(route) < 2 {
		return perr
	}
	if len(r.Cuts) > 0 {
		h.Say("по дороге срублю деревьев: %d (обход дольше)", len(r.Cuts))
	}
	seg, base, ci := 0, 0.0, 0
	for {
		cutNow := false
		err = h.drive(ctx, 3, func(pos bridge.Pos) (map[string]bool, bool) {
			// проекция на маршрут: текущий отрезок, пройденные — дальше
			var along float64
			for ; seg < len(route)-1; seg++ {
				a, b := route[seg], route[seg+1]
				l := dist(a, b)
				along = ((pos.X-a.X)*(b.X-a.X) + (pos.Y-a.Y)*(b.Y-a.Y)) / l
				if along < l-0.15 || seg == len(route)-2 && along < l {
					break
				}
				base += l
			}
			if seg >= len(route)-1 || base+along >= r.Length()-0.15 {
				return nil, true
			}
			if ci < len(r.Cuts) && base+along >= r.Cuts[ci].S-cutAhead {
				cutNow = true
				return nil, true
			}
			// цель — точка маршрута впереди: в открытом месте дальше (идём прямо, не
			// виляя диагоналями), в тесноте — ближе (точнее держим линию)
			ahead := 1.5
			if h.World.Tight(pos) {
				ahead = 0.5
			}
			return dirKeys(pos, r.At(base+math.Max(along, 0)+ahead)), false
		})
		if err != nil {
			return err
		}
		if !cutNow {
			return perr
		}
		if err := h.waitStill(ctx); err != nil {
			return err
		}
		o := r.Cuts[ci].Tree
		ci++
		sb := h.World.SelectionBox(o)
		e := bridge.Entity{Name: o.Name, Type: o.Type, Position: o.Pos, SelectionBox: &sb}
		if cur, _ := h.B.EntityAt(o.Name, o.Pos); cur == nil {
			continue // уже нет
		}
		if err := h.Mine(ctx, e, 10*time.Second); err != nil && !errors.Is(err, ErrHover) {
			return err
		}
	}
}

// closestOnBox — ближайшая к p точка прямоугольника b (сама p, если она внутри).
func closestOnBox(p bridge.Pos, b bridge.Box) bridge.Pos {
	return bridge.Pos{
		X: math.Max(b.LeftTop.X, math.Min(p.X, b.RightBottom.X)),
		Y: math.Max(b.LeftTop.Y, math.Min(p.Y, b.RightBottom.Y)),
	}
}

// WalkNear подходит к сущности на stop тайлов от края её рамки. Идти к position нельзя:
// у больших сущностей (обломки корабля, камни) центр внутри коллизии, персонаж упрётся в край.
// Путь к ближайшей точке выпуклой рамки саму рамку не пересекает.
func (h *Hands) WalkNear(ctx context.Context, e bridge.Entity, stop float64) error {
	if e.SelectionBox == nil {
		return h.WalkTo(ctx, e.Position, stop)
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	return h.WalkTo(ctx, closestOnBox(p.Position, *e.SelectionBox), stop)
}

// mineReach — досягаемость добычи руками (character resource_reach ≈ 2.7) — для любых
// сущностей, не только руды; считается до рамки коллизии, а не выделения (у деревьев
// рамка выделения высокая — от неё не дотянуться). Подходим с запасом.
const mineReach = 1.5

// collisionBox — рамка коллизии сущности из справочника (нет справочника — рамка выделения).
func (h *Hands) collisionBox(e bridge.Entity) *bridge.Box {
	if h.Protos != nil {
		if pe, ok := h.Protos.Entities[e.Name]; ok {
			b := h.footprint(e.Name, e.Position)
			if e.Direction != nil && (*e.Direction == East || *e.Direction == West) && pe.TileWidth != pe.TileHeight {
				c := e.Position
				b = bridge.Box{
					LeftTop:     bridge.Pos{X: c.X - (b.RightBottom.Y - c.Y), Y: c.Y + (b.LeftTop.X - c.X)},
					RightBottom: bridge.Pos{X: c.X + (c.Y - b.LeftTop.Y), Y: c.Y + (b.RightBottom.X - c.X)},
				}
			}
			return &b
		}
	}
	return e.SelectionBox
}

// WalkToMine подходит к сущности на расстояние добычи руками.
func (h *Hands) WalkToMine(ctx context.Context, e bridge.Entity) error {
	b := h.collisionBox(e)
	if b == nil {
		return h.WalkTo(ctx, e.Position, mineReach)
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	return h.WalkTo(ctx, closestOnBox(p.Position, *b), mineReach)
}

// run — бег с удержанием клавиш до расстояния until; обход при застревании.
// final — остановиться (дождаться, пока персонаж встанет); иначе сразу дальше.
func (h *Hands) run(ctx context.Context, target bridge.Pos, until float64, final bool) error {
	err := h.drive(ctx, 10, func(pos bridge.Pos) (map[string]bool, bool) {
		return walkKeys(pos, target), dist(pos, target) <= until
	})
	if err != nil || !final {
		return err
	}
	return h.waitStill(ctx)
}

// drive держит клавиши, которые возвращает steer, пока он не скажет done. Не идём —
// проверка карты/меню, затем обход в сторону; больше maxStucks застреваний — ErrStuck.
// Клавиши отпускаются при выходе.
func (h *Hands) drive(ctx context.Context, maxStucks int, steer func(bridge.Pos) (map[string]bool, bool)) error {
	held := map[string]bool{}
	release := func() {
		for k := range held {
			h.In.KeyUp(k)
		}
		held = map[string]bool{}
	}
	defer release()

	var (
		last      bridge.Pos
		lastMove  = time.Now()
		sideUntil time.Time
		side      map[string]bool
		stucks    int
	)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		want, done := steer(p.Position)
		if done {
			release()
			return nil
		}
		if dist(p.Position, last) > 0.05 {
			last, lastMove = p.Position, time.Now()
		}
		switch {
		case time.Now().Before(sideUntil):
			want = side
		case time.Since(lastMove) > 500*time.Millisecond:
			// не идём: возможно, открыта карта или меню (ввод до мира не доходит)
			open, _ := h.MenuOpen()
			if open || (p.RenderMode != "" && p.RenderMode != "game") {
				release()
				if err := h.guard(ctx); err != nil {
					return err
				}
				lastMove = time.Now()
				continue
			}
			stucks++
			if stucks == 1 {
				h.debugShot(fmt.Sprintf("stuck-%.0f_%.0f", p.Position.X, p.Position.Y))
			}
			if stucks > maxStucks {
				return fmt.Errorf("%w у (%.1f, %.1f)", ErrStuck, p.Position.X, p.Position.Y)
			}
			side = sidestep(want, stucks%2 == 0)
			sideUntil = time.Now().Add(400 * time.Millisecond)
			lastMove = time.Now()
			h.Log("застрял у (%.1f, %.1f), обхожу %v", p.Position.X, p.Position.Y, side)
			want = side
		}
		for k := range held {
			if !want[k] {
				if err := h.In.KeyUp(k); err != nil {
					return err
				}
				delete(held, k)
			}
		}
		for k := range want {
			if !held[k] {
				if err := h.In.KeyDown(k); err != nil {
					return err
				}
				held[k] = true
			}
		}
		time.Sleep(pollEvery)
	}
}

// approach — доводка короткими нажатиями с остановкой после каждого. Длительность
// нажатия адаптивная: слишком короткое игра может не заметить (ввод читается раз в тик),
// слишком длинное — перелёт.
func (h *Hands) approach(ctx context.Context, target bridge.Pos, stop float64) error {
	pulse := 40 * time.Millisecond
	for i := 0; i < 15; i++ {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		d := dist(p.Position, target)
		keys := walkKeys(p.Position, target)
		if d <= stop || len(keys) == 0 {
			return nil
		}
		for k := range keys {
			if err := h.In.KeyDown(k); err != nil {
				return err
			}
		}
		sleep(ctx, pulse)
		for k := range keys {
			h.In.KeyUp(k)
		}
		if err := h.waitStill(ctx); err != nil {
			return err
		}
		q, err := h.B.Player()
		if err != nil {
			return err
		}
		switch moved := dist(p.Position, q.Position); {
		case moved < 0.05:
			pulse = min(pulse*2, 300*time.Millisecond)
		case moved > d:
			pulse = max(pulse/2, 10*time.Millisecond)
		}
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if dist(p.Position, target) <= stop { // последнее нажатие довело
		return nil
	}
	return fmt.Errorf("%w до (%.1f, %.1f): стою в (%.2f, %.2f)", ErrNotReached, target.X, target.Y, p.Position.X, p.Position.Y)
}

// inputLatency — через сколько ввод клиента доходит до сервера (1–3 тика) с запасом.
const inputLatency = 100 * time.Millisecond

// waitStill ждёт остановки персонажа (после отпускания клавиш он проходит ещё ~1 тайл).
// Сначала выжидает задержку ввода — иначе «стоит» читается до того, как нажатие дошло
// до сервера. Стоит — когда два чтения подряд: не идёт и та же позиция.
func (h *Hands) waitStill(ctx context.Context) error {
	if err := sleep(ctx, inputLatency); err != nil {
		return err
	}
	var last *bridge.Pos
	for i := 0; i < 60; i++ {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		still := p.Character == nil || !p.Character.Walking
		if still && last != nil && dist(*last, p.Position) < 1e-6 {
			return nil
		}
		// клавиши отпущены, а персонажа несёт — стоит на ленте: сойти
		if still && last != nil && i > 10 && h.World != nil && h.World.OnBelt(p.Position) {
			if err := h.stepOffBelt(ctx, p.Position); err != nil {
				return err
			}
			last = nil
			continue
		}
		if still {
			pos := p.Position
			last = &pos
		} else {
			last = nil
		}
		if err := sleep(ctx, pollEvery); err != nil {
			return err
		}
	}
	return errors.New("act: персонаж не остановился")
}

// Мышь по сущностям ----------------------------------------------------------

// hoverPoints — куда пробовать навести курсор: центр selection_box, затем сетка внутри
// него (соседние сущности могут перекрывать часть рамки), ближние к центру — раньше.
func hoverPoints(e bridge.Entity) []bridge.Pos {
	if e.SelectionBox == nil {
		return []bridge.Pos{e.Position}
	}
	b := *e.SelectionBox
	c := b.Center()
	pts := []bridge.Pos{c}
	const n = 4
	for i := 0; i <= n; i++ {
		for j := 0; j <= n; j++ {
			// отступ 10% от краёв рамки
			x := b.LeftTop.X + (b.RightBottom.X-b.LeftTop.X)*(0.1+0.8*float64(i)/n)
			y := b.LeftTop.Y + (b.RightBottom.Y-b.LeftTop.Y)*(0.1+0.8*float64(j)/n)
			pts = append(pts, bridge.Pos{X: x, Y: y})
		}
	}
	sort.SliceStable(pts[1:], func(i, j int) bool { return dist(pts[1+i], c) < dist(pts[1+j], c) })
	return pts
}

func same(a *bridge.Entity, b bridge.Entity) bool {
	return a != nil && a.Name == b.Name && dist(a.Position, b.Position) < 0.01
}

// ErrHover — курсор не навёлся: сущность вне экрана или полностью перекрыта соседями.
var ErrHover = errors.New("act: не удалось навести курсор")

// Hover наводит курсор на сущность; подтверждение — player.selected. Персонаж должен стоять.
// Не вышло — вторая попытка с паузой (камера могла ещё доезжать, выделение — запаздывать).
func (h *Hands) Hover(ctx context.Context, e bridge.Entity) error {
	err := h.hoverOnce(ctx, e)
	if !errors.Is(err, ErrHover) {
		return err
	}
	if err := sleep(ctx, 300*time.Millisecond); err != nil {
		return err
	}
	if err = h.hoverOnce(ctx, e); errors.Is(err, ErrHover) {
		h.debugShot("hover-" + e.Name)
	}
	return err
}

func (h *Hands) hoverOnce(ctx context.Context, e bridge.Entity) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	cam := h.Camera(p)
	// соседние объекты: курсор не ставим в их рамки выделения — иначе ПКМ добудет их
	// (например, свою печь рядом с рудой)
	var others []bridge.Box
	if h.World != nil {
		for _, o := range h.World.ObjectsNear(e.Position, 6) {
			if o.Name == e.Name && dist(o.Pos, e.Position) < 0.01 {
				continue
			}
			others = append(others, h.World.SelectionBox(o))
		}
	}
	for _, pt := range hoverPoints(e) {
		if !cam.Visible(pt.X, pt.Y, 20) {
			continue
		}
		covered := false
		for _, b := range others {
			covered = covered || inBox(pt, b, 0.15)
		}
		if covered {
			continue
		}
		if err := h.In.Move(cam.ToScreen(pt.X, pt.Y)); err != nil {
			return err
		}
		if err := sleep(ctx, 40*time.Millisecond); err != nil {
			return err
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		if same(p.Selected, e) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s (%.1f, %.1f)", ErrHover, e.Name, e.Position.X, e.Position.Y)
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// CloseGUI закрывает открытое окно (инвентарь, окно сущности) клавишей Esc.
// Esc жмём только при открытом окне: без него Esc открывает меню игры.
// Открытое окно перехватывает ввод — перед действиями в мире его надо закрыть.
func (h *Hands) CloseGUI(ctx context.Context) error {
	closed := func(p bridge.Player) bool { return p.OpenedGUIType == nil || *p.OpenedGUIType == 0 }
	for i := 0; i < 3; i++ {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		if closed(p) {
			return nil
		}
		if err := h.Focus(); err != nil {
			return err
		}
		if err := h.In.Tap("escape"); err != nil {
			return err
		}
		// ждать закрытия с запасом на задержку ввода: повторный Esc по уже закрытому
		// окну откроет меню игры
		if _, err := h.waitFor(ctx, time.Second, closed); err == nil {
			return nil
		} else if !errors.Is(err, errTimeout) {
			return err
		}
	}
	return errors.New("act: окно не закрывается")
}

// unstuck — выбраться, когда персонажа обступили свои постройки: поднять ближайшие (пока
// не откроется выход), выйти и поставить их на место с тем же направлением.
func (h *Hands) unstuck(ctx context.Context) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	near, err := h.B.FindNearest(bridge.FindArgs{Radius: 3, Limit: 12})
	if err != nil {
		return err
	}
	type lifted struct {
		item string
		pos  bridge.Pos
		dir  int
	}
	var back []lifted
	for _, e := range near {
		item := h.Protos.ItemFor(e.Name)
		if item == "" || e.Type == "character" {
			continue
		}
		if st, err := h.B.GetEntity(e.Name, e.Position); err != nil || st == nil || !st.Own {
			continue
		}
		h.Say("обступили постройки — поднимаю %s, выйду и поставлю назад", e.Name)
		d := 0
		if e.Direction != nil {
			d = *e.Direction
		}
		if err := h.Pickup(ctx, e); err != nil {
			return err
		}
		back = append(back, lifted{item, e.Position, d})
		if _, err := h.World.Sync(h.B, false); err != nil {
			return err
		}
		if !h.World.Enclosed(p.Position) {
			break
		}
	}
	if h.World.Enclosed(p.Position) {
		return fmt.Errorf("act: не выбраться из (%.1f, %.1f)", p.Position.X, p.Position.Y)
	}
	// выйти на 4 тайла туда, куда есть путь
	for k := 0; k < 8; k++ {
		a := float64(k) * math.Pi / 4
		to := bridge.Pos{X: p.Position.X + 4*math.Cos(a), Y: p.Position.Y + 4*math.Sin(a)}
		if r, err := h.World.Route(p.Position, to, 0.5, 20000, 0); err == nil && len(r.Points) > 1 && h.World.Standable(to) {
			if err := h.followPath(ctx, to, 0.5); err != nil {
				return err
			}
			break
		}
	}
	for i := len(back) - 1; i >= 0; i-- {
		if _, err := h.PlaceAll(ctx, back[i].item, []bridge.Pos{back[i].pos}, back[i].dir); err != nil {
			return err
		}
	}
	return nil
}

// stepOffBelt — сойти с ленты на ближайшую точку, где можно стоять.
func (h *Hands) stepOffBelt(ctx context.Context, from bridge.Pos) error {
	best, spot := math.Inf(1), bridge.Pos{}
	for dy := -2.0; dy <= 2; dy += 0.5 {
		for dx := -2.0; dx <= 2; dx += 0.5 {
			q := bridge.Pos{X: from.X + dx, Y: from.Y + dy}
			if h.World.Standable(q) && dist(from, q) < best {
				best, spot = dist(from, q), q
			}
		}
	}
	if math.IsInf(best, 1) {
		return errors.New("act: персонажа несёт лента, сойти некуда")
	}
	h.Log("на ленте — схожу в (%.1f, %.1f)", spot.X, spot.Y)
	return h.drive(ctx, 3, func(pos bridge.Pos) (map[string]bool, bool) {
		return walkKeys(pos, spot), !h.World.OnBelt(pos) && dist(pos, spot) < 0.6
	})
}
