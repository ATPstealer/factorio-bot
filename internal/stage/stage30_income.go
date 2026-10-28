package stage

import (
	"context"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
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

// incomeLayout — будущая добыча из плана стадии 3 (Later: блоки до 2M, камень, кирпич, буры на
// потом в блоках первой итерации, их подводы и перекидки) и дописанное стадией 5 в плавку (не
// сталь); буфер кирпича; столбы к сети.
func incomeLayout(h *act.Hands) ([]layout.Placement, error) {
	return blueAddedFor(h, incomeFile, func(c *cluster.Cluster) bool {
		return c.Kind == cluster.Smelt && !c.IsSteel()
	}, true, func(plan *ClusterPlan, all *layout.Layout) ([]layout.Placement, error) {
		later := laterTags(plan)
		var res []layout.Placement
		for _, pl := range plan.Layout {
			if later[pl.Tag] {
				res = append(res, pl)
			}
		}
		// ленты шин, которые кормит только будущая добыча (камень): в плане они — с первой
		// итерацией, а не с добычей
		have := map[layout.Placement]bool{}
		for _, pl := range res {
			have[pl] = true
		}
		for _, pl := range laterLanes(h, plan) { // часть их бывает и в дописанном проектом синей
			if !have[pl] {
				res = append(res, pl)
			}
		}
		// ленты шин, дописанные стадией 5 (продления, новые ленты, их подземки и делители) — здесь
		// все сразу: стадия долгая и раньше всех частей синей (пустые ленты подождут)
		if added, _, err := blueAdded(); err == nil {
			buses := map[string]bool{}
			for _, b := range busNames(plan) {
				buses[cluster.Tag(b)] = true
			}
			for _, pl := range added {
				if buses[pl.Tag] {
					res = append(res, pl)
				}
			}
		}
		// кирпич — в буфер у кластера (на шину — когда появится потребитель): бот берёт на крафт
		for _, c := range plan.Clusters {
			if c.Kind == cluster.Smelt && c.Product == "stone-brick" {
				buf, err := outBuffer(h, all, c, 1)
				if err != nil {
					return nil, err
				}
				res = append(res, buf...)
			}
		}
		return res, nil
	})
}

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
