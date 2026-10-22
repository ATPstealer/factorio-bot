package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 12 (stages.md): кластеры добычи и плавки железа и меди по плану стадии 5 с буферами
// входа; руда — в ленты шины, плавка берёт руду и уголь с шины, пластины — в ленты шины.
// Буферы выхода (cluster.md 6а) — инсертер с фильтром на продукт берёт с выходной ленты
// плавки в сундук: пластины для крафта и стройки бота.

const s12Plan = "state-stage12.json"

func init() {
	register(Stage{N: 12, Name: "добыча и плавка железа и меди", Run: runSmelting, Check: checkSmelting})
}

// s12Layout — кластеры и подводы к плавке из плана, буферы выхода плавки (один раз на карту).
func s12Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s12Plan)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	res := s12Base(h, plan)
	var smelt []*cluster.Cluster
	var allow []string
	for _, c := range plan.Clusters {
		switch {
		case c.Kind == cluster.Smelt && (c.Product == "iron-plate" || c.Product == "copper-plate"):
			smelt = append(smelt, c)
			allow = append(allow, c.Name)
		case c.Kind == cluster.Mine && (c.Product == "iron-ore" || c.Product == "copper-ore" || c.Product == "coal"),
			c.Kind == cluster.Power, c.Kind == cluster.Bus:
			allow = append(allow, c.Name)
		}
	}
	all := layout.New(h.Protos, h.World)
	for _, pl := range plan.Layout {
		all.Add(pl)
	}
	for _, c := range smelt {
		buf, err := s12OutBuffer(h, all, c, s9OutBuffer[chest])
		if err != nil {
			return nil, err
		}
		res = append(res, buf...)
	}
	// питание — столбами по коридорам от сети
	poles, err := connectPower(h, plan, res, allow)
	if err != nil {
		return nil, err
	}
	res = append(res, poles...)
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(runPath(s12Plan), data, 0o644)
}

// s12OutBuffer — буфер выхода кластера c: сбоку от выходной ленты (от порта назад) pairs пар
// «инсертер с фильтром на продукт — сундук»; места — свободные от плана. Инсертер берёт с
// ленты всё, что успевает (~0.8 шт/с): у сборщика с выходом ~1 шт/с два инсертера забирали
// всё — на шину не шло ничего (стадия 14).
func s12OutBuffer(h *act.Hands, all *layout.Layout, c *cluster.Cluster, pairs int) ([]layout.Placement, error) {
	var out *cluster.Port
	for i := range c.Ports {
		if !c.Ports[i].In {
			out = &c.Ports[i]
		}
	}
	if out == nil {
		return nil, fmt.Errorf("у %s нет порта выхода", c.Name)
	}
	tag := "outbuf/" + c.Name
	var res []layout.Placement
	t := layout.Step(out.T, (out.Dir+8)%16)
	for k := 0; k < 12 && len(res) < 2*pairs; k++ {
		b := all.At(t)
		if b == nil || b.Name != "transport-belt" {
			break
		}
		for _, side := range []int{(b.Dir + 4) % 16, (b.Dir + 12) % 16} {
			ins, box := layout.Step(t, side), layout.Step(layout.Step(t, side), side)
			if len(res) >= 2*pairs || !all.Free(ins, layout.AnyLand) || !all.Free(box, layout.AnyLand) {
				continue
			}
			pi := layout.Placement{Name: "inserter", Pos: ins.Center(), Dir: layout.InserterDir(h.Protos, "inserter", ins, t), Recipe: out.Item, Tag: tag}
			pc := layout.Placement{Name: chest, Pos: box.Center(), Tag: tag}
			all.Add(pi)
			all.Add(pc)
			res = append(res, pi, pc)
		}
		// назад по ленте: тайл, чья лента смотрит в t
		next := world.Tile{}
		found := false
		for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
			q := layout.Step(t, (d+8)%16)
			if p := all.At(q); p != nil && p.Name == "transport-belt" && p.Dir == d && p.Tag == cluster.Tag(c.Name) {
				next, found = q, true
				break
			}
		}
		if !found {
			break
		}
		t = next
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("у выходной ленты %s нет места под буфер выхода", c.Name)
	}
	return res, nil
}

func runSmelting(ctx context.Context, h *act.Hands) error {
	list, err := s12Layout(h)
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites, useStore: true}
	// добыча и машины — потом ленты (подземки — парами по порядку плана), инсертеры, столбы
	order := []string{"electric-mining-drill", furnace, "assembling-machine-1", "transport-belt", "underground-belt", "splitter",
		chest, "inserter", "burner-inserter", "small-electric-pole"}
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b layout.Placement) int {
		return slices.Index(order, a.Name) - slices.Index(order, b.Name)
	})
	if err := s.build(ctx, sorted); err != nil {
		return err
	}
	// фильтры инсертеров буферов
	for _, pl := range list {
		if pl.Name != "inserter" || pl.Recipe == "" {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет инсертера в (%.1f, %.1f): %v", pl.Pos.X, pl.Pos.Y, err)
		}
		if slices.Contains(st.Filters, pl.Recipe) {
			continue
		}
		if err := h.SetFilter(ctx, st.Entity, pl.Recipe); err != nil {
			return err
		}
	}
	// пластины дошли до буферов выхода
	deadline := time.Now().Add(6 * time.Minute)
	for checkSmelting(h) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func checkSmelting(h *act.Hands) error {
	list, err := s12Layout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	got := map[string]int{}
	for _, pl := range list {
		if pl.Name != chest || !strings.HasPrefix(pl.Tag, "outbuf/") {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		for it, n := range st.Inventories["chest"] {
			got[it] += n
		}
	}
	for _, it := range []string{"iron-plate", "copper-plate"} {
		if got[it] == 0 {
			return fmt.Errorf("в буферах выхода нет %s", it)
		}
	}
	return nil
}
