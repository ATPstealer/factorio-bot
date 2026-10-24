package stage

import (
	"context"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 23 (stages.md): сначала — рост старых кластеров по плану стадии 6 (новые машины,
// продление лент, столбы), потом кластер провода и схем как кластеры 19–20 (makeLayout/runMake).

const (
	growFile       = "state-grow.json"
	circuitProduct = "electronic-circuit"
)

func init() {
	register(Stage{N: 23, Name: "рост и кластер схем", Run: runCircuits, Check: checkCircuits})
}

// greenAdded — что стадия 6 дописала в план (нет в плане до неё).
func greenAdded() ([]layout.Placement, *ClusterPlan, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	base, err := loadPlanFile(runPath(greenBaseFile))
	if err != nil {
		return nil, nil, err
	}
	was := map[layout.Placement]bool{}
	for _, pl := range base.Layout {
		was[pl] = true
	}
	var res []layout.Placement
	for _, pl := range plan.Layout {
		if !was[pl] {
			res = append(res, pl)
		}
	}
	return res, plan, nil
}

// growLayout — рост из плана стадии 6: дописанное в старые кластеры добычи, плавки и
// станции и в их логистику, продление лент шин их продуктов, делители на них, столбы к сети;
// один раз на карту (файл).
func growLayout(h *act.Hands) ([]layout.Placement, error) {
	return onceLayout(growFile, func() ([]layout.Placement, error) {
		added, plan, err := greenAdded()
		if err != nil {
			return nil, err
		}
		tags := map[string]bool{}
		items := map[string]bool{}
		var allow []string
		for _, c := range plan.Clusters {
			if c.Kind != cluster.Mine && c.Kind != cluster.Smelt && c.Kind != cluster.Power {
				continue
			}
			tags[cluster.Tag(c.Name)], tags[cluster.LogisticsTag+"/"+c.Name] = true, true
			for _, bus := range busNames(plan) {
				tags[cluster.LogisticsTag+"/"+bus+"/"+c.Name] = true
			}
			for _, o := range c.Out {
				items[o.Item] = true
			}
			allow = append(allow, c.Name)
		}
		var res []layout.Placement
		for _, pl := range added {
			onBus := items[pl.Recipe] && slices.ContainsFunc(busNames(plan), func(b string) bool { return pl.Tag == cluster.Tag(b) })
			if tags[pl.Tag] || onBus {
				res = append(res, pl)
			}
		}
		res = withSplitters(h, plan, res)
		poles, err := connectPower(h, plan, res, allow)
		return append(res, poles...), err
	})
}

func runCircuits(ctx context.Context, h *act.Hands) error {
	grow, err := growLayout(h)
	if err != nil {
		return err
	}
	if len(grow) > 0 {
		h.Say("рост: %d построек по плану стадии 6", len(grow))
		order := []string{"electric-mining-drill", "stone-furnace", "boiler", "steam-engine", chest, "inserter",
			"burner-inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
		if err := buildSorted(ctx, h, grow, order); err != nil {
			return err
		}
	}
	return runMake(ctx, h, circuitProduct)
}

func checkCircuits(h *act.Hands) error {
	grow, err := growLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, grow); err != nil {
		return err
	}
	return checkMake(h, circuitProduct)
}
