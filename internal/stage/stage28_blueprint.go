package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	return maintain(ctx, h, func() ([]layout.Placement, error) { return blueprint(h) }, true)
}

func checkBlueprint(h *act.Hands) error {
	want, err := blueprint(h)
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

// blueprint — чертёж: план и раскладки стадий (state-*.json — столбы, буферы, хранение); где
// раскладка стадии расходится с планом (план обновлён — делитель на месте ленты), прав план.
// Временное (сундук и инсертер станции стадии 11, их убирает стадия 16) — не чертёж.
func blueprint(h *act.Hands) ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	res := withoutLater(plan, plan.Layout) // будущая добыча — на стадии 30 (её файл — ниже, со state-*)
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
	files, _ := filepath.Glob(runPath("state-*.json"))
	for _, f := range files {
		base := filepath.Base(f)
		if base == planFile || base == greenBaseFile || base == blueBaseFile {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var list []layout.Placement
		if json.Unmarshal(data, &list) != nil {
			continue // не раскладка (зоны, точки стадии 10…)
		}
		for _, pl := range list {
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

// removeExtras — лишнее (своё, чего нет в чертежа) — поднять и в хранение (stages.md, стадия 28:
// построенное — в точном соответствии с планом).
func removeExtras(ctx context.Context, h *act.Hands, s *worker, want []layout.Placement) error {
	extra, err := extras(h, want)
	if err != nil {
		return err
	}
	if len(extra) == 0 {
		return nil
	}
	if len(extra) > maxExtras {
		return fmt.Errorf("лишнего (нет в чертеже) %d — больше %d: чертёж неполный? первое — %s в (%.1f, %.1f)", len(extra), maxExtras, extra[0].Name, extra[0].Pos.X, extra[0].Pos.Y)
	}
	h.Say("чертёж: лишнего %d — убираю", len(extra))
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
