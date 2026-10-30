package stage

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/act"
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
// плана (цепочки по коридорам выйдут не длиннее). В порядке использования: сначала стадия 16,
// потом 17, потом столбы (очередь крафта — в этом порядке, stages.md).
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

// materialsNeed — сколько чего скрафтить: по раскладке (что ещё не стоит) и буферы выхода плавки;
// order — предметы в порядке первого использования (буферы — в конце).
func materialsNeed(h *act.Hands) (need map[string]int, order []string, err error) {
	list, err := materialsLayout(h)
	if err != nil {
		return nil, nil, err
	}
	need = map[string]int{}
	miss, err := notBuilt(h, list) // пачками: по одной — 1 мин 40 с в начале и в проверке
	if err != nil {
		return nil, nil, err
	}
	for _, pl := range miss {
		if need[pl.Name] == 0 {
			order = append(order, pl.Name)
		}
		need[pl.Name]++
	}
	plan, _ := LoadPlan()
	for _, c := range materialsClusters(plan) {
		if c.Kind == cluster.Smelt {
			for _, it := range slices.Sorted(maps.Keys(outBufferItems)) {
				if need[it] == 0 {
					order = append(order, it)
				}
				need[it] += outBufferItems[it]
			}
		}
	}
	return need, order, nil
}

// queuedStock — что ещё выйдет из очереди крафта (свои крафты, не полуфабрикаты): стадия 14
// конца очереди не ждёт, стоящее в ней — как есть.
func queuedStock(h *act.Hands) (map[string]int, error) {
	p, err := h.B.Player()
	if err != nil {
		return nil, err
	}
	w := &worker{h: h}
	res := map[string]int{}
	for _, q := range p.CraftingQueue {
		if !q.Prerequisite {
			res[q.Recipe] += q.Count * w.perCraft(q.Recipe)
		}
	}
	return res, nil
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
	need, names, err := materialsNeed(h)
	if err != nil {
		return err
	}
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
	// порядок очереди — порядок использования (names: стадия 16, 17, столбы, буферы — stages.md:
	// конца очереди не ждём, 16-я ждёт только своё, и оно должно быть впереди); внутри —
	// сначала то, что тратит другие нужные предметы (подземки, делители — ленты), иначе их крафт
	// съест уже готовое; на всякий случай — несколько проходов
	uses := func(a, b string) bool {
		for _, in := range h.Protos.Recipes[a].Ingredients {
			if in.Name == b {
				return true
			}
		}
		return false
	}
	rank := map[string]int{}
	for i, it := range names {
		rank[it] = i
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, b := names[i], names[j]
		if uses(a, b) && !uses(b, a) {
			return true // потребитель — перед ингредиентом, даже если нужен позже
		}
		if uses(b, a) && !uses(a, b) {
			return false
		}
		return rank[a] < rank[b]
	})
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
	// всё в очередь без ожидания; конца очереди не ждать (stages.md, решение пользователя 30.10:
	// бот стоял 58 мин — очередь докрафтится по ходу стадии 15, следующие стадии ждут предмет
	// из очереди, только когда он нужен). Очередь — в сохранении игры, снимок её не теряет
	s.noWait = true
	err = passes()
	s.noWait = false
	if err != nil {
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

func checkMaterials(h *act.Hands) error {
	need, _, err := materialsNeed(h)
	if err != nil {
		return err
	}
	stock, err := materialsStock(h)
	if err != nil {
		return err
	}
	queued, err := queuedStock(h) // в очереди — как есть (конца очереди стадия не ждёт)
	if err != nil {
		return err
	}
	for it, n := range need {
		if stock[it]+queued[it] < n {
			return fmt.Errorf("%s: есть %d (в очереди %d), нужно %d", it, stock[it], queued[it], n)
		}
	}
	for it, n := range materialsExtra {
		if stock[it] < n {
			return fmt.Errorf("запас %s: есть %d, нужно %d", it, stock[it], n)
		}
	}
	return nil
}
