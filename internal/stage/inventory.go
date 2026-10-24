package stage

import (
	"context"
	"errors"

	"factorio-bot/internal/act"
)

// Инвентарь — постоянно в порядке (stages.md, «Общее для всех стадий»): нужное текущей стадии —
// её план и предметы на его крафт; запас ходового — по пачке, если план не тяжёлый; остальное —
// в хранение при каждом визите к нему; после визита свободна хотя бы четверть инвентаря.

// curStage — идущая стадия (stage.Do): по её плану — что нужно.
var curStage int

// freeBefore — свободных ячеек перед долгим делом (добыча, вырубка, сбор пластин).
const freeBefore = 10

// reserveItems — самое ходовое: есть — по пачке не выкладывать (быстрее, чем ходить к
// хранению); специально не брать.
var reserveItems = []string{"iron-plate", "copper-plate", "iron-gear-wheel", "electronic-circuit", "copper-cable", "coal"}

// needCache — нужное текущей стадии (план не меняется по ходу стадии).
var needCache struct {
	stage int
	items map[string]bool
	slots int // сколько ячеек займут постройки плана
}

// neededItems — что нужно текущей стадии: постройки её плана и всё на их ручной крафт (по дереву
// рецептов); без плана стройки (стадия 14 — материалы) — ничего: готовые постройки — в хранение.
func neededItems(h *act.Hands) map[string]bool {
	if needCache.items != nil && needCache.stage == curStage {
		return needCache.items
	}
	res := map[string]bool{}
	var add func(item string, depth int)
	add = func(item string, depth int) {
		if item == "" || res[item] && depth > 0 || depth > 6 {
			return
		}
		res[item] = true
		if r, ok := h.Protos.Recipes[item]; ok {
			for _, in := range r.Ingredients {
				add(in.Name, depth+1)
			}
		}
	}
	count := map[string]int{}
	if curStage > 0 {
		if list, err := planOf(h, curStage); err == nil {
			for _, pl := range list {
				it := h.Protos.ItemFor(pl.Name)
				add(it, 0)
				count[it]++
			}
		}
	}
	slots := 0
	for it, n := range count {
		st := h.Protos.Items[it].StackSize
		if st <= 0 {
			st = 50
		}
		slots += (n + st - 1) / st
	}
	needCache.stage, needCache.items, needCache.slots = curStage, res, slots
	return res
}

// keepList — что store не уносит: нужное стадии, запас ходового (если план не тяжёлый —
// постройки плана не больше половины инвентаря) и extra.
func keepList(h *act.Hands, extra ...string) []string {
	var res []string
	for it := range neededItems(h) {
		res = append(res, it)
	}
	if p, err := h.B.Player(); err == nil && needCache.slots*2 <= p.InventorySize {
		res = append(res, reserveItems...)
	}
	return append(res, extra...)
}

// tidy — у хранения: выложить ненужное и запас сверх пачки (stages.md: «бот у хранения —
// заодно выкладывает ненужное и лишнее сверх запаса»).
func (s *worker) tidy(ctx context.Context) error {
	p, err := s.h.B.Player()
	if err != nil {
		return err
	}
	if err := s.store(ctx, p.InventorySize); err != nil {
		return err
	}
	need := neededItems(s.h)
	for _, it := range reserveItems {
		if need[it] {
			continue // нужно стадии — при себе всё
		}
		st := s.h.Protos.Items[it].StackSize
		if st <= 0 {
			st = 50
		}
		if extra := s.inv(it) - st; extra > 0 {
			if err := s.putStore(ctx, it, extra); err != nil && !errors.Is(err, errNoStorage) {
				return err
			}
		}
	}
	return nil
}

// putStore — n предмета item — в сундук хранения, где есть место.
func (s *worker) putStore(ctx context.Context, item string, n int) error {
	list, err := storageLayout()
	if err != nil {
		return errNoStorage
	}
	for _, pl := range list {
		st, err := s.h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil || st.SlotsSize > 0 && len(st.Slots) >= st.SlotsSize {
			continue // полон
		}
		return s.h.Insert(ctx, st.Entity, item, n)
	}
	return errNoStorage
}
