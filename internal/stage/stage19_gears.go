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
)

// Стадии 19, 29, 43 (stages.md) — пропуск: кластера шестерён нет (шестерни — в парах), вся
// станция — на стадии 11, роста нет (ленты шин — со своими кластерами); номера стадий не меняются.
// Общая часть сборки с шины — makeLayout/runMake/checkMake.

func init() {
	for _, n := range []int{19, 29, 43} {
		register(Stage{N: n, Name: "пропуск", Empty: true,
			Run:   func(context.Context, *act.Hands) error { return nil },
			Check: func(*act.Hands) error { return nil }})
	}
}

// makeLayout — сборка продукта product с шины (стадии 20, 23–26): постройки плана стадии её
// кластера — внутренность (буфер выхода — в шаблоне), отводы с шины, лента продукта шины,
// сквозные ленты её входов, столбы; один раз на карту (файл state-make-<продукт>.json).
func makeLayout(h *act.Hands, product string) ([]layout.Placement, *cluster.Cluster, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	c := clusterOf(plan, cluster.Make, product)
	if c == nil {
		return nil, nil, fmt.Errorf("в плане нет кластера %s", product)
	}
	list, err := addedFor(h, fmt.Sprintf("state-make-%s.json", product), c.Stage)
	return list, c, err
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
	order := []string{"assembling-machine-1", chest, "inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
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
