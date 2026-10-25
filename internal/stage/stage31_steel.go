package stage

import (
	"context"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 31 (stages.md): сталь — кластер стали из плана стадии 7 (пары печей «пластины → сталь»,
// уголь второй лентой), отводы руды и угля с bus-1, лента стали bus-2 с буфером выхода, столбы.
// Исследование стали — поставлено на стадии 30; здесь — ждать, пока изучено.

const (
	steelFile = "state-steel.json" // раскладка стадии: один раз на карту
	steelTech = "steel-processing"
)

func init() {
	register(Stage{N: 31, Name: "сталь", Run: runSteel, Check: checkSteel})
}

func steelLayout(h *act.Hands) ([]layout.Placement, error) {
	return blueAddedFor(h, steelFile, func(c *cluster.Cluster) bool { return c.IsSteel() }, false)
}

func runSteel(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, "fast-inserter"); err != nil { // на стадию 32
		return err
	}
	if err := waitResearch(ctx, h, steelTech); err != nil {
		return err
	}
	list, err := steelLayout(h)
	if err != nil {
		return err
	}
	h.Say("сталь: %d построек по плану стадии 7", len(list))
	order := []string{"stone-furnace", chest, "inserter", "long-handed-inserter", "transport-belt",
		"underground-belt", "splitter", "small-electric-pole"}
	return buildSorted(ctx, h, list, order)
}

// checkSteel — всё из раскладки стоит; в печах стали — сталь или пластины (идёт работа).
func checkSteel(h *act.Hands) error {
	list, err := steelLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	for _, pl := range list {
		if pl.Name != "stone-furnace" || pl.Recipe != "steel-plate" {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		for _, inv := range st.Inventories {
			if inv["steel-plate"] > 0 || inv["iron-plate"] > 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("печи стали ещё ничего не получили")
}
