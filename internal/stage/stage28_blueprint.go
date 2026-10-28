package stage

import (
	"context"
	"fmt"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 28 (stages.md): обслуживание после зелёной науки — как стадия 22, и главное — привести
// построенное к чертежу: правила плана, которые только дописывают (делители между лентами
// одного предмета), — к сохранённому плану; сверка по всему чертежу (план и раскладки стадий:
// столбы, буферы, хранение); лишнее — убрать в хранение.

func init() {
	register(Stage{N: 28, Name: "обслуживание: привести к чертежу", Run: runBlueprint, Check: checkBlueprint})
}

// maxExtras — лишнего больше — не убирать, а упасть: чертёж, видимо, неполный (иначе разберём
// базу).
const maxExtras = 60

// firstTechs28 — в самом начале стадии 28 — в очередь исследований по порядку (stages.md,
// решение пользователя): сборщик 2, быстрые ленты, следующий научный пакет.
var firstTechs28 = []string{"automation-2", "logistics-2", "chemical-science-pack"}

func runBlueprint(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, firstTechs28...); err != nil {
		return err
	}
	if err := upgradePlan(h); err != nil {
		return err
	}
	if err := refreshLayouts(h, 27); err != nil {
		return err
	}
	return maintain(ctx, h, func() ([]layout.Placement, error) { return blueprint(h, planAll) }, true)
}

func checkBlueprint(h *act.Hands) error { return checkDrawing(h, planAll) }

// planAll — весь план стройки (без будущей добычи — она на стадии 30).
func planAll(h *act.Hands) ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	return withoutLater(h, plan, plan.Layout), nil
}

// checkDrawing — проверка обслуживания по чертежу (план — base): лишнего нет, остальное — checkKept.
func checkDrawing(h *act.Hands, base func(*act.Hands) ([]layout.Placement, error)) error {
	want, err := blueprint(h, base)
	if err != nil {
		return err
	}
	extra, err := extras(h, want)
	if err != nil {
		return err
	}
	if len(extra) > 0 {
		return fmt.Errorf("лишнее (нет в чертеже): %d, первое — %s в (%.1f, %.1f)", len(extra), extra[0].Name, extra[0].Pos.X, extra[0].Pos.Y)
	}
	return checkKept(h, want)
}

// upgradePlan — правила плана, которые только дописывают построенное (cluster.md §5: делители
// между лентами одного предмета), — к сохранённому плану.
func upgradePlan(h *act.Hands) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	l := layout.New(h.Protos, nil)
	for _, pl := range plan.Layout {
		l.Add(pl)
	}
	if n := cluster.BalancePlan(l, busNames(plan)); n > 0 {
		h.Say("чертёж: +%d делителей между лентами одного предмета на шине", n)
		plan.Layout = l.List
		plan.Placed = len(l.List)
		return writeState(planNowFile(), plan)
	}
	return nil
}

// blueprint — чертёж: из плана — base (стадия 22 — что уже должно стоять, 28 — весь) и раскладки
// стадий (state-*.json — столбы, буферы, хранение); где раскладка стадии расходится с планом
// (план обновлён — делитель на месте ленты), прав план. Временное (сундук и инсертер станции
// стадии 11, их убирает стадия 16) — не чертёж.
func blueprint(h *act.Hands, base func(*act.Hands) ([]layout.Placement, error)) ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	res, err := base(h)
	if err != nil {
		return nil, err
	}
	taken := map[world.Tile]layout.Placement{}
	for _, pl := range plan.Layout {
		for _, t := range layout.Tiles(h.Protos, pl) { // все тайлы (делитель — два)
			taken[t] = pl
		}
	}
	have := map[layout.Placement]bool{}
	for _, pl := range res {
		have[pl] = true
	}
	names, lists := stateLayouts()
	for _, base := range names {
		if base == planFile || base == greenBaseFile || base == blueBaseFile {
			continue
		}
		for _, pl := range lists[base] {
			if pl.Name == "" || have[pl] || pl.Tag == powerTag && (pl.Name == chest || strings.HasSuffix(pl.Name, "inserter")) {
				continue
			}
			clash := false // в плане на этом месте другое — прав план
			for _, t := range layout.Tiles(h.Protos, pl) {
				if q, ok := taken[t]; ok && q != pl {
					clash = true
				}
			}
			if clash {
				continue
			}
			have[pl] = true
			res = append(res, pl)
		}
	}
	return res, nil
}

// extras — свои постройки, которых нет в чертеже (по имени и месту).
func extras(h *act.Hands, want []layout.Placement) ([]world.Object, error) {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return nil, err
	}
	type key struct {
		name string
		x, y int
	}
	k := func(name string, p bridge.Pos) key { return key{name, int(p.X * 2), int(p.Y * 2)} }
	in := map[key]bool{}
	for _, pl := range want {
		in[k(pl.Name, pl.Pos)] = true
	}
	// что есть в плане на потом (весь план с синей) — не лишнее: поставленное раньше срока не
	// разбирать (лента камня bus-1 — старым кодом на стадии 22, а по чертежу — стадия 30)
	if full, err := LoadBluePlan(); err == nil {
		for _, pl := range full.Layout {
			in[k(pl.Name, pl.Pos)] = true
		}
	}
	var res []world.Object
	for _, o := range h.World.Objects() {
		if !o.Own || o.Type == "character" || h.Protos.ItemFor(o.Name) == "" {
			continue
		}
		if !in[k(o.Name, o.Pos)] {
			res = append(res, o)
		}
	}
	return res, nil
}

// removeExtras — лишнее (своё, чего нет в чертеже) — поднять и в хранение (stages.md, стадии 22
// и 28: построенное — в точном соответствии с чертежом). onSpots — только то, что стоит на месте
// чертежа (до достройки); остальное — после (старые столбы — когда новые уже стоят).
func removeExtras(ctx context.Context, h *act.Hands, s *worker, want []layout.Placement, onSpots bool) error {
	all, err := extras(h, want)
	if err != nil {
		return err
	}
	// порог — без столбов сети: её пересчитали, старые столбы все лишние
	var notPoles []world.Object
	for _, o := range all {
		if o.Type != "electric-pole" {
			notPoles = append(notPoles, o)
		}
	}
	if len(notPoles) > maxExtras {
		o := notPoles[0]
		return fmt.Errorf("лишнего (нет в чертеже, не столбов) %d — больше %d: чертёж неполный? первое — %s в (%.1f, %.1f)", len(notPoles), maxExtras, o.Name, o.Pos.X, o.Pos.Y)
	}
	extra := all
	if onSpots {
		spots := map[world.Tile]bool{}
		for _, pl := range want {
			for _, t := range layout.Tiles(h.Protos, pl) {
				spots[t] = true
			}
		}
		extra = nil
		for _, o := range all {
			for _, t := range layout.Tiles(h.Protos, layout.Placement{Name: o.Name, Pos: o.Pos, Dir: o.Direction}) {
				if spots[t] {
					extra = append(extra, o)
					break
				}
			}
		}
	}
	if len(extra) == 0 {
		return nil
	}
	if onSpots {
		h.Say("чертёж: лишнего на месте чертежа %d — убираю до достройки", len(extra))
	} else {
		h.Say("чертёж: лишнего %d — убираю", len(extra))
	}
	var es []bridge.Entity
	for _, o := range extra {
		h.Log("лишнее: %s в (%.1f, %.1f)", o.Name, o.Pos.X, o.Pos.Y)
		if e, err := h.B.EntityAt(o.Name, o.Pos); err == nil && e != nil {
			es = append(es, *e)
		}
	}
	if err := h.PickupAll(ctx, es); err != nil {
		return err
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	return s.store(ctx, p.InventorySize-10)
}
