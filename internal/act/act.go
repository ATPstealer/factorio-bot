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

	altChecked bool        // Alt-режим проверен и включён
	goal       *bridge.Pos // куда идёт WalkTo: сходить с ленты — в её сторону
	qbNext     int         // быстрая панель полна — следующий слот под новый предмет (по кругу)
	handDir    int         // поворот предмета в руке (игра помнит его и между взятиями)
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

// longHop — участок дальней ходьбы, тайлов (A* на сотни тайлов упирается в лимит).
const longHop = 120.0

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
	h.goal = &target
	defer func() { h.goal = nil }()
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
	// далеко (сотни тайлов): путь целиком не укладывается в поиск — участками по ~120
	// тайлов к точкам на прямой до цели (где можно стоять)
	if h.World != nil {
		for hop := 0; hop < 20; hop++ {
			p, err := h.B.Player()
			if err != nil {
				return err
			}
			d := dist(p.Position, target)
			if d <= longHop*1.3 {
				break
			}
			k := longHop / d
			mid := bridge.Pos{X: p.Position.X + (target.X-p.Position.X)*k, Y: p.Position.Y + (target.Y-p.Position.Y)*k}
			spot, ok := h.World.FreeSpot(mid, 1, 1, 20, nil)
			if !ok {
				break
			}
			if err := h.followPath(ctx, spot, 3); err != nil && !errors.Is(err, world.ErrNoPath) && !errors.Is(err, ErrNotReached) {
				return err
			}
			if q, err := h.B.Player(); err == nil && dist(q.Position, p.Position) < 10 {
				break // не продвинулись — дальше обычным путём (пролом и т.п.)
			}
		}
	}
	if h.World != nil {
		unstuckTried := false // вышел из тесноты, а пути всё нет — карман: дальше пролом
		for replan := 0; ; replan++ {
			err := h.followPath(ctx, target, stop)
			if errors.Is(err, world.ErrNoPath) && replan < 3 {
				// обступили свои постройки — поднять, выйти, поставить назад
				p, _ := h.B.Player()
				if !unstuckTried && h.World.Enclosed(p.Position) {
					unstuckTried = true
					if uerr := h.unstuck(ctx); uerr != nil {
						return uerr
					}
					continue
				}
				// щель уже запаса модели (персонаж сюда как-то зашёл) — протиснуться
				if replan == 0 {
					h.World.Squeeze = true
					serr := h.followPath(ctx, target, stop)
					h.World.Squeeze = false
					if serr == nil {
						break
					}
				}
				// за кольцом своих построек (трубы вокруг химзаводов) — пролом
				if ok, berr := h.breach(ctx, target); berr != nil {
					return berr
				} else if ok {
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
	for again := 0; ; again++ {
		err := h.run(ctx, target, stop+brakeDist, true)
		if err == nil {
			break
		}
		// застрял на прямом доходе (лес гуще модели, стадия 12 на 859088572) — щель отмечена
		// (MarkStuck): путь заново и снова
		if !errors.Is(err, ErrStuck) || h.World == nil || again >= 2 {
			return err
		}
		h.Say("застрял — перестраиваю путь")
		if ferr := h.followPath(ctx, target, stop); ferr != nil && !errors.Is(ferr, ErrStuck) {
			return ferr
		}
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
			b := h.footprint(e.Name, e.Position, dirOf(e))
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
		cuts      int // срублено застряв (деревья впереди)
	)
	const maxCuts = 40
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		want, done := steer(p.Position)
		ahead := want
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
			// застрял в лесу (щели уже модели, стадия 12 на 859088572) — срубить дерево впереди
			// и идти дальше: устранимое не обходить (правило пользователя)
			if cuts < maxCuts {
				if t, ok := h.treeAhead(p.Position, ahead); ok {
					cuts++
					release()
					h.Log("застрял в лесу у (%.1f, %.1f) — рублю %s впереди", p.Position.X, p.Position.Y, t.Name)
					if e, err := h.B.EntityAt(t.Name, t.Pos); err == nil && e != nil {
						if err := h.Mine(ctx, *e, 10*time.Second); err != nil && ctx.Err() != nil {
							return err
						}
					}
					lastMove = time.Now()
					continue
				}
			}
			stucks++
			if stucks == 1 {
				h.debugShot(fmt.Sprintf("stuck-%.0f_%.0f", p.Position.X, p.Position.Y))
			}
			if stucks > maxStucks {
				if h.World != nil { // не идти этой щелью снова
					var dx, dy float64
					for k, v := range map[string][2]float64{"d": {1, 0}, "a": {-1, 0}, "s": {0, 1}, "w": {0, -1}} {
						if ahead[k] {
							dx, dy = dx+v[0], dy+v[1]
						}
					}
					h.World.MarkStuck(p.Position, dx, dy)
				}
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

// treeAhead — ближайшее дерево впереди (по клавишам ahead) в досягаемости рубки от рамки
// коллизии персонажа.
func (h *Hands) treeAhead(at bridge.Pos, ahead map[string]bool) (world.Object, bool) {
	if h.World == nil {
		return world.Object{}, false
	}
	var dx, dy float64
	for k, v := range map[string][2]float64{"d": {1, 0}, "a": {-1, 0}, "s": {0, 1}, "w": {0, -1}} {
		if ahead[k] {
			dx, dy = dx+v[0], dy+v[1]
		}
	}
	best, bd := world.Object{}, math.Inf(1)
	for _, o := range h.World.ObjectsNear(at, 3) {
		if o.Type != "tree" {
			continue
		}
		vx, vy := o.Pos.X-at.X, o.Pos.Y-at.Y
		if vx*dx+vy*dy <= 0 { // позади или сбоку
			continue
		}
		b := h.World.CollisionBox(o)
		if d := dist(at, closestOnBox(at, b)); d <= mineReachGame && d < bd {
			best, bd = o, d
		}
	}
	return best, !math.IsInf(bd, 1)
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
	offs := 0
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
			if offs++; offs > 3 {
				return fmt.Errorf("%w: не сойти с ленты у (%.1f, %.1f)", ErrStuck, p.Position.X, p.Position.Y)
			}
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
	if err = h.hoverOnce(ctx, e); !errors.Is(err, ErrHover) {
		return err
	}
	// мир может закрывать окно, которого мод не видит (фильтр слота) — закрыть и снова
	if err := h.closeStray(ctx); err != nil {
		return err
	}
	if err = h.hoverOnce(ctx, e); !errors.Is(err, ErrHover) {
		return err
	}
	// у края экрана объект могут закрывать панели интерфейса — подойти (камера следует
	// за персонажем, объект окажется ближе к середине) и навести снова
	if werr := h.WalkNear(ctx, e, 1.5); werr == nil || errors.Is(werr, ErrNotReached) {
		if err = h.hoverOnce(ctx, e); !errors.Is(err, ErrHover) {
			return err
		}
	}
	// цель под кроной дерева (крона выделяется первой) — срубить накрывающие деревья
	if cut, cerr := h.cutCovering(ctx, e); cerr != nil {
		return cerr
	} else if cut {
		if err = h.hoverOnce(ctx, e); !errors.Is(err, ErrHover) {
			return err
		}
	}
	h.debugShot("hover-" + e.Name)
	return err
}

// cutCovering — срубить деревья, чья рамка выделения (крона) накрывает середину рамки
// выделения постройки e. true — что-то срублено.
func (h *Hands) cutCovering(ctx context.Context, e bridge.Entity) (bool, error) {
	if h.World == nil || e.Type == "tree" { // деревья — Mine сам рубит соседей
		return false, nil
	}
	c := e.Position
	if e.SelectionBox != nil {
		c = bridge.Pos{X: (e.SelectionBox.LeftTop.X + e.SelectionBox.RightBottom.X) / 2, Y: (e.SelectionBox.LeftTop.Y + e.SelectionBox.RightBottom.Y) / 2}
	}
	cut := false
	for _, o := range h.World.ObjectsNear(c, 6) {
		if o.Type != "tree" || o.Name == e.Name && dist(o.Pos, e.Position) < 0.01 || !inBox(c, h.World.SelectionBox(o), 0) {
			continue
		}
		t, err := h.B.EntityAt(o.Name, o.Pos)
		if err != nil {
			return cut, err
		}
		if t == nil {
			continue
		}
		h.Say("%s под кроной %s — рублю дерево", e.Name, o.Name)
		if err := h.WalkToMine(ctx, *t); err != nil && !errors.Is(err, ErrNotReached) {
			return cut, err
		}
		if err := h.mineOnce(ctx, *t, 20*time.Second); err != nil {
			return cut, err
		}
		cut = true
	}
	if cut {
		_, err := h.World.Sync(h.B, false)
		return cut, err
	}
	return false, nil
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
	// сначала точки вне чужих рамок; целиком перекрыт (сухой ствол под кроной) — и
	// перекрытые: игра может выделить и его, подтверждение всё равно по selected
	for pass := 0; pass < 2; pass++ {
		for _, pt := range hoverPoints(e) {
			if !cam.Visible(pt.X, pt.Y, 20) {
				continue
			}
			if sx, sy := cam.ToScreen(pt.X, pt.Y); h.underPanel(sx, sy) {
				continue
			}
			covered := false
			for _, b := range others {
				covered = covered || inBox(pt, b, 0.15)
			}
			if covered != (pass == 1) {
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

// breachable — свои постройки, которые можно поднять, чтобы пройти, и поставить назад
// (ленты не мешают — по ним ходят; рецепт сборщика восстановит стройка).
func breachable(o world.Object) bool {
	return o.Own && o.Type != "transport-belt" && o.Type != "character"
}

// breach — пути до target нет, но появится, если поднять одну-три свои постройки рядом с
// ботом или с целью (кольцо своих труб, карман у станции): поднять, пройти на 2.5 тайла
// дальше по пути и поставить назад. false — таких нет (цель за водой/утёсами).
func (h *Hands) breach(ctx context.Context, target bridge.Pos) (bool, error) {
	p, err := h.B.Player()
	if err != nil {
		return false, err
	}
	seen := map[bridge.Pos]bool{}
	var objs []world.Object
	for _, o := range append(h.World.ObjectsNear(p.Position, 15), h.World.ObjectsNear(target, 30)...) {
		if breachable(o) && h.Protos.ItemFor(o.Name) != "" && !seen[o.Pos] {
			seen[o.Pos] = true
			objs = append(objs, o)
		}
	}
	// сначала ближние к прямой «бот — цель»
	off := func(o world.Object) float64 { return dist(p.Position, o.Pos) + dist(o.Pos, target) }
	sort.Slice(objs, func(i, j int) bool { return off(objs[i]) < off(objs[j]) })
	b0, _ := h.World.Route(p.Position, target, 1.5, 300000, 6)
	base := b0.Length()
	// годится — путь до цели или хотя бы на 20+ тайлов дальше, чем сейчас (до дальней цели
	// путь целиком может не уложиться в лимит)
	try := func(set []world.Object) (world.Route, bool) {
		var route world.Route
		ok := false
		var rec func(i int)
		rec = func(i int) {
			if i == len(set) {
				r, err := h.World.Route(p.Position, target, 1.5, 300000, 6)
				route, ok = r, err == nil || r.Length() > base+20
				return
			}
			h.World.Without(set[i], func() { rec(i + 1) })
		}
		rec(0)
		return route, ok
	}
	var set []world.Object
	var route world.Route
	found := false
	for i := 0; i < len(objs) && i < 30 && !found; i++ {
		set = []world.Object{objs[i]}
		route, found = try(set)
	}
	// пары и тройки — из ближайших к боту (две параллельные трубы — нефть и газ)
	close := append([]world.Object(nil), objs...)
	sort.Slice(close, func(i, j int) bool { return dist(close[i].Pos, p.Position) < dist(close[j].Pos, p.Position) })
	for i := 0; i < len(close) && i < 10 && !found; i++ {
		for j := i + 1; j < len(close) && j < 10 && !found; j++ {
			set = []world.Object{close[i], close[j]}
			route, found = try(set)
		}
	}
	for i := 0; i < len(close) && i < 7 && !found; i++ {
		for j := i + 1; j < len(close) && j < 7 && !found; j++ {
			for k := j + 1; k < len(close) && k < 7 && !found; k++ {
				set = []world.Object{close[i], close[j], close[k]}
				route, found = try(set)
			}
		}
	}
	if !found {
		h.Log("пролом: из %d своих построек рядом ни одна (и пары, тройки) не открывает путь", len(objs))
		return false, nil
	}
	for _, o := range set {
		e, err := h.B.EntityAt(o.Name, o.Pos)
		if err != nil || e == nil {
			return false, err
		}
		h.Say("путь закрыт своими постройками — поднимаю %s, пройду и поставлю назад", o.Name)
		if err := h.Pickup(ctx, *e); err != nil {
			return false, err
		}
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		return false, err
	}
	// точка пути за поднятым: после ближайшего к нему места, в 2.5 тайла от всех
	var pts []bridge.Pos
	for i := 1; i < len(route.Points); i++ {
		a, b := route.Points[i-1], route.Points[i]
		n := int(dist(a, b)/0.25) + 1
		for k := 1; k <= n; k++ {
			f := float64(k) / float64(n)
			pts = append(pts, bridge.Pos{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f})
		}
	}
	near := func(q bridge.Pos) float64 {
		d := math.Inf(1)
		for _, o := range set {
			d = math.Min(d, dist(q, o.Pos))
		}
		return d
	}
	closest := 0
	for i, q := range pts {
		if near(q) < near(pts[closest]) {
			closest = i
		}
	}
	past := target
	for _, q := range pts[closest:] {
		if near(q) >= 2.5 {
			past = q
			break
		}
	}
	if err := h.followPath(ctx, past, 0.5); err != nil && !errors.Is(err, world.ErrNoPath) {
		return false, err
	}
	for i := len(set) - 1; i >= 0; i-- {
		o := set[i]
		if _, err := h.PlaceAll(ctx, h.Protos.ItemFor(o.Name), []bridge.Pos{o.Pos}, o.Direction); err != nil {
			return false, err
		}
	}
	return true, nil
}

// stepOffBelt — сойти с ленты на ближайшую точку, где можно стоять.
func (h *Hands) stepOffBelt(ctx context.Context, from bridge.Pos) error {
	// только что поставленные ленты модель могла ещё не видеть — иначе «сойти» на ленту
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	type cand struct {
		q bridge.Pos
		d float64
	}
	var cs []cand
	for dy := -4.0; dy <= 4; dy += 0.5 {
		for dx := -4.0; dx <= 4; dx += 0.5 {
			q := bridge.Pos{X: from.X + dx, Y: from.Y + dy}
			if h.World.Standable(q) {
				d := dist(from, q)
				if h.goal != nil { // идём к цели — сходить в её сторону, а не куда ближе
					d += dist(q, *h.goal)
				}
				if h.nearBelt(q) {
					d += 3 // щель у ленты (коридор между бурами) — снова снесёт
				}
				cs = append(cs, cand{q, d})
			}
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	// ближняя может быть за стволом, которого модель не знает, — по очереди несколько,
	// отличающихся хотя бы на тайл
	var tried []bridge.Pos
	for _, c := range cs {
		if len(tried) == 4 {
			break
		}
		near := false
		for _, t := range tried {
			near = near || dist(t, c.q) < 1
		}
		if near {
			continue
		}
		tried = append(tried, c.q)
		spot := c.q
		h.Log("на ленте — схожу в (%.1f, %.1f)", spot.X, spot.Y)
		err := h.drive(ctx, 2, func(pos bridge.Pos) (map[string]bool, bool) {
			return walkKeys(pos, spot), !h.World.OnBelt(pos) && dist(pos, spot) < 0.6
		})
		if err == nil || !errors.Is(err, ErrStuck) {
			return err
		}
	}
	// вбок не сойти (коридор между бурами, щель в тайл) — по ходу ленты до её конца: лента
	// сама выводит из коридора
	if d, ok := h.beltDir(from); ok {
		v := map[int][2]float64{0: {0, -1}, 4: {1, 0}, 8: {0, 1}, 12: {-1, 0}}[d]
		goal := bridge.Pos{X: from.X + 40*v[0], Y: from.Y + 40*v[1]}
		h.Log("на ленте — вбок не сойти, иду по ходу ленты")
		err := h.drive(ctx, 2, func(pos bridge.Pos) (map[string]bool, bool) {
			return walkKeys(pos, goal), !h.World.OnBelt(pos)
		})
		if err == nil || !errors.Is(err, ErrStuck) {
			return err
		}
	}
	return errors.New("act: персонажа несёт лента, сойти некуда")
}

// beltDir — направление ленты под точкой p.
func (h *Hands) beltDir(p bridge.Pos) (int, bool) {
	t := world.TileOf(p)
	for _, o := range h.World.ObjectsNear(p, 1.5) {
		if (o.Type == "transport-belt" || o.Type == "underground-belt") && world.TileOf(o.Pos) == t {
			return o.Direction, true
		}
	}
	return 0, false
}

// dirOf — направление сущности (нет — север).
func dirOf(e bridge.Entity) int {
	if e.Direction != nil {
		return *e.Direction
	}
	return 0
}
