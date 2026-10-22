package stage

import (
	"context"
	"encoding/json"
	"os"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 19 (stages.md): сначала — рост старых кластеров по плану стадии 18 (новые машины,
// продление лент, столбы), потом кластер провода и схем как кластеры 14–15 (makeLayout/runMake).

const (
	s19Grow    = "state-stage19-grow.json"
	s19Product = "electronic-circuit"
)

func init() {
	register(Stage{N: 19, Name: "рост и кластер схем", Run: runCircuits, Check: checkCircuits})
}

// s18Added — что стадия 18 дописала в план (нет в плане до неё).
func s18Added() ([]layout.Placement, *ClusterPlan, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	base, err := loadPlanFile(runPath(s18Base))
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

// s19GrowLayout — рост из плана стадии 18: дописанное в старые кластеры добычи, плавки и
// станции и в их логистику, продление лент шин их продуктов, делители на них, столбы к сети;
// один раз на карту (файл).
func s19GrowLayout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s19Grow)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil {
			return list, nil
		}
	}
	added, plan, err := s18Added()
	if err != nil {
		return nil, err
	}
	tags := map[string]bool{}
	items := map[string]bool{}
	allow := []string{}
	for _, c := range plan.Clusters {
		switch c.Kind {
		case cluster.Mine, cluster.Smelt, cluster.Power:
			tags[cluster.Tag(c.Name)], tags[cluster.LogisticsTag+"/"+c.Name] = true, true
			for _, bus := range busNames(plan) {
				tags[cluster.LogisticsTag+"/"+bus+"/"+c.Name] = true
			}
			for _, o := range c.Out {
				items[o.Item] = true
			}
			allow = append(allow, c.Name)
		case cluster.Bus:
			allow = append(allow, c.Name)
		}
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
	if err != nil {
		return nil, err
	}
	res = append(res, poles...)
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(runPath(s19Grow), data, 0o644)
}

// buildSorted — построить list (уже стоящее пропускается) в порядке order (машины — раньше лент
// и инсертеров), брать из хранения.
func buildSorted(ctx context.Context, h *act.Hands, list []layout.Placement, order []string) error {
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites, useStore: true}
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
	return s.build(ctx, sorted)
}

func runCircuits(ctx context.Context, h *act.Hands) error {
	grow, err := s19GrowLayout(h)
	if err != nil {
		return err
	}
	if len(grow) > 0 {
		h.Say("рост: %d построек по плану стадии 18", len(grow))
		order := []string{"electric-mining-drill", "stone-furnace", "boiler", "steam-engine", "transport-belt", "underground-belt",
			"splitter", chest, "inserter", "burner-inserter", "small-electric-pole"}
		if err := buildSorted(ctx, h, grow, order); err != nil {
			return err
		}
	}
	return runMake(ctx, h, s19Product, 19)
}

func checkCircuits(h *act.Hands) error {
	grow, err := s19GrowLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, grow); err != nil {
		return err
	}
	return checkMake(h, s19Product, 19)
}
