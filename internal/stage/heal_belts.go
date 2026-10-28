package stage

import (
	"context"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Чужой предмет на ленте — убрать (stages.md, общее; решение пользователя 28.10): на лентах
// кластера (его и его подводов) — предмет, которого кластер не берёт и не делает; на ленте шины —
// не её предмет. Такие ленты поднять (предметы — в инвентарь) и поставить назад.

// beltAllowed — какие предметы может нести лента плана pl ("" — любые: не наша забота).
func beltAllowed(plan *ClusterPlan, byName map[string]*cluster.Cluster, buses map[string]bool, pl layout.Placement) map[string]bool {
	if name, ok := strings.CutPrefix(pl.Tag, "c/"); ok && buses[name] {
		if pl.Recipe == "" || pl.Recipe == "input" || pl.Recipe == "output" {
			return nil
		}
		return map[string]bool{pl.Recipe: true}
	}
	// кластер ленты: c/<кластер>…, c/logistics/<кластер>, c/logistics/<шина>/<кластер>
	rest := strings.TrimPrefix(pl.Tag, "c/")
	rest = strings.TrimPrefix(rest, cluster.LogisticsTag[len("c/"):]+"/")
	parts := strings.Split(rest, "/")
	var c *cluster.Cluster
	for _, p := range parts {
		if q, ok := byName[p]; ok && !buses[p] {
			c = q
		}
	}
	if c == nil || c.Kind != cluster.Make && c.Kind != cluster.Lab {
		return nil
	}
	res := map[string]bool{c.Product: true}
	for _, fs := range [][]cluster.Flow{c.In, c.Out, c.Internal} {
		for _, f := range fs {
			res[f.Item] = true
		}
	}
	for _, u := range c.Units {
		if r, ok := knownRecipes[u.Recipe]; ok {
			for _, it := range r {
				res[it] = true
			}
		}
	}
	return res
}

// knownRecipes — заполняет healBelts: рецепт → его ингредиенты и продукты.
var knownRecipes map[string][]string

// healBelts — ленты (простые, не подземки и не делители) с чужим предметом поднять и поставить
// назад.
func healBelts(ctx context.Context, h *act.Hands) error {
	plan, err := LoadBluePlan()
	if err != nil {
		return nil
	}
	knownRecipes = map[string][]string{}
	for name, r := range h.Protos.Recipes {
		for _, in := range r.Ingredients {
			knownRecipes[name] = append(knownRecipes[name], in.Name)
		}
		for _, p := range r.Products {
			knownRecipes[name] = append(knownRecipes[name], p.Name)
		}
	}
	byName := map[string]*cluster.Cluster{}
	for _, c := range plan.Clusters {
		byName[c.Name] = c
	}
	buses := map[string]bool{}
	for _, b := range busNames(plan) {
		buses[b] = true
	}
	var belts []layout.Placement
	var allowed []map[string]bool
	for _, pl := range plan.Layout {
		if pl.Name != "transport-belt" {
			continue
		}
		if a := beltAllowed(plan, byName, buses, pl); a != nil {
			belts, allowed = append(belts, pl), append(allowed, a)
		}
	}
	states, err := entityStates(h, belts)
	if err != nil {
		return err
	}
	var bad []layout.Placement
	var es []bridge.Entity
	for i, st := range states {
		if st == nil {
			continue
		}
		for it, n := range st.Belt {
			if n > 0 && !allowed[i][it] {
				bad, es = append(bad, belts[i]), append(es, st.Entity)
				h.Log("на ленте чужое: %s в (%.1f, %.1f), тег %s", it, belts[i].Pos.X, belts[i].Pos.Y, belts[i].Tag)
				break
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	h.Say("на лентах чужие предметы: %d лент — поднимаю и ставлю назад", len(bad))
	if err := h.PickupAll(ctx, es); err != nil {
		return err
	}
	if err := buildSorted(ctx, h, bad, []string{"transport-belt"}); err != nil {
		return err
	}
	s := &worker{h: h, useStore: true}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	return s.store(ctx, p.InventorySize-10)
}
