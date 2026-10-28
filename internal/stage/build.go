package stage

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Общее для стадий стройки: построить раскладку по порядку, задать рецепты и фильтры.

// buildSorted — построить list (уже стоящее пропускается) в порядке order (чего нет в order — в
// конце), недостающее — из хранения. Сборщики и инсертеры настраиваются сразу, как поставлены
// (configure): инсертеры — раньше лент, иначе буфер без фильтра набирает чужое.
func buildSorted(ctx context.Context, h *act.Hands, list []layout.Placement, order []string) error {
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites, useStore: true}
	s.placed = func(ctx context.Context, name string) error {
		var these []layout.Placement
		for _, pl := range list {
			if pl.Name == name {
				these = append(these, pl)
			}
		}
		return configure(ctx, h, these)
	}
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b layout.Placement) int {
		ia, ib := slices.Index(order, a.Name), slices.Index(order, b.Name)
		if ia < 0 {
			ia = len(order)
		}
		if ib < 0 {
			ib = len(order)
		}
		return ia - ib
	})
	// по участкам, а не по видам (stages.md, общее): участок — чанк; следующий — ближайший к
	// персонажу; в участке — виды по order
	regions, keys := buildRegions(sorted)
	if len(keys) <= 1 {
		return s.build(ctx, sorted)
	}
	s.all = sorted
	defer func() { s.all = nil }()
	done := map[world.Tile]bool{}
	for len(done) < len(keys) {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		var next world.Tile
		best := math.Inf(1)
		for _, k := range keys {
			c := bridge.Pos{X: float64(k.X*world.ChunkSize + world.ChunkSize/2), Y: float64(k.Y*world.ChunkSize + world.ChunkSize/2)}
			if d := dist2(c, p.Position); !done[k] && d < best {
				next, best = k, d
			}
		}
		done[next] = true
		reg := regions[next]
		s.placed = func(ctx context.Context, name string) error { // настроить — только этот участок
			var these []layout.Placement
			for _, pl := range reg {
				if pl.Name == name {
					these = append(these, pl)
				}
			}
			return configure(ctx, h, these)
		}
		if err := s.build(ctx, reg); err != nil {
			return err
		}
	}
	return nil
}

// buildRegions — list по участкам (чанк, 32×32), порядок внутри — как в list; подземки — парой
// (вход, выход — ugPairs) в участке входа. keys — участки в порядке первого появления.
func buildRegions(list []layout.Placement) (map[world.Tile][]layout.Placement, []world.Tile) {
	res := map[world.Tile][]layout.Placement{}
	var keys []world.Tile
	of := func(pl layout.Placement) world.Tile {
		t := world.TileOf(pl.Pos)
		return world.Tile{X: int(math.Floor(float64(t.X) / world.ChunkSize)), Y: int(math.Floor(float64(t.Y) / world.ChunkSize))}
	}
	add := func(k world.Tile, pl layout.Placement) {
		if res[k] == nil {
			keys = append(keys, k)
		}
		res[k] = append(res[k], pl)
	}
	var ugs []layout.Placement
	for _, pl := range list {
		if strings.HasPrefix(pl.Name, "underground") {
			ugs = append(ugs, pl)
		}
	}
	pairOf := map[layout.Placement]world.Tile{} // подземка → участок её входа
	pairs := ugPairs(ugs)
	for i := 0; i < len(pairs); i += 2 {
		k := of(pairs[i])
		pairOf[pairs[i]] = k
		if i+1 < len(pairs) {
			pairOf[pairs[i+1]] = k
		}
	}
	for _, pl := range list {
		if k, ok := pairOf[pl]; ok {
			add(k, pl)
			continue
		}
		add(of(pl), pl)
	}
	return res, keys
}

// filterHold — фильтр-заглушка, пока нужный предмет не открыт: инсертер с ним стоит (уголь на
// лентах пакетов и продуктов не бывает); без фильтра он перекладывал бы всё подряд. Нужный
// фильтр добавляется позже (белый список «уголь + предмет» — берёт только предмет).
const filterHold = "coal"

// lockedItems — предметы, чей рецепт ещё не открыт: в 2.1 окно выбора фильтра их не показывает
// («Nothing found» — стадия 21 на 859088572, фильтр зелёных пакетов до исследования).
func lockedItems(h *act.Hands) (map[string]bool, error) {
	f, err := h.B.Force()
	if err != nil {
		return nil, err
	}
	res := map[string]bool{}
	for name := range h.Protos.Recipes {
		if _, item := h.Protos.Items[name]; item && !slices.Contains(f.Recipes, name) {
			res[name] = true
		}
	}
	return res, nil
}

// recipeMachine — у постройки есть рецепт, который ставит бот (сборщики, НПЗ, химзавод).
func recipeMachine(name string) bool {
	return strings.HasPrefix(name, "assembling-machine") || name == "oil-refinery" || name == "chemical-plant"
}

// filterInserter — инсертер с фильтром (у твердотопливного фильтров нет).
func filterInserter(name string) bool {
	return strings.HasSuffix(name, "inserter") && name != "burner-inserter"
}

// configure — рецепты машин (recipeMachine) и фильтры инсертеров (filterInserter) list по плану
// (Recipe). Фильтр на ещё не открытый предмет — заглушка filterHold, настоящий ставит стадия,
// которая предмет вводит; рецепт ещё не открыт — пропустить (waitOpen, потом configure ещё
// раз). Раньше — только сборщик 1 и обычный инсертер: сборщики 2, НПЗ, химзаводы, длинные и
// быстрые инсертеры синих стадий оставались без настройки (решение пользователя 26.10).
func configure(ctx context.Context, h *act.Hands, list []layout.Placement) error {
	locked, err := lockedItems(h)
	if err != nil {
		return err
	}
	for _, pl := range list {
		if pl.Recipe == "" || !recipeMachine(pl.Name) && !filterInserter(pl.Name) {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет %s в (%.1f, %.1f): %v", pl.Name, pl.Pos.X, pl.Pos.Y, err)
		}
		switch item := pl.Recipe; {
		case recipeMachine(pl.Name):
			if st.Recipe != pl.Recipe && !locked[pl.Recipe] {
				err = h.SetRecipe(ctx, st.Entity, pl.Recipe)
			}
		case !slices.Contains(st.Filters, pl.Recipe):
			if locked[item] {
				item = filterHold
			}
			if !slices.Contains(st.Filters, item) {
				err = h.SetFilter(ctx, st.Entity, item)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// openWait — сколько ждать исследования рецепта (зелёная наука: в очереди с обслуживания).
const openWait = 40 * time.Minute

// waitOpen — рецепты машин (recipeMachine) list открыты; нет — доставлять в очередь нужные
// исследования и ждать (страховка: обычно изучено заранее).
func waitOpen(ctx context.Context, h *act.Hands, list []layout.Placement) error {
	deadline := time.Now().Add(openWait)
	for said := false; ; said = true {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		var miss []string
		for _, pl := range list {
			if recipeMachine(pl.Name) && pl.Recipe != "" && !slices.Contains(f.Recipes, pl.Recipe) && !slices.Contains(miss, pl.Recipe) {
				miss = append(miss, pl.Recipe)
			}
		}
		if len(miss) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("рецепт %v не открыт за %v (идёт %s, %.0f%%)", miss, openWait, f.Current, f.Progress*100)
		}
		if !said {
			h.Say("жду исследования рецепта %v", miss)
		}
		if err := queueResearch(ctx, h); err != nil {
			return err
		}
		if err := sleepCtx(ctx, 30*time.Second); err != nil {
			return err
		}
	}
}
