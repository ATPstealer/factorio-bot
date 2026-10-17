package act

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
	"factorio-bot/internal/world"
)

// Направления 2.0 (16 румбов): постройки поворачиваются по 4.
const (
	North = 0
	East  = 4
	South = 8
	West  = 12
)

// ErrPlace — постройка не встала (место занято, далеко, не та клетка).
var ErrPlace = errors.New("act: не удалось поставить")

// footprint — рамка коллизии постройки item в точке pos (без поворота).
func (h *Hands) footprint(entity string, pos bridge.Pos) bridge.Box {
	b := bridge.Box{LeftTop: bridge.Pos{X: -0.5, Y: -0.5}, RightBottom: bridge.Pos{X: 0.5, Y: 0.5}}
	if e, ok := h.Protos.Entities[entity]; ok {
		b = e.CollisionBox
	}
	return bridge.Box{
		LeftTop:     bridge.Pos{X: pos.X + b.LeftTop.X, Y: pos.Y + b.LeftTop.Y},
		RightBottom: bridge.Pos{X: pos.X + b.RightBottom.X, Y: pos.Y + b.RightBottom.Y},
	}
}

func inBox(p bridge.Pos, b bridge.Box, margin float64) bool {
	return p.X > b.LeftTop.X-margin && p.X < b.RightBottom.X+margin && p.Y > b.LeftTop.Y-margin && p.Y < b.RightBottom.Y+margin
}

// buildReach — с какого расстояния ставим и кладём не сходя с места (досягаемость
// персонажа 10, с запасом). Важно, например, когда персонаж строит вокруг себя.
const buildReach = 8.5

// standNear — подойти к месту постройки: на расстояние досягаемости, но не на саму
// площадку (персонаж мешает поставить).
func (h *Hands) standNear(ctx context.Context, fp bridge.Box) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	c := fp.Center()
	if !inBox(p.Position, fp, 0.8) && dist(p.Position, c) < buildReach {
		return nil
	}
	// встать к югу от площадки (камера центрирована на игроке — площадка будет на экране)
	spot := bridge.Pos{X: c.X, Y: fp.RightBottom.Y + 1.5}
	return h.WalkTo(ctx, spot, 0.5)
}

// Place ставит постройку из предмета item центром в pos и поворачивает в dir.
func (h *Hands) Place(ctx context.Context, item string, pos bridge.Pos, dir int) (bridge.Entity, error) {
	es, err := h.PlaceAll(ctx, item, []bridge.Pos{pos}, dir)
	if len(es) == 0 {
		return bridge.Entity{}, err
	}
	return es[0], err
}

// PlaceAll ставит постройки из item в точки positions (в заданном порядке) направлением dir.
// Предмет берётся в руку один раз и не убирается между постройками; подходит, только если
// точка вне досягаемости. Подтверждение каждой — событие built_entity в этой точке.
// Направление: с предметом в руке R поворачивает сам предмет (а не постройку под курсором),
// поэтому после первой постройки руку доворачиваем R, а уже поставленные с неверным
// направлением поворачиваем в конце пустой рукой.
func (h *Hands) PlaceAll(ctx context.Context, item string, positions []bridge.Pos, dir int) ([]bridge.Entity, error) {
	it, ok := h.Protos.Items[item]
	if !ok || it.PlaceResult == "" {
		return nil, fmt.Errorf("act: %s не ставится", item)
	}
	if err := h.guard(ctx); err != nil {
		return nil, err
	}
	if len(positions) == 1 {
		h.Say("ставлю %s в (%.1f, %.1f)%s", it.PlaceResult, positions[0].X, positions[0].Y, dirName(dir))
	} else {
		h.Say("ставлю %s ×%d%s", it.PlaceResult, len(positions), dirName(dir))
	}
	var built []bridge.Entity
	var misrotated []int
	defer h.ClearCursor(ctx)
	for _, pos := range positions {
		if err := h.standNear(ctx, h.footprint(it.PlaceResult, pos)); err != nil {
			return built, err
		}
		if err := h.Pick(ctx, item); err != nil {
			return built, err
		}
		e, err := h.clickBuild(ctx, it.PlaceResult, pos)
		if err != nil {
			return built, err
		}
		built = append(built, e)
		if e.Direction != nil && *e.Direction != dir {
			misrotated = append(misrotated, len(built)-1)
			// довернуть предмет в руке: R — 90° по часовой (шаг 4)
			for k := ((dir-*e.Direction)/4%4 + 4) % 4; k > 0; k-- {
				if err := h.In.Tap("r"); err != nil {
					return built, err
				}
			}
		}
	}
	if err := h.ClearCursor(ctx); err != nil {
		return built, err
	}
	for _, i := range misrotated {
		if err := h.Rotate(ctx, built[i], dir); err != nil {
			return built, err
		}
		d := dir
		built[i].Direction = &d
	}
	return built, nil
}

// clickBuild — ЛКМ по точке с постройкой в руке и ожидание события built_entity.
func (h *Hands) clickBuild(ctx context.Context, entity string, pos bridge.Pos) (bridge.Entity, error) {
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		return bridge.Entity{}, err
	}
	p, err := h.B.Player()
	if err != nil {
		return bridge.Entity{}, err
	}
	if err := h.In.Move(h.Camera(p).ToScreen(pos.X, pos.Y)); err != nil {
		return bridge.Entity{}, err
	}
	if err := sleep(ctx, 40*time.Millisecond); err != nil {
		return bridge.Entity{}, err
	}
	if err := h.In.Click(input.Left); err != nil {
		return bridge.Entity{}, err
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := sleep(ctx, pollEvery); err != nil {
			return bridge.Entity{}, err
		}
		evs, _, err := h.B.PollEvents(seq0)
		if err != nil {
			return bridge.Entity{}, err
		}
		for _, e := range evs {
			if e.Type == "built_entity" && e.Entity != nil && e.Entity.Name == entity &&
				math.Abs(e.Entity.Position.X-pos.X) < 0.01 && math.Abs(e.Entity.Position.Y-pos.Y) < 0.01 {
				return *e.Entity, nil
			}
		}
	}
	h.debugShot("place-" + entity)
	return bridge.Entity{}, fmt.Errorf("%w %s в (%.1f, %.1f)", ErrPlace, entity, pos.X, pos.Y)
}

func dirName(d int) string {
	switch d {
	case North:
		return ""
	case East:
		return " на восток"
	case South:
		return " на юг"
	case West:
		return " на запад"
	}
	return fmt.Sprintf(" направление %d", d)
}

// Rotate поворачивает свою постройку клавишей R (по часовой, шаг 4) до направления dir.
func (h *Hands) Rotate(ctx context.Context, e bridge.Entity, dir int) error {
	h.Say("поворачиваю %s%s", e.Name, dirName(dir))
	if err := h.Hover(ctx, e); err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		st, err := h.B.GetEntity(e.Name, e.Position)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("act: %s исчез", e.Name)
		}
		if st.Direction == nil || *st.Direction == dir {
			return nil
		}
		if err := h.In.Tap("r"); err != nil {
			return err
		}
		if err := sleep(ctx, inputLatency); err != nil {
			return err
		}
	}
	return fmt.Errorf("act: %s не повернулся в %d", e.Name, dir)
}

// held — сколько предмета у игрока: инвентарь + рука.
func held(p bridge.Player, item string) int {
	n := p.Inventory[item]
	if p.Cursor != nil && p.Cursor.Name == item {
		n += p.Cursor.Count
	}
	return n
}

var zInterval = 120 * time.Millisecond

// Insert кладёт n предметов в сущность: предмет в руку → навести → Z n раз
// (каждое нажатие — ровно один предмет). Проверка — у игрока стало на n меньше.
func (h *Hands) Insert(ctx context.Context, e bridge.Entity, item string, n int) error {
	p0, err := h.B.Player()
	if err != nil {
		return err
	}
	if held(p0, item) < n {
		return fmt.Errorf("act: %s есть %d, нужно %d", item, held(p0, item), n)
	}
	h.Say("кладу %s ×%d в %s", item, n, e.Name)
	if err := h.WalkNear(ctx, e, 2); err != nil {
		return err
	}
	if err := h.Pick(ctx, item); err != nil {
		return err
	}
	if err := h.Hover(ctx, e); err != nil {
		h.ClearCursor(ctx)
		return err
	}
	// одно нажатие Z (удержание Input.Hold) — ровно один предмет
	for i := 0; i < n; i++ {
		if err := h.In.Tap("z"); err != nil {
			return err
		}
		if err := sleep(ctx, zInterval); err != nil {
			return err
		}
	}
	_, werr := h.waitFor(ctx, 2*time.Second, func(p bridge.Player) bool { return held(p0, item)-held(p, item) >= n })
	p1, err := h.B.Player()
	if err != nil {
		return err
	}
	if err := h.ClearCursor(ctx); err != nil {
		return err
	}
	if got := held(p0, item) - held(p1, item); werr != nil || got != n {
		return fmt.Errorf("act: в %s положено %s ×%d из %d (полон?)", e.Name, item, got, n)
	}
	return nil
}

// Take забирает из сущности то, что она выдаёт (у печи — результат): Ctrl+ЛКМ с пустой
// рукой. Возвращает, что прибавилось у игрока.
func (h *Hands) Take(ctx context.Context, e bridge.Entity) (map[string]int, error) {
	h.Say("забираю из %s", e.Name)
	if err := h.WalkNear(ctx, e, 2); err != nil {
		return nil, err
	}
	if err := h.Clean(ctx); err != nil {
		return nil, err
	}
	p0, err := h.B.Player()
	if err != nil {
		return nil, err
	}
	if err := h.Hover(ctx, e); err != nil {
		return nil, err
	}
	if err := h.In.KeyDown("ctrl"); err != nil {
		return nil, err
	}
	err = h.In.Click(input.Left)
	h.In.KeyUp("ctrl")
	if err != nil {
		return nil, err
	}
	if err := sleep(ctx, inputLatency); err != nil {
		return nil, err
	}
	p1, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool {
		for k, v := range p.Inventory {
			if v > p0.Inventory[k] {
				return true
			}
		}
		return false
	})
	got := map[string]int{}
	for k, v := range p1.Inventory {
		if d := v - p0.Inventory[k]; d > 0 {
			got[k] = d
		}
	}
	if errors.Is(err, errTimeout) {
		return got, nil // нечего забирать
	}
	return got, err
}

// Pickup подбирает свою постройку обратно в инвентарь (удержание ПКМ).
func (h *Hands) Pickup(ctx context.Context, e bridge.Entity) error {
	return h.PickupAll(ctx, []bridge.Entity{e})
}

// mineReachGame — с какого расстояния до рамки коллизии добываем, не подходя (игра — 2.7).
const mineReachGame = 2.4

// PickupAll подбирает свои постройки: ближайшую досягаемую — не сходя с места и не
// отпуская ПКМ между соседними; если досягаемых нет — встаёт туда, откуда достаёт
// больше всего оставшихся.
func (h *Hands) PickupAll(ctx context.Context, es []bridge.Entity) error {
	if len(es) == 0 {
		return nil
	}
	if len(es) == 1 {
		h.Say("подбираю %s", es[0].Name)
	} else {
		h.Say("подбираю %d построек", len(es))
	}
	if err := h.Clean(ctx); err != nil {
		return err
	}
	held := false
	release := func() {
		if held {
			h.In.ButtonUp(input.Right)
			held = false
		}
	}
	defer release()
	left := append([]bridge.Entity(nil), es...)
	reach := func(from bridge.Pos, e bridge.Entity) float64 {
		return dist(from, closestOnBox(from, *h.collisionBox(e)))
	}
	for len(left) > 0 {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		best, bestD := 0, math.Inf(1)
		for i, e := range left {
			if d := reach(p.Position, e); d < bestD {
				best, bestD = i, d
			}
		}
		e := left[best]
		left = append(left[:best], left[best+1:]...)
		if st, err := h.B.GetEntity(e.Name, e.Position); err != nil {
			return err
		} else if st == nil {
			continue // уже нет
		}
		t0 := time.Now()
		if bestD > mineReachGame {
			release()
			if err := h.WalkTo(ctx, h.bestStand(e, left, reach), 0.5); err != nil {
				if err := h.WalkToMine(ctx, e); err != nil {
					return err
				}
			}
		}
		t1 := time.Now()
		if err := h.mineHeld(ctx, e, nil, &held); err != nil {
			return err
		}
		h.Log("подбор: ходьба %.2f, добыча %.2f", t1.Sub(t0).Seconds(), time.Since(t1).Seconds())
	}
	return nil
}

// bestStand — точка у цели e (тайл в досягаемости добычи), откуда достаётся больше всего
// оставшихся others; при равенстве — ближе к цели. Только проходимые тайлы модели.
func (h *Hands) bestStand(e bridge.Entity, others []bridge.Entity, reach func(bridge.Pos, bridge.Entity) float64) bridge.Pos {
	c := world.TileOf(e.Position)
	best, bestN, bestD := e.Position, -1, math.Inf(1)
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			t := world.Tile{X: c.X + dx, Y: c.Y + dy}
			if h.World != nil && !h.World.Walkable(t) {
				continue
			}
			pos := t.Center()
			d := reach(pos, e)
			if d > mineReachGame-0.3 {
				continue
			}
			n := 0
			for _, o := range others {
				if reach(pos, o) <= mineReachGame-0.3 {
					n++
				}
			}
			if n > bestN || (n == bestN && d < bestD) {
				best, bestN, bestD = pos, n, d
			}
		}
	}
	return best
}

// Drop выкладывает один предмет на землю в точку pos: предмет в руку → навести на пустое
// место → Z. Подтверждение — на земле появился item-entity с этим предметом рядом с pos.
func (h *Hands) Drop(ctx context.Context, item string, pos bridge.Pos) (bridge.Entity, error) {
	h.Say("выкладываю %s на землю в (%.1f, %.1f)", item, pos.X, pos.Y)
	if err := h.guard(ctx); err != nil {
		return bridge.Entity{}, err
	}
	p, err := h.B.Player()
	if err != nil {
		return bridge.Entity{}, err
	}
	if dist(p.Position, pos) > buildReach {
		if err := h.WalkTo(ctx, pos, 3); err != nil {
			return bridge.Entity{}, err
		}
	}
	before, err := h.B.FindNearest(bridge.FindArgs{Type: "item-entity", Position: &pos, Radius: 1, Limit: 20})
	if err != nil {
		return bridge.Entity{}, err
	}
	if err := h.Pick(ctx, item); err != nil {
		return bridge.Entity{}, err
	}
	p, err = h.B.Player()
	if err != nil {
		return bridge.Entity{}, err
	}
	if err := h.In.Move(h.Camera(p).ToScreen(pos.X, pos.Y)); err != nil {
		return bridge.Entity{}, err
	}
	if err := sleep(ctx, 60*time.Millisecond); err != nil {
		return bridge.Entity{}, err
	}
	// под курсором постройка — Z положит в неё; руда и предметы на земле — не мешают
	if p, _ := h.B.Player(); p.Selected != nil && p.Selected.Type != "resource" && p.Selected.Type != "item-entity" {
		h.ClearCursor(ctx)
		return bridge.Entity{}, fmt.Errorf("act: в (%.1f, %.1f) под курсором %s — Z положил бы в него", pos.X, pos.Y, p.Selected.Name)
	}
	if err := h.In.Tap("z"); err != nil {
		return bridge.Entity{}, err
	}
	seen := map[bridge.Pos]bool{}
	for _, e := range before {
		seen[e.Position] = true
	}
	var dropped *bridge.Entity
	deadline := time.Now().Add(time.Second)
	for dropped == nil && time.Now().Before(deadline) {
		if err := sleep(ctx, pollEvery); err != nil {
			return bridge.Entity{}, err
		}
		after, err := h.B.FindNearest(bridge.FindArgs{Type: "item-entity", Position: &pos, Radius: 1, Limit: 20})
		if err != nil {
			return bridge.Entity{}, err
		}
		for i := range after {
			if after[i].Item == item && !seen[after[i].Position] {
				dropped = &after[i]
				break
			}
		}
	}
	if err := h.ClearCursor(ctx); err != nil {
		return bridge.Entity{}, err
	}
	if dropped == nil {
		h.debugShot("drop-" + item)
		return bridge.Entity{}, fmt.Errorf("act: %s не появился на земле у (%.1f, %.1f)", item, pos.X, pos.Y)
	}
	return *dropped, nil
}

// PickupItem поднимает предмет с земли (item-entity): подойти, навести, удержать ПКМ.
func (h *Hands) PickupItem(ctx context.Context, e bridge.Entity) error {
	h.Say("поднимаю с земли %s", e.Item)
	if err := h.WalkToMine(ctx, e); err != nil {
		return err
	}
	if err := h.Clean(ctx); err != nil {
		return err
	}
	return h.Mine(ctx, e, 5*time.Second)
}
