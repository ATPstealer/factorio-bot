package stage

import (
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадия 50 (stages.md): все печи — на стальные, все сборщики 1 — на сборщики 2, на месте: новая
// встаёт поверх старой (act.TierUp — одна группа замены: рецепт сохраняется, старая — в
// инвентарь). Ленты, инсертеры, столбы, сундуки — как есть (стадия 62).

// tier2Of — машина второго уровня на месте машины первого.
var tier2Of = map[string]string{"stone-furnace": "steel-furnace", "assembling-machine-1": "assembling-machine-2"}

func init() {
	s := blueStage{n: 50, name: "замена печей и сборщиков на второй уровень", file: "state-tier2.json",
		wait: []string{"advanced-material-processing", "automation-2"}, place: tier2Layout,
		fact: working("steel-furnace", "assembling-machine-2")}
	register(Stage{N: s.n, Name: s.name, Run: s.run, Check: s.check})
	yellowStages[s.n] = s
}

// tier2Layout — машины первого уровня плана, второго уровня — на тех же местах, с теми же
// рецептами.
func tier2Layout(*act.Hands) ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	var res []layout.Placement
	for _, pl := range plan.Layout {
		if up, ok := tier2Of[pl.Name]; ok {
			pl.Name = up
			res = append(res, pl)
		}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("в плане нет печей и сборщиков первого уровня")
	}
	return res, nil
}
