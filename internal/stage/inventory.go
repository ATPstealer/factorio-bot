package stage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// Инвентарь — постоянно в порядке (stages.md, «Общее для всех стадий»): нужное текущей стадии —
// её раскладка и предметы на её крафт; свободно меньше четверти — ненужное в хранение, пока не
// станет свободна половина (store); пластины — только когда полон, а выложить больше нечего
// (сверх plateKeep). Набрать нужное — worker.go.

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
			// только то, что ещё не стоит: поставленное — не «нужное» (решение пользователя 26.10)
			if left, err := notBuilt(h, list); err == nil {
				list = left
			}
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
// четверти — ненужное в хранение до половины); take — что сейчас берётся, его не класть
// (уголь на бойлеры: клал обратно и брал снова 40 минут — стадия 29 на 427965634).
func (s *worker) tidy(ctx context.Context, take string) error {
	p, err := s.h.B.Player()
	if err != nil {
		return err
	}
	return s.store(ctx, p.InventorySize, take)
}

// errNoStorage — инвентарь полон, а хранения ещё нет (до стадии 13).
var errNoStorage = errors.New("инвентарь полон, а хранения нет")

// fullSlots — свободных ячеек не больше стольких — инвентарь полон.
const fullSlots = 2

// neverStore — не выкладывать никогда: пластины всё равно используем (stages.md, инвентарь);
// кроме полного инвентаря, где выложить больше нечего, — сверх plateKeep (store).
var neverStore = map[string]bool{"iron-plate": true, "copper-plate": true, "steel-plate": true}

// plateKeep — пластин каждого вида оставить при себе, когда излишек всё же выкладывается.
const plateKeep = 1000

// storeRank — ходовость предмета для выкладки (stages.md: от самого неходового к ходовому):
// меньше — выкладывать раньше.
func storeRank(it string) int {
	switch it {
	case "coal":
		return 3
	case "iron-gear-wheel", "copper-cable", "electronic-circuit":
		return 2
	}
	return 0
}

// store — инвентарь в порядке (stages.md, решение пользователя): свободно 25% и больше — не
// трогать; меньше — к хранению и выложить ненужное, пока не станет свободно 50%: пластины —
// никогда, нужное стадии (need и план стадии) — нет (s.any — полон посреди дела: нужное тоже
// можно), от самого неходового к ходовому. free — сколько ячеек просит дело (не больше
// четверти — порог один на всех, решение пользователя; free — только для повторного вызова).
func (s *worker) store(ctx context.Context, free int, need ...string) error {
	p, err := s.h.B.Player()
	if err != nil {
		return err
	}
	size := p.InventorySize
	freeNow := func() int { return p.InventorySize - len(p.InventorySlots) }
	if freeNow() >= size/4 && freeNow() > fullSlots {
		return nil
	}
	keep := map[string]bool{}
	for _, it := range need {
		keep[it] = true
	}
	if !s.any {
		for it := range neededItems(s.h) {
			keep[it] = true
		}
	}
	var order []string
	for it := range p.Inventory {
		if !neverStore[it] && !keep[it] {
			order = append(order, it)
		}
	}
	// неходовое — раньше; при равной — что больше места занимает
	slots := func(it string) int {
		return (p.Inventory[it] + max(s.h.Protos.Items[it].StackSize, 1) - 1) / max(s.h.Protos.Items[it].StackSize, 1)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if ri, rj := storeRank(order[i]), storeRank(order[j]); ri != rj {
			return ri < rj
		}
		if si, sj := slots(order[i]), slots(order[j]); si != sj {
			return si > sj
		}
		return order[i] < order[j]
	})
	// выложить можно меньше, чем нужно до четверти (занято пластинами и нужным), — ходка ничего не
	// меняет: только когда полон
	freeable := 0
	for _, it := range order {
		freeable += slots(it)
	}
	if freeNow() > fullSlots && freeNow()+freeable < size/4 {
		return nil
	}
	// полон, а выложить почти нечего — и пластины сверх plateKeep (stages.md, инвентарь): иначе
	// крафт стоит, готовому некуда лечь (стадия 28 на 859088572: 6120 железных — 62 ячейки из 80)
	spare := map[string]bool{}
	if freeNow() <= fullSlots && freeNow()+freeable < size/4 {
		for _, it := range []string{"iron-plate", "copper-plate", "steel-plate"} {
			if p.Inventory[it] > 2*plateKeep && !slices.Contains(need, it) { // не то, что сейчас набираем
				order, spare[it] = append(order, it), true
			}
		}
	}
	if len(order) == 0 {
		return nil // выложить нечего: всё нужное или пластины
	}
	list, err := storageNear(s.h)
	if err != nil {
		return fmt.Errorf("%w: %v", errNoStorage, err) // плана ещё нет (до стадии 3)
	}
	var chests []bridge.Entity
	for _, pl := range list {
		if e, err := s.entity(pl); err == nil && e != nil {
			chests = append(chests, *e)
		}
	}
	if len(chests) == 0 {
		return errNoStorage
	}
	if freeNow() <= fullSlots {
		s.h.Say("инвентарь полон — кладу в хранение")
	} else {
		s.h.Say("свободно %d из %d (меньше четверти) — кладу в хранение ненужное", freeNow(), size)
	}
	k := 0
	for _, it := range order {
		if freeNow() >= size/2 || k >= len(chests) || spare[it] && freeNow() >= size/4 {
			break
		}
		last := -1 // куда легло (сундук полон — следующий)
		for p.Inventory[it] > 0 && k < len(chests) {
			n, err := s.h.InsertAll(ctx, chests[k], it)
			if err != nil {
				return err
			}
			if n == 0 { // сундук полон — следующий
				k++
			} else {
				last = k
			}
			if p, err = s.h.B.Player(); err != nil {
				return err
			}
		}
		// пластины — все в сундук и plateKeep обратно (положить «всё» — Ctrl, часть — нет)
		if spare[it] && last >= 0 {
			if err := s.h.TakeItem(ctx, chests[last], it, plateKeep); err != nil {
				return err
			}
			if p, err = s.h.B.Player(); err != nil {
				return err
			}
		}
	}
	s.h.Log("хранение: выложено, свободно %d из %d (сундуков пройдено %d из %d)", freeNow(), size, k, len(chests))
	// все сундуки полны — хранение сразу до 4×8, как на обслуживании (8 сундуков забивались к
	// стадии 15: разбор точек не помещался — 238178543), и ещё раз
	if k >= len(chests) && len(list) < storageChests && !s.growing {
		s.growing = true
		defer func() { s.growing = false }()
		s.h.Say("хранение полно — расширяю до %d сундуков", storageChests)
		if err := growStorage(ctx, s.h, s); err != nil {
			return err
		}
		return s.store(ctx, free, need...)
	}
	return nil
}
