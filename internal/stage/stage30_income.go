package stage

import (
	"context"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадия 30 (stages.md): расширение дохода — из плана стадии 5 добыча до 2M (старые блоки — на
// всё месторождение, новые блоки со своими лентами bus-1 и делителями на подводах) и ещё плавка
// железа (выход — в ленту железа bus-2), отводы и делители на шинах, столбы к сети базы. В
// самом начале — сталь в очередь исследований (стадия долгая).

const incomeFile = "state-income.json" // раскладка стадии: один раз на карту

func init() {
	register(Stage{N: 30, Name: "расширение дохода", Run: runIncome, Check: checkIncome})
}

// incomeLayout — постройки плана стадии 30: добыча до цели (новые блоки и буры сверх цели стадий
// 16–17 — тег «later»), их подводы, перекидки и ленты bus-1, кирпич (буфер выхода — в шаблоне);
// столбы к сети.
func incomeLayout(h *act.Hands) ([]layout.Placement, error) { return addedFor(h, incomeFile, 30) }

func runIncome(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, steelTech); err != nil {
		return err
	}
	list, err := incomeLayout(h)
	if err != nil {
		return err
	}
	h.Say("доход: %d построек по плану стадии 5", len(list))
	order := []string{"electric-mining-drill", "stone-furnace", chest, "inserter", "long-handed-inserter",
		"burner-inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
	return buildSorted(ctx, h, list, order)
}

// checkIncome — всё из раскладки стоит, буры — в сети.
func checkIncome(h *act.Hands) error {
	list, err := incomeLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	var drills []layout.Placement
	for _, pl := range list {
		if pl.Name == "electric-mining-drill" {
			drills = append(drills, pl)
		}
	}
	states, err := entityStates(h, drills) // пачками: буров — сотни
	if err != nil {
		return err
	}
	for i, st := range states {
		if st != nil && st.Network == 0 {
			return fmt.Errorf("бур (%.1f, %.1f) без сети", drills[i].Pos.X, drills[i].Pos.Y)
		}
	}
	return nil
}
