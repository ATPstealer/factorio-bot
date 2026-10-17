package act

import (
	"context"
	"errors"
	"fmt"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
)

// ErrNotMining — ПКМ зажата, а персонаж не добывает: инвентарь полон или цель вне досягаемости.
var ErrNotMining = errors.New("act: персонаж не добывает")

// ErrNoTargets — подходящих целей в радиусе не осталось.
var ErrNoTargets = errors.New("act: нет целей для добычи")

const notMiningGrace = 1500 * time.Millisecond

// MineUntil наводит курсор на e и держит ПКМ, пока сущность не исчезнет или done не вернёт true.
func (h *Hands) MineUntil(ctx context.Context, e bridge.Entity, done func(bridge.Player) bool) error {
	if err := h.Hover(ctx, e); err != nil {
		return err
	}
	if err := h.In.ButtonDown(input.Right); err != nil {
		return err
	}
	pressed := true
	defer func() {
		if pressed {
			h.In.ButtonUp(input.Right)
		}
	}()
	var idleSince time.Time
	repressed := false
	for {
		if err := sleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		got, err := h.B.EntityAt(e.Name, e.Position)
		if err != nil {
			return err
		}
		if got == nil {
			return nil
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		if done != nil && done(p) {
			return nil
		}
		if p.Character != nil && p.Character.Mining {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) < notMiningGrace {
			continue
		}
		if same(p.Selected, e) {
			// нажатие ПКМ могло потеряться — один раз отпустить и нажать заново
			if !repressed {
				repressed = true
				h.Log("не копает — повторно нажимаю ПКМ")
				h.In.ButtonUp(input.Right)
				if err := sleep(ctx, inputLatency); err != nil {
					return err
				}
				if err := h.In.ButtonDown(input.Right); err != nil {
					return err
				}
				idleSince = time.Time{}
				continue
			}
			h.debugShot("not-mining-" + e.Name)
			return fmt.Errorf("%w: %s (%.1f, %.1f)", ErrNotMining, e.Name, e.Position.X, e.Position.Y)
		}
		// курсор съехал с цели — отпустить ПКМ (иначе добудем то, что под курсором) и навести заново
		h.In.ButtonUp(input.Right)
		pressed = false
		if err := h.Hover(ctx, e); err != nil {
			return err
		}
		if err := h.In.ButtonDown(input.Right); err != nil {
			return err
		}
		pressed = true
		idleSince = time.Time{}
	}
}

// Mine добывает сущность целиком (дерево, камень, обломок) за отведённое время.
func (h *Hands) Mine(ctx context.Context, e bridge.Entity, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := h.MineUntil(ctx, e, nil)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("act: не добыл %s за %v", e.Name, timeout)
	}
	return err
}

// GatherResult — итог Gather: сколько добыто (сущностей или единиц руды) и что прибавилось в инвентаре.
type GatherResult struct {
	Mined int
	Items map[string]int
}

// mineStop — на каком расстоянии от края рамки останавливаться: руду персонаж добывает
// только вблизи (resource_reach ≈ 2.7), остальное — с reach 10, но подходим ближе,
// чтобы цель была на экране.
func mineStop(e bridge.Entity) float64 {
	if e.Type == "resource" {
		return 1.0
	}
	return 1.5
}

// Gather добывает count раз цель target (см. bridge.FindMinable): для руды — count единиц,
// для остального — count сущностей. Подходит к ближайшей цели, недоступные курсору пропускает.
func (h *Hands) Gather(ctx context.Context, target string, count int, radius float64) (GatherResult, error) {
	res := GatherResult{Items: map[string]int{}}
	start, err := h.B.Player()
	if err != nil {
		return res, err
	}
	defer func() {
		if end, err := h.B.Player(); err == nil {
			for k, v := range end.Inventory {
				if d := v - start.Inventory[k]; d != 0 {
					res.Items[k] = d
				}
			}
		}
	}()

	skip := map[bridge.Pos]bool{}
	const maxUnreachable = 5
	unreachable := 0
	for res.Mined < count {
		cands, err := h.B.FindMinable(target, radius, 20)
		if err != nil {
			return res, err
		}
		var e *bridge.Entity
		for i := range cands {
			if !skip[cands[i].Position] {
				e = &cands[i]
				break
			}
		}
		if e == nil {
			return res, fmt.Errorf("%w: %q в радиусе %.0f", ErrNoTargets, target, radius)
		}
		h.Log("цель %s (%.1f, %.1f), %.1f тайлов", e.Name, e.Position.X, e.Position.Y, e.Distance)
		if err := h.WalkNear(ctx, *e, mineStop(*e)); err != nil {
			// цель за препятствием — пока (до поиска пути) берём следующую
			if (errors.Is(err, ErrStuck) || errors.Is(err, ErrNotReached)) && unreachable < maxUnreachable {
				unreachable++
				h.Log("%v — пропускаю", err)
				skip[e.Position] = true
				continue
			}
			return res, err
		}

		if e.Type != "resource" {
			err = h.Mine(ctx, *e, 30*time.Second)
			if err == nil {
				res.Mined++
			}
		} else {
			// руда: каждая добыча даёт единицу продукта, сущность остаётся до истощения
			ore := e.Name
			if len(e.Products) > 0 {
				ore = e.Products[0].Name
			}
			p0, perr := h.B.Player()
			if perr != nil {
				return res, perr
			}
			need := count - res.Mined
			got := 0
			err = h.MineUntil(ctx, *e, func(p bridge.Player) bool {
				got = p.Inventory[ore] - p0.Inventory[ore]
				return got >= need
			})
			if p, perr := h.B.Player(); perr == nil {
				got = p.Inventory[ore] - p0.Inventory[ore]
			}
			res.Mined += got
		}
		if errors.Is(err, ErrHover) {
			h.Log("%v — пропускаю", err)
			skip[e.Position] = true
			continue
		}
		if err != nil {
			return res, err
		}
	}
	return res, nil
}
