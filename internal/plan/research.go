package plan

import (
	"fmt"
	"slices"
	"sort"

	"factorio-bot/internal/knowledge"
)

// Researched — технология изучена. Trigger-технологии 2.0 открываются действием
// (скрафтить N предметов, поставить, добыть); обычные — наукой в лабораториях (7.3).
type Researched struct {
	Tech string
	// Site — раскладка с лабораториями (State.Design): "research" (по умолчанию) — науку
	// подкладывает бот; "science" — фабрика, наука идёт в лаборатории сама
	Site string
}

func (g Researched) Key() string    { return "research:" + g.Tech + ":" + g.Site }
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
			return sub(pl, s, c, Researched{Tech: p, Site: g.Site})
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
	case "mine-entity":
		// нефть (oil-processing): насосы на поле с питанием — качают сами
		if s.Protos.Entities[tr.Entity].ResourceCategory == "basic-fluid" {
			if p, err := sub(pl, s, c, Site{"oil-jacks"}); p != nil || err != nil {
				return p, err
			}
			return c.prim(Primitive{Kind: "idle", Item: "насосы качают " + tr.Entity, N: 10}), nil
		}
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
	kind := g.Site
	if kind == "" {
		kind = "research"
	}
	site, ok := s.Design(s, kind)
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
	auto := kind == "science" // наука в лаборатории идёт с ленты
	// наука — в пустые лаборатории (на всех разом)
	for _, ing := range t.UnitIngredients {
		if auto {
			break
		}
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
	if auto {
		return c.prim(Primitive{Kind: "await", Item: g.Tech}), nil // ждать, сколько идёт
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

// RedScience — автоматическая красная наука (7.6–7.8): фабрика ("science") построена и
// изучено всё на красной науке, что нужно для ракеты.
type RedScience struct{}

func (g RedScience) Key() string    { return "red-science" }
func (g RedScience) String() string { return "автоматическая красная наука" }

// GoalTech — конечная цель игры: всё изучаемое — только то, что ведёт к ней.
const GoalTech = "rocket-silo"

// onPath — технологии, ведущие к GoalTech (её предпосылки, рекурсивно).
func onPath(s *State) map[string]bool {
	res := map[string]bool{}
	stack := []string{GoalTech}
	for len(stack) > 0 {
		t := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if res[t] {
			continue
		}
		res[t] = true
		stack = append(stack, s.Protos.Technologies[t].Prerequisites...)
	}
	return res
}

// redTechs — технологии только на красной науке, нужные для ракеты (решение пользователя:
// оружие и прочее не изучать).
func redTechs(s *State) []string { return packTechs(s, "automation-science-pack") }

// packTechs — технологии на пути к ракете, которым хватает наук packs (не trigger).
func packTechs(s *State, packs ...string) []string {
	var res []string
	need := onPath(s)
	for name, t := range s.Protos.Technologies {
		if !need[name] || t.Trigger != nil || t.Hidden || len(t.UnitIngredients) == 0 {
			continue
		}
		ok := true
		for _, ing := range t.UnitIngredients {
			ok = ok && slices.Contains(packs, ing.Name)
		}
		if ok {
			res = append(res, name)
		}
	}
	sort.Strings(res)
	return res
}

func (g RedScience) Satisfied(s *State) bool {
	for _, t := range redTechs(s) {
		if !s.Av.Researched[t] {
			return false
		}
	}
	return true
}

func (g RedScience) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	if p, err := refuelRound(pl, s, c); p != nil || err != nil {
		return p, err
	}
	// фабрике нужны сборщики и электробуры (решение пользователя: с электричеством —
	// только электробуры) — их изучить наукой руками первыми
	for _, t := range []string{"automation", "electric-mining-drill"} {
		if !s.Av.Researched[t] {
			return sub(pl, s, c, Researched{Tech: t})
		}
	}
	if site, ok := s.Design(s, "science"); !ok || !site.Satisfied(s) {
		return sub(pl, s, c, Site{"science"})
	}
	return researchLoop(pl, s, c, redTechs(s))
}

// GreenScience — зелёная наука (8.1–8.2): блок "green" построен, изучено на красной+
// зелёной нужное для ракеты — до Target (её предпосылки; пусто — всё); наука в
// лаборатории фабрики идёт с лент.
type GreenScience struct{ Target string }

// techs — что изучать: всё на красной+зелёной для ракеты или только путь к Target.
func (g GreenScience) techs(s *State) []string {
	all := packTechs(s, greenPacks...)
	if g.Target == "" {
		return all
	}
	path, _ := s.Protos.TechPath([]string{g.Target}, map[string]bool{})
	var res []string
	for _, t := range all {
		if slices.Contains(path, t) {
			res = append(res, t)
		}
	}
	return res
}

var greenPacks = []string{"automation-science-pack", "logistic-science-pack"}

func (g GreenScience) Key() string    { return "green-science:" + g.Target }
func (g GreenScience) String() string { return "зелёная наука" }

func (g GreenScience) Satisfied(s *State) bool {
	for _, t := range g.techs(s) {
		if !s.Av.Researched[t] {
			return false
		}
	}
	return true
}

func (g GreenScience) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	if !(RedScience{}).Satisfied(s) {
		return sub(pl, s, c, RedScience{})
	}
	if p, err := refuelRound(pl, s, c); p != nil || err != nil {
		return p, err
	}
	if p, err := sub(pl, s, c, Site{"green"}); p != nil || err != nil {
		return p, err
	}
	return researchLoop(pl, s, c, g.techs(s))
}

// researchLoop — изучить techs по порядку TechPath наукой с лент фабрики; очередь до 3.
func researchLoop(pl *Planner, s *State, c *ctx, techs []string) (*Primitive, error) {
	order, err := s.Protos.TechPath(techs, s.Av.Researched)
	if err != nil {
		return nil, err
	}
	if len(s.Researching) > 0 && len(s.Researching) < 3 {
		for _, t := range order {
			if s.Av.Researched[t] || s.Researching[t] {
				continue
			}
			ready := true
			for _, pre := range s.Protos.Technologies[t].Prerequisites {
				ready = ready && (s.Av.Researched[pre] || s.Researching[pre])
			}
			if p := c.prim(Primitive{Kind: "research", Item: t}); ready && !pl.banned(p.Key()) {
				return p, nil
			}
		}
	}
	for _, t := range order {
		if !s.Av.Researched[t] {
			return sub(pl, s, c, Researched{Tech: t, Site: "science"})
		}
	}
	return nil, nil
}
