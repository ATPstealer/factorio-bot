package plan

import (
	"fmt"
	"strconv"
	"strings"
)

// Старт игры (стадия 7.0, решение пользователя): сначала дерево и камень с объектов-камней,
// потом добывающие точки на burner-бурах по кругу; дальше ресурсы — с точек.

const (
	StartWood  = 50
	StartStone = 200
)

// StartModules — порядок точек: тег = руда-номер (раскладку даёт State.Design).
var StartModules = []string{
	"coal-1", "coal-2", "iron-ore-1", "copper-ore-1", "stone-1",
	"coal-3", "iron-ore-2", "copper-ore-2", "coal-4", "iron-ore-3", "copper-ore-3",
	"coal-5", "iron-ore-4", "copper-ore-4", "iron-ore-5", "copper-ore-5",
}

// ModuleOre — руда точки по тегу ("iron-ore-2" → "iron-ore").
func ModuleOre(tag string) (string, bool) {
	i := strings.LastIndex(tag, "-")
	if i < 0 {
		return "", false
	}
	if _, err := strconv.Atoi(tag[i+1:]); err != nil {
		return "", false
	}
	for _, m := range StartModules {
		if m == tag {
			return tag[:i], true
		}
	}
	return "", false
}

// ModuleOf — точка, к которой относится роль постройки ("coal-2-1" → "coal-2", "coal").
func ModuleOf(role string) (tag, ore string, ok bool) { return moduleOf(role) }

func moduleOf(role string) (tag, ore string, ok bool) {
	i := strings.LastIndex(role, "-")
	if i < 0 {
		return "", "", false
	}
	tag = role[:i]
	ore, ok = ModuleOre(tag)
	return tag, ore, ok
}

// moduleProduct — что даёт точка руды ore: уголь и камень — сами, руда — пластины из печи.
func (s *State) moduleProduct(ore string) string {
	if p := s.smeltProduct(ore); p != "" && ore != "stone" {
		return p
	}
	return ore
}

type Start struct{}

func (g Start) Key() string { return "start" }
func (g Start) String() string {
	return "старт: дерево, камни, добывающие точки"
}

func (g Start) Satisfied(s *State) bool {
	if !s.Done["start:wood"] || !s.Done["start:stone"] || s.Design == nil {
		return false
	}
	for _, tag := range StartModules {
		if site, ok := s.Design(s, tag); ok && !site.Satisfied(s) {
			return false
		}
	}
	return true
}

func (g Start) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	// добыча руками в статистику производства не попадает — этапы отмечаются в Done
	if !s.Done["start:wood"] {
		return c.prim(Primitive{Kind: "gather", Item: "wood", N: StartWood, Source: "tree", Mark: "start:wood"}), nil
	}
	if !s.Done["start:stone"] {
		return c.prim(Primitive{Kind: "gather", Item: "stone", N: StartStone, Source: "rock", Mark: "start:stone"}), nil
	}
	if s.Design == nil {
		return nil, fmt.Errorf("plan: старт: негде проектировать точки")
	}
	for _, tag := range StartModules {
		site, ok := s.Design(s, tag)
		if !ok {
			continue // нет места/месторождения — следующая
		}
		if !site.Satisfied(s) {
			return sub(pl, s, c, site)
		}
	}
	return nil, nil
}

// fromOutposts — получить item с добывающих точек: дозаправить и подождать (руками не
// копать). nil, nil — точек под item нет или все стоят мёртвыми.
func (g HaveItem) fromOutposts(pl *Planner, s *State, c *ctx, lack int) (*Primitive, error) {
	// живые точки: хоть что-то в них работает (иначе ждать бесполезно); статус пуст —
	// неизвестен (симуляция) — считаем живой
	alive := map[string]bool{}
	for _, o := range s.Owned {
		if tag, _, ok := moduleOf(o.Role); ok && (o.Status == "working" || o.Status == "") {
			alive[tag] = true
		}
	}
	var best *Owned
	bestN := -1
	for _, o := range s.Owned {
		tag, ore, ok := moduleOf(o.Role)
		if !ok || s.moduleProduct(ore) != g.Item {
			continue
		}
		// точка без топлива — заправить (уголь — с угольных пар)
		if e := s.Protos.Entities[o.Name]; e.Burner != nil && len(o.Inv["fuel"]) == 0 && o.Status != "working" {
			if ore == "coal" && g.Item == "coal" {
				continue // угольная пара заправляет себя сама; мёртвую — руками ниже
			}
			if p, err := sub(pl, s, c, Supplied{o.Role, "coal", fuelFor(s, o.Name)}); p != nil || err != nil {
				return p, err
			}
		}
		if !alive[tag] {
			continue
		}
		n := o.Output(g.Item)
		if ore == "coal" {
			n = o.In("fuel", "coal")
		}
		if s.Protos.Entities[o.Name].Type == "furnace" || o.Name == Chest || ore == "coal" {
			if n > bestN {
				best, bestN = o, n
			}
		}
	}
	if best == nil {
		return nil, nil
	}
	return c.prim(Primitive{Kind: "wait", Role: best.Role, Item: g.Item, N: min(lack, 20)}), nil
}
