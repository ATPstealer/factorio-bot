package stage

import (
	"context"
	"fmt"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 30 (stages.md): расширение дохода — из плана стадии 7 добыча до 2M (старые блоки — на
// всё месторождение, новые блоки со своими лентами bus-1 и делителями на подводах) и ещё плавка
// железа (выход — в ленту железа bus-2), отводы и делители на шинах, столбы к сети базы.

const incomeFile = "state-income.json" // раскладка стадии: один раз на карту

func init() {
	register(Stage{N: 30, Name: "расширение дохода", Run: runIncome, Check: checkIncome})
}

// incomeLayout — дописанное стадией 7 в добычу (не нефть) и плавку, их логистика, ленты шин их
// продуктов и делители на шинах; столбы к сети; один раз на карту.
func incomeLayout(h *act.Hands) ([]layout.Placement, error) {
	return onceLayout(incomeFile, func() ([]layout.Placement, error) {
		added, plan, err := blueAdded()
		if err != nil {
			return nil, err
		}
		tags := map[string]bool{}
		items := map[string]bool{}
		var allow []string
		for _, c := range plan.Clusters {
			if (c.Kind != cluster.Mine || c.IsOil()) && (c.Kind != cluster.Smelt || c.IsSteel()) {
				continue
			}
			tags[cluster.Tag(c.Name)], tags[cluster.LogisticsTag+"/"+c.Name] = true, true
			for _, bus := range busNames(plan) {
				tags[cluster.LogisticsTag+"/"+bus+"/"+c.Name] = true
			}
			items[c.Product] = true
			for _, o := range c.Out {
				items[o.Item] = true
			}
			allow = append(allow, c.Name)
		}
		buses := busNames(plan)
		var res []layout.Placement
		for _, pl := range added {
			onBus := slices.ContainsFunc(buses, func(b string) bool { return pl.Tag == cluster.Tag(b) }) &&
				(items[pl.Recipe] || pl.Name == "splitter")
			if tags[pl.Tag] || onBus {
				res = append(res, pl)
			}
		}
		if len(res) == 0 {
			return nil, fmt.Errorf("в плане стадии 7 нет роста добычи и плавки")
		}
		poles, err := connectPower(h, plan, res, allow)
		return append(res, poles...), err
	})
}

func runIncome(ctx context.Context, h *act.Hands) error {
	list, err := incomeLayout(h)
	if err != nil {
		return err
	}
	h.Say("доход: %d построек по плану стадии 7", len(list))
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
	for _, pl := range list {
		if pl.Name != "electric-mining-drill" {
			continue
		}
		if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && st.Network == 0 {
			return fmt.Errorf("бур (%.1f, %.1f) без сети", pl.Pos.X, pl.Pos.Y)
		}
	}
	return nil
}
