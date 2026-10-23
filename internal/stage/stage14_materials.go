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
// меди по плану стадии 5 (с лентами между ними и лентой угля на станцию) — скрафтить руками;
// сверх расчёта — запас materialsExtra; не влезает в инвентарь — в кластер хранения. Только
// материалы: строят стадии 16 (добыча угля) и 17 (добыча и плавка железа и меди).

// materialsExtra — запас сверх расчёта: стройке 16 и 17 — до дохода с её же цепочек.
var materialsExtra = map[string]int{"iron-plate": 1500, "copper-plate": 1000, "coal": 1000}

// outBufferItems — буфер выхода плавки (стадия 12: пластины копятся в контейнерах).
var outBufferItems = map[string]int{"inserter": 2, chest: 2}

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
	for _, pl := range list {
		e, err := h.B.EntityAt(pl.Name, pl.Pos)
		if err != nil {
			return nil, err
		}
		if e == nil {
			need[pl.Name]++
		}
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
	for pass := 0; pass < 3; pass++ {
		short := false
		for _, it := range names {
			stock, err := materialsStock(h)
			if err != nil {
				return err
			}
			if lack := need[it] - stock[it]; lack > 0 {
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
			break
		}
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

// store — свободных ячеек инвентаря меньше free — в хранение то, что не нужно для крафта
// сейчас: сначала готовые постройки, потом запас (уголь, пластины); need — что ещё ставить
// (не убирать).
func (s *worker) store(ctx context.Context, free int, need ...string) error {
	p, err := s.h.B.Player()
	if err != nil {
		return err
	}
	if p.InventorySize-len(p.InventorySlots) >= free {
		return nil
	}
	list, err := storageLayout()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoStorage, err) // плана ещё нет (до стадии 5)
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
	keep := map[string]bool{"wood": true, "stone": true, "iron-plate": true, "copper-plate": true, "coal": true,
		"iron-gear-wheel": true, "copper-cable": true, "electronic-circuit": true}
	order := []string{}
	for it := range p.Inventory {
		if !keep[it] {
			order = append(order, it)
		}
	}
	sort.Strings(order)
	order = append(order, "coal", "iron-plate", "copper-plate", "wood", "stone")
	order = slices.DeleteFunc(order, func(it string) bool { return slices.Contains(need, it) })
	s.h.Say("инвентарь полон — кладу в хранение")
	k := 0
	for _, it := range order {
		for p.Inventory[it] > 0 && k < len(chests) {
			n, err := s.h.InsertAll(ctx, chests[k], it)
			if err != nil {
				return err
			}
			if n == 0 { // сундук полон — следующий
				k++
			}
			if p, err = s.h.B.Player(); err != nil {
				return err
			}
		}
		if p.InventorySize-len(p.InventorySlots) >= free+10 || k >= len(chests) {
			break
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
