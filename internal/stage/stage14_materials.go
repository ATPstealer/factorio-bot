package stage

import (
	"context"
	"fmt"
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
