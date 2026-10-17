package act

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
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

// standNear — подойти к месту постройки: на расстояние досягаемости, но не на саму
// площадку (персонаж мешает поставить).
func (h *Hands) standNear(ctx context.Context, fp bridge.Box) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	c := fp.Center()
	if !inBox(p.Position, fp, 0.8) && dist(p.Position, c) < 6 {
		return nil
	}
	// встать к югу от площадки (камера центрирована на игроке — площадка будет на экране)
	spot := bridge.Pos{X: c.X, Y: fp.RightBottom.Y + 1.5}
	return h.WalkTo(ctx, spot, 0.5)
}

// Place ставит постройку из предмета item центром в pos и поворачивает в dir:
// предмет в руку (хоткей) → навести → ЛКМ → R по постройке до нужного направления.
// Подтверждение — событие built_entity в этой точке.
func (h *Hands) Place(ctx context.Context, item string, pos bridge.Pos, dir int) (bridge.Entity, error) {
	it, ok := h.Protos.Items[item]
	if !ok || it.PlaceResult == "" {
		return bridge.Entity{}, fmt.Errorf("act: %s не ставится", item)
	}
	if err := h.guard(ctx); err != nil {
		return bridge.Entity{}, err
	}
	fp := h.footprint(it.PlaceResult, pos)
	if err := h.standNear(ctx, fp); err != nil {
		return bridge.Entity{}, err
	}
	if err := h.Pick(ctx, item); err != nil {
		return bridge.Entity{}, err
	}
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		return bridge.Entity{}, err
	}
	p, err := h.B.Player()
	if err != nil {
		return bridge.Entity{}, err
	}
	cam := h.Camera(p)
	if err := h.In.Move(cam.ToScreen(pos.X, pos.Y)); err != nil {
		return bridge.Entity{}, err
	}
	if err := sleep(ctx, 60*time.Millisecond); err != nil {
		return bridge.Entity{}, err
	}
	if err := h.In.Click(input.Left); err != nil {
		return bridge.Entity{}, err
	}
	var built *bridge.Entity
	deadline := time.Now().Add(time.Second)
	for built == nil && time.Now().Before(deadline) {
		if err := sleep(ctx, pollEvery); err != nil {
			return bridge.Entity{}, err
		}
		evs, _, err := h.B.PollEvents(seq0)
		if err != nil {
			return bridge.Entity{}, err
		}
		for _, e := range evs {
			if e.Type == "built_entity" && e.Entity != nil && e.Entity.Name == it.PlaceResult &&
				math.Abs(e.Entity.Position.X-pos.X) < 0.01 && math.Abs(e.Entity.Position.Y-pos.Y) < 0.01 {
				built = e.Entity
			}
		}
	}
	if built == nil {
		h.debugShot("place-" + it.PlaceResult)
	}
	if err := h.ClearCursor(ctx); err != nil {
		return bridge.Entity{}, err
	}
	if built == nil {
		return bridge.Entity{}, fmt.Errorf("%w %s в (%.1f, %.1f)", ErrPlace, it.PlaceResult, pos.X, pos.Y)
	}
	if built.Direction != nil && *built.Direction != dir {
		if err := h.Rotate(ctx, *built, dir); err != nil {
			return *built, err
		}
		d := dir
		built.Direction = &d
	}
	return *built, nil
}

// Rotate поворачивает свою постройку клавишей R (по часовой, шаг 4) до направления dir.
func (h *Hands) Rotate(ctx context.Context, e bridge.Entity, dir int) error {
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
	if err := h.WalkNear(ctx, e, 2); err != nil {
		return err
	}
	if err := h.Clean(ctx); err != nil {
		return err
	}
	return h.Mine(ctx, e, 10*time.Second)
}
