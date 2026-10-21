package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 10 (stages.md): кластеры добычи и плавки железа и меди по плану стадии 3 с буферами
// входа; руда — лентой от добычи к плавке, уголь — к плавке (с добычи стадии 9). Шину не
// трогать: пластины копятся в контейнерах буфера выхода (cluster.md 6а) — инсертер с
// фильтром на продукт берёт с выходной ленты плавки в сундук.

const s10Plan = "state-stage10.json"

func init() {
	register(Stage{N: 10, Name: "добыча и плавка железа и меди", Run: runSmelting, Check: checkSmelting})
}

// s10Layout — кластеры и подводы к плавке из плана, буферы выхода плавки (один раз на карту).
func s10Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s10Plan)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	tags := map[string]bool{}
	var smelt []*cluster.Cluster
	for _, c := range s7Clusters(plan) {
		if c.Product == "coal" {
			continue
		}
		tags[cluster.Tag(c.Name)] = true
		if c.Kind == cluster.Smelt {
			tags[cluster.LogisticsTag+"/"+c.Name] = true
			smelt = append(smelt, c)
		}
	}
	all := layout.New(h.Protos, h.World)
	var res []layout.Placement
	for _, pl := range plan.Layout {
		all.Add(pl)
		if tags[pl.Tag] {
			res = append(res, pl)
		}
	}
	var allow []string
	for _, c := range plan.Clusters {
		if tags[cluster.Tag(c.Name)] || c.Kind == cluster.Power || c.Kind == cluster.Mine && c.Product == "coal" {
			allow = append(allow, c.Name)
		}
	}
	for _, c := range smelt {
		buf, err := s10OutBuffer(h, all, c)
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
	return res, os.WriteFile(runPath(s10Plan), data, 0o644)
}

// s10OutBuffer — буфер выхода плавки c: сбоку от выходной ленты (от порта назад) инсертер с
// фильтром на продукт и сундук за ним; места — свободные от плана.
func s10OutBuffer(h *act.Hands, all *layout.Layout, c *cluster.Cluster) ([]layout.Placement, error) {
	var out *cluster.Port
	for i := range c.Ports {
		if !c.Ports[i].In {
			out = &c.Ports[i]
		}
	}
	if out == nil {
		return nil, fmt.Errorf("у %s нет порта выхода", c.Name)
	}
	tag := "s10/" + c.Name
	var res []layout.Placement
	t := layout.Step(out.T, (out.Dir+8)%16)
	for k := 0; k < 12 && len(res) < 2*s7OutBuffer[chest]; k++ {
		b := all.At(t)
		if b == nil || b.Name != "transport-belt" {
			break
		}
		for _, side := range []int{(b.Dir + 4) % 16, (b.Dir + 12) % 16} {
			ins, box := layout.Step(t, side), layout.Step(layout.Step(t, side), side)
			if len(res) >= 2*s7OutBuffer[chest] || !all.Free(ins, layout.AnyLand) || !all.Free(box, layout.AnyLand) {
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
	list, err := s10Layout(h)
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
	list, err := s10Layout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	got := map[string]int{}
	for _, pl := range list {
		if pl.Name != chest || len(pl.Tag) < 4 || pl.Tag[:4] != "s10/" {
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
