package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Инвентарь — постоянно в порядке (stages.md, «Общее для всех стадий»): нужное текущей стадии —
// её план и предметы на его крафт; свободно меньше четверти — ненужное в хранение, пока не станет
// свободна половина (store), пластины — никогда.

// curStage — идущая стадия (stage.Do): по её плану — что нужно.
var curStage int

// freeBefore — свободных ячеек перед долгим делом (добыча, вырубка, сбор пластин).
const freeBefore = 10

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

// tidy — у хранения (берёт оттуда): то же правило, что всегда (store: свободно меньше
// четверти — ненужное в хранение до половины).
func (s *worker) tidy(ctx context.Context) error {
	p, err := s.h.B.Player()
	if err != nil {
		return err
	}
	return s.store(ctx, p.InventorySize)
}
