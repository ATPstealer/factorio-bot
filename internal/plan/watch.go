package plan

import (
	"fmt"
	"sort"
	"strings"
)

// Наблюдение «ожидание против реальности» (стадия 12, просьба пользователя): каждый снимок
// состояния статусы своих построек сверяются с тем, что от них ждём. Проблема с известным
// исправлением становится целью Repair — она идёт первой (меняет приоритеты: сначала
// поднять питание/заправить, потом остальное); остальные — в журнал и консоль.

// Problem — расхождение ожидания и реальности.
type Problem struct {
	Key    string // для учёта «появилась/ушла»
	Role   string // где видно
	What   string // что не так (для человека)
	Cause  string // найденная причина (если удалось проследить)
	Fix    Goal   // исправление (nil — только сообщить)
	Urgent bool   // всё остальное без этого бессмысленно (нет питания)
}

func (p Problem) String() string {
	s := p.What
	if p.Cause != "" {
		s += " — причина: " + p.Cause
	}
	return s
}

// ResearchStall — исследование не продвигается столько секунд, считаем застрявшим.
const ResearchStall = 180

// Diagnose — проблемы своих построек и исследования.
func Diagnose(s *State) []Problem {
	var res []Problem
	feed := feeders(s)
	byStatus := map[string][]*Owned{}
	for _, o := range s.Owned {
		byStatus[o.Status] = append(byStatus[o.Status], o)
	}

	// нет питания: ищем причину на станциях (бойлер без топлива, кормушка пуста); бойлеры
	// горят, а питания мало — станции не хватает мощности: построить ещё блок
	// не подключённые к сети (столбов ещё нет — стройка не закончена) — это не нехватка
	// мощности: их достроит своя цель; станцию за это не расширять
	// и сеть без своего двигателя — столбы до станции ещё не дотянуты
	powered := map[int]bool{}
	for _, o := range s.Owned {
		if s.Protos.Entities[o.Name].Type == "generator" && o.Network != 0 {
			powered[o.Network] = true
		}
	}
	var victims, cut []*Owned
	for _, o := range append(byStatus["no_power"], byStatus["low_power"]...) {
		if o.Network == 0 || len(powered) > 0 && !powered[o.Network] {
			cut = append(cut, o)
		} else {
			victims = append(victims, o)
		}
	}
	if len(cut) > 0 && len(victims) == 0 {
		res = append(res, Problem{Key: "power-cut", Role: cut[0].Role, What: fmt.Sprintf("%d построек не подключены к сети (%s…)", len(cut), cut[0].Role), Cause: "стройка не закончена: нет столбов до станции"})
	}
	if len(victims) > 0 {
		p := Problem{Key: "power", Role: victims[0].Role, What: fmt.Sprintf("нет питания у %d построек (%s…)", len(victims), victims[0].Role), Urgent: true}
		boilers := 0
		for _, o := range s.Owned {
			if s.Protos.Entities[o.Name].Type != "boiler" {
				continue
			}
			boilers++
			if strings.HasPrefix(o.Role, "stage-") { // бойлер кластера — уголь лентой, не руками
				if len(o.Inv["fuel"]) == 0 && o.Status != "working" && p.Cause == "" {
					p.Cause = o.Role + " без топлива: уголь не доходит по ленте (см. кластеры)"
				}
				continue
			}
			if len(o.Inv["fuel"]) == 0 && o.Status != "working" {
				p.Cause = o.Role + " без топлива"
				if ch := s.Role(feed[o.Role]); ch != nil {
					p.Cause += ", кормушка " + ch.Role
					if ch.In("chest", "coal") == 0 {
						p.Cause += " пуста"
					}
				}
				// с запасом: бойлер на полной нагрузке жжёт уголь за 2 с — до полного слота
				p.Fix = Supplied{o.Role, "coal", fuelCap(s, o.Name)}
				break
			}
		}
		if p.Cause == "" && boilers > 0 {
			p.Cause = "бойлеры горят — мощности станции мало (рост кластера станции — следующий этап)"
		}
		if p.Cause == "" {
			p.Cause = "станции нет или столбы не связаны"
		}
		res = append(res, p)
	}
	// кормушка бойлера станции нулевого этапа почти пуста — дозаправить обходом
	for burner, chest := range feed {
		if ch := s.Role(chest); ch != nil && ch.In("chest", "coal") < chestFeedTarget/4 && s.Protos.Entities[s.Role(burner).Name].Type == "boiler" {
			res = append(res, Problem{Key: "feed:" + chest, Role: chest, What: "кормушка " + chest + " почти пуста", Fix: FeedRound{}})
		}
	}

	// burner-постройки без топлива (кроме угольных пар — живут сами; кормящихся из
	// сундука — если в сундуке есть)
	round, _, _, _ := refuelPlan(s)
	inRound := map[string]bool{}
	for _, r := range round.Roles {
		inRound[r] = true
	}
	for _, o := range byStatus["no_fuel"] {
		if _, ore, ok := moduleOf(o.Role); ok && ore == "coal" {
			continue
		}
		// постройки кластеров — уголь лентой: нехватку видит монитор кластеров (вход), руками
		// не заправлять (это для нулевого этапа)
		if strings.HasPrefix(o.Role, "stage-") {
			continue
		}
		if ch := s.Role(feed[o.Role]); ch != nil && ch.In("chest", "coal") > 0 {
			continue
		}
		if s.Protos.Entities[o.Name].Type == "boiler" {
			continue // это — в «нет питания»
		}
		// в общем обходе — обходом (все разом), иначе с запасом (до полного слота)
		var fix Goal = Supplied{o.Role, "coal", fuelCap(s, o.Name)}
		if inRound[o.Role] {
			fix = FeedRound{}
		}
		res = append(res, Problem{Key: "fuel:" + o.Role, Role: o.Role, What: o.Role + " без топлива", Fix: fix})
	}

	// лаборатории ждут науку, а исследование идёт — проследить цепочку вверх
	if len(s.Researching) > 0 && len(byStatus["missing_science_packs"]) > 0 {
		labs := byStatus["missing_science_packs"]
		p := Problem{Key: "labs-idle", Role: labs[0].Role, What: fmt.Sprintf("лаборатории простаивают (%d) — нет науки", len(labs))}
		// какой науки нет: нужна исследованию, а в лабораториях её нет; не делается вовсе —
		// так и сказать (а не «выход красной забит»)
		var missing []string
		need := map[string]bool{}
		for t := range s.Researching {
			for _, in := range s.Protos.Technologies[t].UnitIngredients {
				need[in.Name] = true
			}
		}
		for pack := range need {
			have := false
			for _, o := range labs {
				have = have || o.In("lab_input", pack) > 0
			}
			if !have {
				made := false
				for _, o := range s.Owned {
					made = made || o.Recipe == pack
				}
				if made {
					missing = append(missing, pack)
				} else {
					missing = append(missing, pack+" (не производится)")
				}
			}
		}
		sort.Strings(missing)
		p.Cause = upstream(s)
		if len(missing) > 0 {
			p.What += ": " + strings.Join(missing, ", ")
		}
		res = append(res, p)
	}

	// буры: месторождение выработано
	for _, o := range byStatus["no_minable_resources"] {
		res = append(res, Problem{Key: "depleted:" + o.Role, Role: o.Role, What: o.Role + ": руда под буром кончилась"})
	}

	// исследование стоит
	if s.ResearchStalled > ResearchStall {
		res = append(res, Problem{Key: "research-stall", What: fmt.Sprintf("исследование не идёт %d с", s.ResearchStalled), Cause: upstream(s)})
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].Urgent && !res[j].Urgent })
	return res
}

// upstream — первые сбои в цепочке производства науки: сборщики → печи → буры.
func upstream(s *State) string {
	var why []string
	add := func(o *Owned, what string) {
		if len(why) < 4 {
			why = append(why, o.Role+": "+what)
		}
	}
	for _, typ := range []string{"assembling-machine", "furnace", "mining-drill", "inserter"} {
		for _, o := range s.Owned {
			if s.Protos.Entities[o.Name].Type != typ {
				continue
			}
			switch o.Status {
			case "item_ingredient_shortage", "no_ingredients":
				add(o, "нет ингредиентов")
			case "full_output":
				add(o, "выход забит (дальше не забирают)")
			case "no_fuel":
				add(o, "нет топлива")
			case "no_power", "low_power":
				add(o, "нет питания")
			case "no_minable_resources":
				add(o, "руда кончилась")
			case "waiting_for_space_in_destination":
				if typ == "mining-drill" {
					add(o, "некуда класть")
				}
			case "no_recipe":
				add(o, "нет рецепта")
			}
		}
	}
	if len(why) == 0 {
		return "не найдена"
	}
	return strings.Join(why, "; ")
}

// Repair — исправить найденное наблюдением (Diagnose), что исправляется: идёт первой целью.
type Repair struct{}

func (g Repair) Key() string    { return "repair" }
func (g Repair) String() string { return "исправить расхождения" }

func (g Repair) Satisfied(s *State) bool {
	for _, p := range Diagnose(s) {
		if p.Fix != nil && !p.Fix.Satisfied(s) {
			return false
		}
	}
	return true
}

func (g Repair) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	for _, p := range Diagnose(s) {
		if p.Fix == nil || p.Fix.Satisfied(s) {
			continue
		}
		if pr, err := sub(pl, s, c, p.Fix); pr != nil || err != nil {
			return pr, err
		}
	}
	return nil, nil
}
