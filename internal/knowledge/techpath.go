package knowledge

import (
	"fmt"
	"sort"
)

// TechPath — неизученные технологии, нужные для goals (вместе со всеми неизученными
// предками), в порядке исследования: каждая — после своих предпосылок.
func (p *Prototypes) TechPath(goals []string, researched map[string]bool) ([]string, error) {
	var order []string
	state := map[string]int{} // 1 — в обходе, 2 — готово
	var visit func(t string) error
	visit = func(t string) error {
		if researched[t] || state[t] == 2 {
			return nil
		}
		if state[t] == 1 {
			return fmt.Errorf("knowledge: цикл в зависимостях технологий на %s", t)
		}
		tech, ok := p.Technologies[t]
		if !ok {
			return fmt.Errorf("knowledge: нет технологии %s", t)
		}
		state[t] = 1
		pre := append([]string(nil), tech.Prerequisites...)
		sort.Strings(pre)
		for _, q := range pre {
			if err := visit(q); err != nil {
				return err
			}
		}
		state[t] = 2
		order = append(order, t)
		return nil
	}
	gs := append([]string(nil), goals...)
	sort.Strings(gs)
	for _, g := range gs {
		if err := visit(g); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// TechCost — цена списка технологий: научные пакеты по видам, время (с) при скорости
// лабораторий 1 и одной лаборатории, и действия для trigger-технологий.
type TechCost struct {
	Packs    map[string]float64
	Seconds  float64
	Triggers []string // «craft-item lab ×1» и т.п.
}

func (p *Prototypes) Cost(techs []string) TechCost {
	c := TechCost{Packs: map[string]float64{}}
	for _, t := range techs {
		tech := p.Technologies[t]
		if tech.Trigger != nil {
			what := tech.Trigger.Item
			if what == "" {
				what = tech.Trigger.Entity
			}
			n := tech.Trigger.Count
			if n == 0 {
				n = 1
			}
			c.Triggers = append(c.Triggers, fmt.Sprintf("%s: %s %s ×%d", t, tech.Trigger.Type, what, n))
			continue
		}
		for _, in := range tech.UnitIngredients {
			c.Packs[in.Name] += in.Amount * tech.UnitCount
		}
		c.Seconds += tech.UnitTime * tech.UnitCount
	}
	return c
}

// TechsFor — технологии, открывающие недоступные рецепты из списка.
func (p *Prototypes) TechsFor(recipes []string, a *Availability) []string {
	set := map[string]bool{}
	for _, r := range recipes {
		if a.Recipes[r] {
			continue
		}
		if t := p.TechFor(r); t != "" {
			set[t] = true
		}
	}
	res := make([]string, 0, len(set))
	for t := range set {
		res = append(res, t)
	}
	sort.Strings(res)
	return res
}
