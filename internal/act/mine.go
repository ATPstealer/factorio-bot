package act

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
	"factorio-bot/internal/world"
)

// ErrNotMining — ПКМ зажата, а персонаж не добывает: инвентарь полон или цель вне досягаемости.
var ErrNotMining = errors.New("act: персонаж не добывает")

// ErrNoTargets — подходящих целей в радиусе не осталось.
var ErrNoTargets = errors.New("act: нет целей для добычи")

const notMiningGrace = 1500 * time.Millisecond

// ErrFull — добытое некуда класть: инвентарь полон.
var ErrFull = errors.New("act: инвентарь полон")

// MineUntil наводит курсор на e и держит ПКМ, пока сущность не исчезнет или done не вернёт true.
func (h *Hands) MineUntil(ctx context.Context, e bridge.Entity, done func(bridge.Player) bool) error {
	held := false
	defer func() {
		if held {
			h.In.ButtonUp(input.Right)
		}
	}()
	return h.mineHeld(ctx, e, done, &held)
}

// mineHeld — MineUntil, который не отпускает ПКМ в конце: *held — нажата ли ПКМ сейчас.
// Подряд идущие цели добываются без отпускания кнопки — курсор просто переводится на
// следующую (как протягивание мышью), без лишнего нажатия на каждую.
func (h *Hands) mineHeld(ctx context.Context, e bridge.Entity, done func(bridge.Player) bool, held *bool) error {
	if !*held {
		if err := h.guard(ctx); err != nil {
			return err
		}
		// цель дальше досягаемости (подход застрял в лесу — стадия 12 на 859088572: рубил сухой
		// ствол с 5 тайлов) — подойти ещё раз; не подошли — так и сказать, а не «не добывает»
		if b := h.collisionBox(e); b != nil {
			reach := func() float64 {
				p, err := h.B.Player()
				if err != nil {
					return 0
				}
				return dist(p.Position, closestOnBox(p.Position, *b))
			}
			if reach() > mineReachGame {
				if err := h.WalkToMine(ctx, e); err != nil && !errors.Is(err, ErrNotReached) && !errors.Is(err, ErrStuck) {
					return err
				}
				if d := reach(); d > mineReachGame {
					return fmt.Errorf("%w до %s (%.1f, %.1f): %.1f тайла", ErrNotReached, e.Name, e.Position.X, e.Position.Y, d)
				}
			}
		}
	}
	if err := h.Hover(ctx, e); err != nil {
		return err
	}
	if !*held {
		if err := h.In.ButtonDown(input.Right); err != nil {
			return err
		}
		*held = true
	}
	var idleSince, fullSince time.Time
	items := -1 // предметов в инвентаре: прибавляется — копаем (в неполную пачку), не «полон»
	repressed := false
	flips := 0
	for {
		if err := sleep(ctx, pollEvery); err != nil {
			return err
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		// под курсором уже не цель — немедленно отпустить ПКМ: иначе добудем чужое
		// (у построек время добычи ~0.2 с)
		if p.Selected != nil && !same(p.Selected, e) {
			h.In.ButtonUp(input.Right)
			*held = false
			got, err := h.B.EntityAt(e.Name, e.Position)
			if err != nil {
				return err
			}
			if got == nil {
				return nil // цель добыта, выделение перешло на соседа
			}
			// выделение раз за разом уходит на соседа (цель под его кроной) — пусть Mine
			// сначала срубит соседей
			if flips++; flips >= 3 {
				return fmt.Errorf("%w: %s под %s", ErrHover, e.Name, p.Selected.Name)
			}
			h.Say("под курсором %s вместо %s — отпускаю и навожу заново", p.Selected.Name, e.Name)
			if err := h.Hover(ctx, e); err != nil {
				return err
			}
			if err := h.In.ButtonDown(input.Right); err != nil {
				return err
			}
			*held = true
			idleSince = time.Time{}
			continue
		}
		got, err := h.B.EntityAt(e.Name, e.Position)
		if err != nil {
			return err
		}
		if got == nil {
			return nil
		}
		if done != nil && done(p) {
			return nil
		}
		if p.Character != nil && p.Character.Mining {
			idleSince = time.Time{}
			// инвентарь полон — сундук с содержимым/постройку не поднять, копает вечно; все
			// ячейки заняты, но руда идёт в неполную пачку — не полон (стадия 14: уголь к
			// 1028292478 «не поднять», а он поднимался)
			n := 0
			for _, k := range p.Inventory {
				n += k
			}
			grew := items >= 0 && n > items
			items = n
			if p.InventorySize == 0 || len(p.InventorySlots) < p.InventorySize || grew {
				fullSince = time.Time{}
			} else if fullSince.IsZero() {
				fullSince = time.Now()
			} else if time.Since(fullSince) > 8*time.Second {
				return fmt.Errorf("%w: %s не поднять", ErrFull, e.Name)
			}
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
		// выделения нет (курсор съехал) — отпустить ПКМ и навести заново
		h.In.ButtonUp(input.Right)
		*held = false
		if err := h.Hover(ctx, e); err != nil {
			return err
		}
		if err := h.In.ButtonDown(input.Right); err != nil {
			return err
		}
		*held = true
		idleSince = time.Time{}
	}
}

// Mine добывает сущность целиком (дерево, камень, обломок) за отведённое время. Курсор не
// навёлся (в густом лесу ствол целиком под кроной соседнего дерева) — сначала срубить
// соседние деревья поблизости, ближние первыми, и ещё раз.
func (h *Hands) Mine(ctx context.Context, e bridge.Entity, timeout time.Duration) error {
	err := h.mineOnce(ctx, e, timeout)
	if !errors.Is(err, ErrHover) || h.World == nil {
		return err
	}
	near := h.World.ObjectsNear(e.Position, 3)
	sort.Slice(near, func(i, j int) bool { return dist(near[i].Pos, e.Position) < dist(near[j].Pos, e.Position) })
	for _, o := range near {
		if o.Type != "tree" || dist(o.Pos, e.Position) < 0.01 {
			continue
		}
		t, err := h.B.EntityAt(o.Name, o.Pos)
		if err != nil || t == nil {
			continue
		}
		h.Say("%s закрыт кроной — сначала рублю %s", e.Name, o.Name)
		if err := h.WalkToMine(ctx, *t); err != nil && !errors.Is(err, ErrNotReached) {
			return err
		}
		if err := h.mineOnce(ctx, *t, timeout); err != nil && !errors.Is(err, ErrHover) {
			return err
		}
		if _, err := h.World.Sync(h.B, false); err != nil {
			return err
		}
		if err = h.mineOnce(ctx, e, timeout); !errors.Is(err, ErrHover) {
			return err
		}
	}
	return err
}

// mineOnce — добыть e целиком за отведённое время, одной попыткой наведения. Инвентарь полон —
// освободить место (OnFull: стадии — в хранение; без таймаута добычи — до хранения идти) и
// ещё раз со своим временем.
func (h *Hands) mineOnce(ctx context.Context, e bridge.Entity, timeout time.Duration) error {
	err := h.mineTimed(ctx, e, timeout)
	if errors.Is(err, ErrFull) && h.OnFull != nil {
		h.Say("инвентарь полон — освобождаю место")
		if ferr := h.OnFull(ctx); ferr != nil {
			return fmt.Errorf("%w (освободить место: %v)", err, ferr)
		}
		err = h.mineTimed(ctx, e, timeout)
	}
	return err
}

func (h *Hands) mineTimed(ctx context.Context, e bridge.Entity, timeout time.Duration) error {
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
	freed := 0 // раз освобождали место (инвентарь полон)
	for res.Mined < count {
		cands, err := h.B.FindMinable(target, radius, 20)
		if err != nil {
			return res, err
		}
		var e *bridge.Entity
		for i := range cands {
			c := &cands[i]
			if skip[c.Position] {
				continue
			}
			// руда под постройкой или объектом: курсор выделит их, а не руду
			if c.Type == "resource" && h.World != nil && h.World.Blocked(world.TileOf(c.Position)) {
				continue
			}
			e = c
			break
		}
		if e == nil {
			return res, fmt.Errorf("%w: %q в радиусе %.0f", ErrNoTargets, target, radius)
		}
		h.Say("добываю %s (%.0f, %.0f), %.0f тайлов — %d из %d", e.Name, e.Position.X, e.Position.Y, e.Distance, res.Mined+1, count)
		if err := h.reachMine(ctx, *e); err != nil {
			// цель за препятствием — пока (до поиска пути) берём следующую
			if (errors.Is(err, ErrStuck) || errors.Is(err, ErrNotReached)) && unreachable < maxUnreachable {
				unreachable++
				h.Say("не подойти к %s — беру другую цель", e.Name)
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
			h.Say("%s закрыт другими объектами — беру другую цель", e.Name)
			skip[e.Position] = true
			continue
		}
		// руда при полном инвентаре — освободить место и дальше (как mineOnce; стадия 14 на
		// 1028292478 падала: «инвентарь полон: coal не поднять»)
		if errors.Is(err, ErrFull) && h.OnFull != nil && freed < 2 {
			freed++
			h.Say("инвентарь полон — освобождаю место")
			if ferr := h.OnFull(ctx); ferr != nil {
				return res, fmt.Errorf("%w (освободить место: %v)", err, ferr)
			}
			continue
		}
		if err != nil {
			return res, err
		}
	}
	return res, nil
}
