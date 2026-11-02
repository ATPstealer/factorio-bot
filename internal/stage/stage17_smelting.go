package stage

import (
	"context"
	"fmt"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадия 17 (stages.md): кластеры добычи и плавки железа и меди по плану; руда — в ленты шины,
// плавка берёт руду и уголь с шины, пластины пока не на шину (стадия 18). Буферы выхода
// (cluster.md 6а) — инсертер с фильтром на продукт берёт с выходной ленты плавки в сундук:
// пластины для крафта и стройки бота.

const smeltingFile = "state-smelting.json"

func init() {
	register(Stage{N: 17, Name: "добыча и плавка железа и меди", Run: runSmelting, Check: checkSmelting})
}

// smeltingLayout — постройки плана стадии 17: добыча железа и меди до цели, плавка (буферы выхода
// — в шаблоне), подводы и отводы с bus-1; столбы; один раз на карту.
func smeltingLayout(h *act.Hands) ([]layout.Placement, error) { return addedFor(h, smeltingFile, 17) }

func runSmelting(ctx context.Context, h *act.Hands) error {
	list, err := smeltingLayout(h)
	if err != nil {
		return err
	}
	// добыча и машины, сундуки и инсертеры (фильтры — сразу), потом ленты (подземки — парами по
	// порядку плана), столбы
	order := []string{"electric-mining-drill", furnace, "assembling-machine-1", chest, "inserter", "long-handed-inserter",
		"burner-inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	if err := configure(ctx, h, list); err != nil { // фильтры инсертеров буферов
		return err
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
	list, err := smeltingLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	got := map[string]int{}
	for _, b := range outBufs(h, list) {
		pl := b.chest
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
