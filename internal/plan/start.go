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

// StartModules — порядок точек: тег = руда-номер (раскладку даёт State.Design). Сначала
// 2 угольные пары, железо, медь (пример пользователя), дальше железо чаще: из пластин
// строятся следующие буры (9 на бур), медь на старте почти не нужна.
var StartModules = []string{
	"coal-1", "coal-2", "iron-ore-1", "copper-ore-1", "iron-ore-2", "iron-ore-3", "stone-1",
	"coal-3", "iron-ore-4", "copper-ore-2", "coal-4", "iron-ore-5", "copper-ore-3",
	"coal-5", "copper-ore-4", "copper-ore-5",
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
	if p, err := refuelRound(pl, s, c); p != nil || err != nil {
		return p, err
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

// refuelRound — дозаправить свои burner-постройки (кроме угольных пар — они сами) одним
// обходом: набрать уголь на всех разом и разнести маршрутом.
func refuelRound(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	// обход — когда у кого-то меньше четверти минимума; доливаем всем, у кого меньше трёх
	// четвертей; угля в инвентаре много — сверх минимума поровну, до полного слота
	p := c.prim(Primitive{Kind: "refuel", Item: "coal"})
	var caps []int
	need, low := 0, false
	feed := feeders(s)
	fedBy := map[string]bool{}
	for burner, chest := range feed {
		if ch := s.Role(chest); ch != nil {
			have := ch.In("chest", "coal")
			if b := s.Role(burner); have > 0 || b == nil || len(b.Inv["fuel"]) > 0 || b.Status == "working" {
				fedBy[burner] = true // пустую пару «сундук + бойлер» — заправить бойлер напрямую
			}
			low = low || have < chestFeedTarget/4
			if have < chestFeedTarget*3/4 {
				p.Roles = append(p.Roles, chest)
				p.Ns = append(p.Ns, chestFeedTarget-have)
				caps = append(caps, chestFeedTarget*2-have)
				need += chestFeedTarget - have
			}
		}
	}
	for _, o := range s.Owned {
		if fedBy[o.Role] {
			continue
		}
		if _, ore, ok := moduleOf(o.Role); ok && ore == "coal" || s.Protos.Entities[o.Name].Burner == nil {
			continue
		}
		// ручная плавильня и площадки/линии старых целей — у них своя заправка
		if o.Role == "smelter" || strings.HasPrefix(o.Role, "mine-") || strings.HasPrefix(o.Role, "smelt-") {
			continue
		}
		full := fuelFor(s, o.Name)
		have := o.In("fuel", "coal")
		low = low || have < full/4
		if have < full*3/4 {
			p.Roles = append(p.Roles, o.Role)
			p.Ns = append(p.Ns, full-have)
			caps = append(caps, fuelCap(s, o.Name)-have)
			need += full - have
		}
	}
	if !low || pl.banned(p.Key()) {
		return nil, nil
	}
	if s.Inventory["coal"] < need {
		return sub(pl, s, c, HaveItem{"coal", need})
	}
	spreadExtra(p.Ns, caps, s.Inventory["coal"]-need)
	for i, r := range p.Roles {
		// топливо до полного слота — Ctrl+клик (сундуки — отмеренно: туда влезет весь уголь)
		p.Full = append(p.Full, p.Ns[i] >= caps[i] && s.Role(r).Name != Chest)
	}
	return p, nil
}

// fuelCap — сколько угля влезает в топливо постройки (слоты × стопка).
func fuelCap(s *State, entity string) int {
	b := s.Protos.Entities[entity].Burner
	stack := s.Protos.Items["coal"].StackSize
	if b == nil || stack == 0 {
		return fuelFor(s, entity)
	}
	return max(1, b.FuelInventorySize) * stack
}

// spreadExtra — лишний уголь (extra) поровну сверх ns, не больше caps (запас — меньше
// подходить).
func spreadExtra(ns, caps []int, extra int) {
	for extra > 0 {
		open := 0
		for i := range ns {
			if ns[i] < caps[i] {
				open++
			}
		}
		if open == 0 {
			return
		}
		share := max(1, extra/open)
		for i := range ns {
			if extra == 0 {
				return
			}
			if add := min(share, caps[i]-ns[i], extra); add > 0 {
				ns[i] += add
				extra -= add
			}
		}
	}
}
