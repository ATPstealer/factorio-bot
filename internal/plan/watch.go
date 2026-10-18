package plan

import (
	"fmt"
	"sort"
	"strings"
)

// Наблюдение «ожидание против реальности» (стадия 11, просьба пользователя): каждый снимок
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

// MaxPowerBlocks — сколько блоков «помпа, бойлер, 2 двигателя» достраивать к станции.
const MaxPowerBlocks = 4

// powerBlocks — сколько блоков расширения станции уже спроектировано (power-N).
func powerBlocks(s *State) int {
	n := 0
	for k := 1; k <= MaxPowerBlocks; k++ {
		if s.Design == nil {
			break
		}
		if b, ok := s.Design(s, fmt.Sprintf("power-%d", k)); ok && b.Satisfied(s) {
			n = k
		} else {
			break
		}
	}
	return n
}

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
	if len(byStatus["no_power"])+len(byStatus["low_power"]) > 0 {
		victims := append(byStatus["no_power"], byStatus["low_power"]...)
		p := Problem{Key: "power", Role: victims[0].Role, What: fmt.Sprintf("нет питания у %d построек (%s…)", len(victims), victims[0].Role), Urgent: true}
		boilers := 0
		for _, o := range s.Owned {
			if s.Protos.Entities[o.Name].Type != "boiler" {
				continue
			}
			boilers++
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
			p.Cause = "бойлеры горят — мощности станции мало"
			if n := powerBlocks(s); n < MaxPowerBlocks {
				p.Fix = Site{fmt.Sprintf("power-%d", n+1)}
			}
		}
		if p.Cause == "" {
			p.Cause = "станции нет или столбы не связаны"
		}
		res = append(res, p)
	}
	// кормушка бойлера пуста — заполнить (бойлер с электроинсертером без неё встанет)
	for burner, chest := range feed {
		if ch := s.Role(chest); ch != nil && ch.In("chest", "coal") < chestFeedTarget/4 && s.Protos.Entities[s.Role(burner).Name].Type == "boiler" {
			res = append(res, Problem{Key: "feed:" + chest, Role: chest, What: "кормушка " + chest + " почти пуста", Fix: Supplied{chest, "coal", chestFeedTarget}})
		}
	}

	// burner-постройки без топлива (кроме угольных пар — живут сами; кормящихся из
	// сундука — если в сундуке есть)
	for _, o := range byStatus["no_fuel"] {
		if _, ore, ok := moduleOf(o.Role); ok && ore == "coal" {
			continue
		}
		if ch := s.Role(feed[o.Role]); ch != nil && ch.In("chest", "coal") > 0 {
			continue
		}
		if s.Protos.Entities[o.Name].Type == "boiler" {
			continue // это — в «нет питания»
		}
		res = append(res, Problem{Key: "fuel:" + o.Role, Role: o.Role, What: o.Role + " без топлива", Fix: Supplied{o.Role, "coal", fuelFor(s, o.Name)}})
	}

	// лаборатории ждут науку, а исследование идёт — проследить цепочку вверх
	if len(s.Researching) > 0 && len(byStatus["missing_science_packs"]) > 0 {
		labs := byStatus["missing_science_packs"]
		p := Problem{Key: "labs-idle", Role: labs[0].Role, What: fmt.Sprintf("лаборатории простаивают (%d) — нет науки", len(labs))}
		p.Cause = upstream(s)
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
