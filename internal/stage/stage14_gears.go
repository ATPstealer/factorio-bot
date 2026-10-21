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
)

// Стадия 14 (stages.md): кластер шестерён по плану стадии 5, сразу как положено — отвод железа
// с шины делителем, буфер входа, сборщики с рецептом, выход — в ленту шестерён шины, буфер
// выхода, питание по коридорам. Общая часть (завод с шины) — makeLayout/runMake/checkMake.

func init() {
	register(Stage{N: 14, Name: "кластер шестерён",
		Run:   func(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, "iron-gear-wheel", 14) },
		Check: func(h *act.Hands) error { return checkMake(h, "iron-gear-wheel", 14) }})
}

// makeLayout — завод продукта product с шины (стадия n): внутренность кластера, отводы с
// шины (с буферами входа), лента продукта шины с подводом, делители на ней, буфер выхода,
// столбы; один раз на карту (файл state-stage<n>.json).
func makeLayout(h *act.Hands, product string, n int) ([]layout.Placement, *cluster.Cluster, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	c := clusterOf(plan, cluster.Make, product)
	if c == nil {
		return nil, nil, fmt.Errorf("в плане нет кластера %s", product)
	}
	file := runPath(fmt.Sprintf("state-stage%d.json", n))
	if data, err := os.ReadFile(file); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, c, nil
		}
	}
	res := tagged(plan, cluster.Tag(c.Name), cluster.LogisticsTag+"/"+c.Name)
	res = withSplitters(h, plan, append(res, busParts(plan, []string{product}, []string{c.Name})...))
	all := layout.New(h.Protos, h.World)
	for _, pl := range plan.Layout {
		all.Add(pl)
	}
	buf, err := s12OutBuffer(h, all, c)
	if err != nil {
		return nil, nil, err
	}
	res = append(res, buf...)
	allow := []string{c.Name}
	for _, o := range plan.Clusters {
		if o.Kind == cluster.Bus || o.Kind == cluster.Power {
			allow = append(allow, o.Name)
		}
	}
	poles, err := connectPower(h, plan, res, allow)
	if err != nil {
		return nil, nil, err
	}
	res = append(res, poles...)
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return nil, nil, err
	}
	return res, c, os.WriteFile(file, data, 0o644)
}

// runMake — построить завод (машины, ленты, инсертеры, столбы), рецепты сборщиков, фильтры
// инсертеров буферов; дождаться продукта на шине и в буфере выхода.
func runMake(ctx context.Context, h *act.Hands, product string, n int) error {
	list, _, err := makeLayout(h, product, n)
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites, useStore: true}
	order := []string{"assembling-machine-1", "transport-belt", "underground-belt", "splitter", chest, "inserter", "small-electric-pole"}
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b layout.Placement) int {
		return slices.Index(order, a.Name) - slices.Index(order, b.Name)
	})
	if err := s.build(ctx, sorted); err != nil {
		return err
	}
	for _, pl := range list {
		if pl.Recipe == "" || pl.Name != "assembling-machine-1" && pl.Name != "inserter" {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет %s в (%.1f, %.1f): %v", pl.Name, pl.Pos.X, pl.Pos.Y, err)
		}
		switch {
		case pl.Name == "assembling-machine-1" && st.Recipe != pl.Recipe:
			err = h.SetRecipe(ctx, st.Entity, pl.Recipe)
		case pl.Name == "inserter" && !slices.Contains(st.Filters, pl.Recipe):
			err = h.SetFilter(ctx, st.Entity, pl.Recipe)
		}
		if err != nil {
			return err
		}
	}
	deadline := time.Now().Add(8 * time.Minute)
	for checkMake(h, product, n) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// checkMake — всё стоит, рецепты и фильтры заданы; продукт — на ленте шины (на любой её ленте
// после подвода) и в буфере выхода.
func checkMake(h *act.Hands, product string, n int) error {
	list, c, err := makeLayout(h, product, n)
	if err != nil {
		return err
	}
	onBus, inBuf := false, false
	for _, pl := range list {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("не стоит %s в (%.1f, %.1f)", pl.Name, pl.Pos.X, pl.Pos.Y)
		}
		switch {
		case pl.Name == "assembling-machine-1" && pl.Recipe != "" && st.Recipe != pl.Recipe:
			return fmt.Errorf("сборщик в (%.1f, %.1f): рецепт %q, нужен %s", pl.Pos.X, pl.Pos.Y, st.Recipe, pl.Recipe)
		case pl.Name == "inserter" && pl.Recipe != "" && !slices.Contains(st.Filters, pl.Recipe):
			return fmt.Errorf("инсертер в (%.1f, %.1f) без фильтра %s", pl.Pos.X, pl.Pos.Y, pl.Recipe)
		case pl.Recipe == product && pl.Name == "transport-belt" && st.Belt[product] > 0:
			onBus = true
		case pl.Name == chest && strings.HasPrefix(pl.Tag, "outbuf/") && st.Inventories["chest"][product] > 0:
			inBuf = true
		}
	}
	if !onBus {
		return fmt.Errorf("%s (%s) не на шине", product, c.Name)
	}
	if !inBuf {
		return fmt.Errorf("в буфере выхода %s нет %s", c.Name, product)
	}
	return nil
}
