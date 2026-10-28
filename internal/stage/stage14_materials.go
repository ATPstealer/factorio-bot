package stage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 14 (stages.md): материалы на кластеры добычи угля, железа, меди и плавки железа и
// меди по плану стадии 3 (с лентами между ними и лентой угля на станцию) — скрафтить руками;
// сверх расчёта — запас materialsExtra; не влезает в инвентарь — в кластер хранения. Только
// материалы: строят стадии 16 (добыча угля) и 17 (добыча и плавка железа и меди).

// materialsExtra — запас сверх расчёта: стройке 16 и 17 — до дохода с её же цепочек.
var materialsExtra = map[string]int{"iron-plate": 1500, "copper-plate": 1000, "coal": 1000}

// outBufferItems — материалы буфера выхода плавки (стадия 17: пластины копятся в контейнере):
// минимальный — один инсертер и один сундук (cluster.md §6а, решение пользователя 26.10). Только
// для расчёта материалов: ключ chest — деревянный сундук (до стадии 22); outBuffer берёт 1 пару
// (с 22-й chest — железный, и outBufferItems[chest] был 0: буфер кирпича на стадии 30 не вставал).
var outBufferItems = map[string]int{"inserter": 1, chest: 1}

func init() {
	register(Stage{N: 14, Name: "материалы на добычу и плавку", Run: runMaterials, Check: checkMaterials})
}

// materialsClusters — кластеры стадий 16–17: добыча угля, железа, меди; плавка железа и меди.
func materialsClusters(plan *ClusterPlan) []*cluster.Cluster {
	var res []*cluster.Cluster
	for _, c := range plan.Clusters {
		switch {
		case c.Kind == cluster.Mine && (c.Product == "coal" || c.Product == "iron-ore" || c.Product == "copper-ore"),
			c.Kind == cluster.Smelt && (c.Product == "iron-plate" || c.Product == "copper-plate"):
			res = append(res, c)
		}
	}
	return res
}

// materialsLayout — что построят стадии 16–17 (coalBase, smeltingBase) и столбы: по столбам логистики
// плана (цепочки по коридорам выйдут не длиннее).
func materialsLayout(h *act.Hands) ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	seen := map[layout.Placement]bool{}
	var res []layout.Placement
	for _, pl := range append(append(coalBase(h, plan), smeltingBase(h, plan)...), tagged(plan, cluster.LogisticsTag)...) {
		if !seen[pl] {
			seen[pl] = true
			res = append(res, pl)
		}
	}
	return res, nil
}

// materialsNeed — сколько чего скрафтить: по раскладке (что ещё не стоит) и буферы выхода плавки.
func materialsNeed(h *act.Hands) (map[string]int, error) {
	list, err := materialsLayout(h)
	if err != nil {
		return nil, err
	}
	need := map[string]int{}
	miss, err := notBuilt(h, list) // пачками: по одной — 1 мин 40 с в начале и в проверке
	if err != nil {
		return nil, err
	}
	for _, pl := range miss {
		need[pl.Name]++
	}
	plan, _ := LoadPlan()
	for _, c := range materialsClusters(plan) {
		if c.Kind == cluster.Smelt {
			for it, n := range outBufferItems {
				need[it] += n
			}
		}
	}
	return need, nil
}

// materialsStock — что есть: инвентарь и сундуки хранения.
func materialsStock(h *act.Hands) (map[string]int, error) {
	p, err := h.B.Player()
	if err != nil {
		return nil, err
	}
	res := map[string]int{}
	for it, n := range p.Inventory {
		res[it] += n
	}
	list, err := storageLayout()
	if err != nil {
		return nil, err
	}
	for _, pl := range list {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		for it, n := range st.Inventories["chest"] {
			res[it] += n
		}
	}
	return res, nil
}

func runMaterials(ctx context.Context, h *act.Hands) error {
	need, err := materialsNeed(h)
	if err != nil {
		return err
	}
	var names []string
	for it := range need {
		names = append(names, it)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, it := range names {
		fmt.Fprintf(&b, " %s ×%d", it, need[it])
	}
	h.Say("материалы на добычу и плавку:%s", b.String())
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites}
	// сначала то, что тратит другие нужные предметы (подземки, делители — ленты), иначе
	// их крафт съест уже готовое; на всякий случай — несколько проходов
	uses := func(a, b string) bool {
		for _, in := range h.Protos.Recipes[a].Ingredients {
			if in.Name == b {
				return true
			}
		}
		return false
	}
	sort.SliceStable(names, func(i, j int) bool { return uses(names[i], names[j]) && !uses(names[j], names[i]) })
	// passes — недостающее: крафт в очередь; noWait — не ждать готового (идёт, пока бот носит
	// пластины на следующее), в очереди — как уже есть
	passes := func() error {
		for pass := 0; pass < 3; pass++ {
			short := false
			for _, it := range names {
				stock, err := materialsStock(h)
				if err != nil {
					return err
				}
				if lack := need[it] - stock[it] - s.pending(it); lack > 0 {
					short = true
					if err := s.store(ctx, 8); err != nil {
						return err
					}
					if err := s.have(ctx, it, s.inv(it)+lack); err != nil {
						return fmt.Errorf("%s: %w", it, err)
					}
				}
			}
			if !short {
				return nil
			}
		}
		return nil
	}
	// сначала всё в очередь без ожидания (правила скорости 27.10: бот стоял 39–57 мин, пока шёл
	// крафт), потом — конец очереди и сверка с ожиданием
	s.noWait = true
	err = passes()
	s.noWait = false
	if err != nil {
		return err
	}
	if err := s.waitQueue(ctx); err != nil {
		return err
	}
	if err := passes(); err != nil {
		return err
	}
	for _, it := range []string{"wood", "stone", "copper-plate", "iron-plate", "coal"} {
		stock, err := materialsStock(h)
		if err != nil {
			return err
		}
		// сверх расчёта: сырьё — тоже из запаса (на крафт уже ушло своё)
		for lack := materialsExtra[it] - stock[it]; lack > 0; lack = materialsExtra[it] - stock[it] {
			if err := s.store(ctx, 8); err != nil {
				return err
			}
			step := min(lack, 400)
			if err := s.have(ctx, it, s.inv(it)+step); err != nil {
				return fmt.Errorf("%s: %w", it, err)
			}
			if stock, err = materialsStock(h); err != nil {
				return err
			}
		}
	}
	return nil
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

func checkMaterials(h *act.Hands) error {
	need, err := materialsNeed(h)
	if err != nil {
		return err
	}
	stock, err := materialsStock(h)
	if err != nil {
		return err
	}
	for it, n := range need {
		if stock[it] < n {
			return fmt.Errorf("%s: есть %d, нужно %d", it, stock[it], n)
		}
	}
	for it, n := range materialsExtra {
		if stock[it] < n {
			return fmt.Errorf("запас %s: есть %d, нужно %d", it, stock[it], n)
		}
	}
	return nil
}
