package plan

import (
	"fmt"
	"sort"

	"factorio-bot/internal/knowledge"
)

// Researched — технология изучена. Trigger-технологии 2.0 открываются действием
// (скрафтить N предметов, поставить, добыть); обычные — наукой в лабораториях (7.3).
type Researched struct{ Tech string }

func (g Researched) Key() string    { return "research:" + g.Tech }
func (g Researched) String() string { return "изучить " + g.Tech }
func (g Researched) Satisfied(s *State) bool {
	return s.Av.All || s.Av.Researched[g.Tech]
}

func (g Researched) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	if g.Satisfied(s) {
		return nil, nil
	}
	t, ok := s.Protos.Technologies[g.Tech]
	if !ok {
		return nil, fmt.Errorf("plan: нет технологии %s", g.Tech)
	}
	pre := append([]string(nil), t.Prerequisites...)
	sort.Strings(pre)
	for _, p := range pre {
		if !s.Av.Researched[p] {
			return sub(pl, s, c, Researched{p})
		}
	}
	tr := t.Trigger
	if tr == nil {
		return g.byScience(pl, s, c, t)
	}
	switch tr.Type {
	case "craft-item":
		// счётчик — статистика производства: произвести недостающее сверх того, что есть
		n := max(tr.Count, 1) - int(s.Produced[tr.Item])
		if n <= 0 {
			n = 1 // счётчик набран, а технология ещё не открылась — ещё одну
		}
		return sub(pl, s, c, HaveItem{tr.Item, s.Inventory[tr.Item] + n})
	}
	return nil, fmt.Errorf("plan: %s: условие %s пока не умею", g.Tech, tr.Type)
}

// labPacks — сколько каждой науки держим в лаборатории за раз.
const labPacks = 10

// byScience — лаборатории в сети (раскладка "research"), наука в них, запуск исследования,
// ожидание.
func (g Researched) byScience(pl *Planner, s *State, c *ctx, t knowledge.Technology) (*Primitive, error) {
	if s.Design == nil {
		return nil, fmt.Errorf("plan: %s: негде исследовать", g.Tech)
	}
	site, ok := s.Design(s, "research")
	if !ok {
		return nil, fmt.Errorf("plan: %s: нет места под лаборатории", g.Tech)
	}
	if !site.Satisfied(s) {
		return sub(pl, s, c, site)
	}
	var labs []string
	for i, p := range site.List {
		if s.Protos.Entities[p.Name].Type == "lab" {
			labs = append(labs, BuildRole(site.Tag, i))
		}
	}
	if len(labs) == 0 {
		return nil, fmt.Errorf("plan: в %s нет лабораторий", site.Tag)
	}
	// наука — в пустые лаборатории (на всех разом)
	for _, ing := range t.UnitIngredients {
		var empty []string
		for _, r := range labs {
			if s.Role(r).Count(ing.Name) == 0 {
				empty = append(empty, r)
			}
		}
		if len(empty) == 0 {
			continue
		}
		if need := labPacks * len(empty); s.Inventory[ing.Name] < need {
			return sub(pl, s, c, HaveItem{ing.Name, need})
		}
		return sub(pl, s, c, Supplied{empty[0], ing.Name, labPacks})
	}
	if !s.Researching[g.Tech] {
		return c.prim(Primitive{Kind: "research", Item: g.Tech}), nil
	}
	return c.prim(Primitive{Kind: "await", Item: g.Tech, Role: labs[0]}), nil
}

// lockedBy — технология, открывающая рецепт предмета (рецепт есть, но не изучен).
func (s *State) lockedBy(item string) string {
	recs := s.Protos.RecipesFor(item)
	sort.Strings(recs)
	for _, r := range recs {
		if s.Protos.Usable(r) && !s.Av.Recipe(r) {
			if t := s.Protos.TechFor(r); t != "" {
				return t
			}
		}
	}
	return ""
}
