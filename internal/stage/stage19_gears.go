package stage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 19 (stages.md): кластер шестерён по плану стадии 5, сразу как положено — отвод железа
// с шины делителем, буфер входа, сборщики с рецептом, выход — в ленту шестерён шины, буфер
// выхода, питание по коридорам. Общая часть (завод с шины) — makeLayout/runMake/checkMake.

func init() {
	register(Stage{N: 19, Name: "кластер шестерён",
		Run:   func(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, "iron-gear-wheel") },
		Check: func(h *act.Hands) error { return checkMake(h, "iron-gear-wheel") }})
}

// makeLayout — завод продукта product с шины: внутренность кластера, отводы с шины (с буферами
// входа), лента продукта шины с подводом, делители на ней, буфер выхода, столбы; один раз на
// карту (файл state-make-<продукт>.json).
func makeLayout(h *act.Hands, product string) ([]layout.Placement, *cluster.Cluster, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	c := clusterOf(plan, cluster.Make, product)
	if c == nil {
		return nil, nil, fmt.Errorf("в плане нет кластера %s", product)
	}
	list, err := onceLayout(fmt.Sprintf("state-make-%s.json", product), func() ([]layout.Placement, error) {
		res := tagged(plan, cluster.Tag(c.Name), cluster.LogisticsTag+"/"+c.Name)
		res = withSplitters(h, plan, append(res, busParts(plan, []string{product}, []string{c.Name})...))
		all := layout.New(h.Protos, h.World)
		for _, pl := range plan.Layout {
			all.Add(pl)
		}
		if !isScience(product) { // у науки буфера выхода нет — всё на шину (cluster.md §6а)
			buf, err := endBuffer(h, all, plan, c, product)
			if err != nil {
				return nil, err
			}
			res = append(res, buf...)
		}
		poles, err := connectPower(h, plan, res, []string{c.Name})
		return append(res, poles...), err
	})
	return list, c, err
}

// endBuffer — буфер выхода сборки в конце ленты продукта на шине (самый дальний её конец):
// туда доходит только излишек, мимо потребителей. На выходной ленте кластера инсертер буфера
// (~0.8 шт/с) забирал весь выход (схемы 0.12/с) — на шину не шло ничего (стадия 20).
func endBuffer(h *act.Hands, all *layout.Layout, plan *ClusterPlan, c *cluster.Cluster, product string) ([]layout.Placement, error) {
	buses := map[string]bool{}
	for _, b := range busNames(plan) {
		buses[cluster.Tag(b)] = true
	}
	var end *layout.Placement
	for i, pl := range plan.Layout {
		if buses[pl.Tag] && pl.Recipe == product && pl.Name == "transport-belt" && (end == nil || pl.Pos.X > end.Pos.X) {
			end = &plan.Layout[i]
		}
	}
	if end == nil {
		return outBuffer(h, all, c, 1) // продукт выше не нужен — буфер на выходной ленте
	}
	tag := "outbuf/" + c.Name
	t := world.TileOf(end.Pos)
	for k := 0; k < 12; k++ {
		if b := all.At(t); b == nil || b.Name != "transport-belt" || b.Recipe != product {
			break
		}
		for _, side := range []int{(end.Dir + 4) % 16, (end.Dir + 12) % 16} {
			ins, box := layout.Step(t, side), layout.Step(layout.Step(t, side), side)
			if !all.Free(ins, layout.AnyLand) || !all.Free(box, layout.AnyLand) {
				continue
			}
			pi := layout.Placement{Name: "inserter", Pos: ins.Center(), Dir: layout.InserterDir(h.Protos, "inserter", ins, t), Recipe: product, Tag: tag}
			pc := layout.Placement{Name: chest, Pos: box.Center(), Tag: tag}
			all.Add(pi)
			all.Add(pc)
			return []layout.Placement{pi, pc}, nil
		}
		t = layout.Step(t, (end.Dir+8)%16) // назад по ленте
	}
	return nil, fmt.Errorf("у конца ленты %s нет места под буфер выхода %s", product, c.Name)
}

// isScience — пакет науки (у кластеров науки нет буфера выхода).
var isScience = cluster.IsScience

// runMake — построить завод (машины, ленты, инсертеры, столбы), рецепты сборщиков, фильтры
// инсертеров буферов; дождаться продукта на шине и в буфере выхода.
func runMake(ctx context.Context, h *act.Hands, product string) error {
	list, _, err := makeLayout(h, product)
	if err != nil {
		return err
	}
	order := []string{"assembling-machine-1", "transport-belt", "underground-belt", "splitter", chest, "inserter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	if err := waitOpen(ctx, h, list); err != nil { // рецепт ещё не изучен — ждать только перед ним
		return err
	}
	if err := configure(ctx, h, list); err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Minute)
	for checkMake(h, product) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// checkMake — всё стоит, рецепты и фильтры заданы; продукт — на ленте шины (на любой её ленте
// после подвода) и в буфере выхода.
func checkMake(h *act.Hands, product string) error {
	list, c, err := makeLayout(h, product)
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
	if !inBuf && !isScience(product) {
		// буфер — в конце ленты продукта: потребитель выше по ходу (зелёная наука берёт
		// инсертеры) забирает всё, а излишка при слабом выпуске нет (стадия 24 на 859088572) —
		// тогда достаточно, что кластер производит: выпуск предмета за минуту растёт (бот стоит
		// — руками не крафтит)
		f0, err := h.B.Force()
		if err != nil {
			return err
		}
		time.Sleep(time.Minute)
		f1, err := h.B.Force()
		if err != nil {
			return err
		}
		if f1.Produced[product] <= f0.Produced[product] {
			return fmt.Errorf("в буфере выхода %s нет %s, и за минуту не произведено ни одного", c.Name, product)
		}
	}
	return nil
}
