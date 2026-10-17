// Package act — элементарные действия персонажа: ввод через input, проверка результата через bridge.
package act

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/camera"
	"factorio-bot/internal/input"
)

// WindowName — префикс заголовка окна клиента.
const WindowName = "Factorio"

const pollEvery = 50 * time.Millisecond

type Hands struct {
	B   *bridge.Bridge
	In  *input.Input
	Win input.Rect
	Log func(format string, args ...any)
}

func New(b *bridge.Bridge, in *input.Input) (*Hands, error) {
	win, err := in.FindWindow(WindowName)
	if err != nil {
		return nil, err
	}
	return &Hands{B: b, In: in, Win: win, Log: func(string, ...any) {}}, nil
}

func (h *Hands) Camera(p bridge.Player) camera.Camera {
	return camera.Camera{
		CenterX: p.Position.X, CenterY: p.Position.Y, Zoom: p.Zoom, Scale: p.Scale,
		WinX: h.Win.X, WinY: h.Win.Y, W: p.Resolution.Width, H: p.Resolution.Height,
	}
}

// Focus — курсор в центр окна клиента: без WM фокус клавиатуры следует за указателем.
func (h *Hands) Focus() error {
	return h.In.Move(h.Win.X+h.Win.W/2, h.Win.Y+h.Win.H/2)
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// Ходьба -------------------------------------------------------------------

const deadZone = 0.3 // по оси ближе — клавишу не держим

// walkKeys — какие клавиши держать, чтобы идти из from в to.
func walkKeys(from, to bridge.Pos) map[string]bool {
	k := map[string]bool{}
	dx, dy := to.X-from.X, to.Y-from.Y
	if dx > deadZone {
		k["d"] = true
	}
	if dx < -deadZone {
		k["a"] = true
	}
	if dy > deadZone {
		k["s"] = true
	}
	if dy < -deadZone {
		k["w"] = true
	}
	return k
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
// По возвращении персонаж стоит.
func (h *Hands) WalkTo(ctx context.Context, target bridge.Pos, stop float64) error {
	if err := h.Focus(); err != nil {
		return err
	}
	if err := h.run(ctx, target, stop+brakeDist); err != nil {
		return err
	}
	return h.approach(ctx, target, stop)
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

// run — бег с удержанием клавиш до расстояния until; обход при застревании.
func (h *Hands) run(ctx context.Context, target bridge.Pos, until float64) error {
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
		if dist(p.Position, target) <= until {
			release()
			return h.waitStill(ctx)
		}
		if dist(p.Position, last) > 0.05 {
			last, lastMove = p.Position, time.Now()
		}
		want := walkKeys(p.Position, target)
		switch {
		case time.Now().Before(sideUntil):
			want = side
		case time.Since(lastMove) > 500*time.Millisecond:
			stucks++
			if stucks > 10 {
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
func (h *Hands) Hover(ctx context.Context, e bridge.Entity) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	cam := h.Camera(p)
	for _, pt := range hoverPoints(e) {
		if !cam.Visible(pt.X, pt.Y, 20) {
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
	for i := 0; i < 3; i++ {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		if p.OpenedGUIType == nil || *p.OpenedGUIType == 0 {
			return nil
		}
		if err := h.Focus(); err != nil {
			return err
		}
		if err := h.In.Tap("escape"); err != nil {
			return err
		}
		if err := sleep(ctx, 150*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("act: окно не закрывается")
}
