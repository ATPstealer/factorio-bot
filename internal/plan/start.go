package plan

import (
	"fmt"
	"sort"
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
		if s.exhausted(tag) || s.Done["evicted:"+tag] || s.Done["dismantled"] || s.StartRemovable {
			continue
		}
		if site, ok := s.Design(s, tag); ok && !site.Satisfied(s) {
			return false
		}
	}
	return true
}

func (s *State) exhausted(tag string) bool { return s.Exhausted != nil && s.Exhausted(tag) }

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
		if s.exhausted(tag) || s.Done["evicted:"+tag] || s.Done["dismantled"] || s.StartRemovable {
			continue // руда под буром кончилась / место занял кластер / нулевой этап разобран
		}
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
	plan, caps, need, low := refuelPlan(s)
	p := c.prim(plan)
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

// refuelPlan — обход дозаправки: кого и сколько (кормушки бойлеров и burner-постройки),
// low — у кого-то меньше четверти: пора.
func refuelPlan(s *State) (p Primitive, caps []int, need int, low bool) {
	// обход — когда у кого-то меньше четверти минимума; доливаем всем, у кого меньше трёх
	// четвертей; угля в инвентаре много — сверх минимума поровну, до полного слота
	p = Primitive{Kind: "refuel", Item: "coal"}
	// обход — когда пустой кто-то или ниже четверти хотя бы пятая часть (по одной печи
	// ходить через каждые несколько минут — дорого)
	lowN, total, empty := 0, 0, false
	count := func(isLow, isEmpty bool) {
		total++
		if isLow {
			lowN++
		}
		empty = empty || isEmpty
	}
	rich := false
	for _, o := range s.Owned {
		rich = rich || strings.HasPrefix(o.Role, "coal-mine-")
	}
	feed := feeders(s)
	fedBy := map[string]bool{}
	for burner, chest := range feed {
		if b := s.Role(burner); b == nil || s.Protos.Entities[b.Name].Burner == nil {
			continue // уголь — сырьё рецепта (пластик у нефти): его заправляет стройка нефти
		}
		if ch := s.Role(chest); ch != nil {
			have := ch.In("chest", "coal")
			if b := s.Role(burner); have > 0 || b == nil || len(b.Inv["fuel"]) > 0 || b.Status == "working" {
				fedBy[burner] = true // пустую пару «сундук + бойлер» — заправить бойлер напрямую
			}
			count(have < chestFeedTarget/4, have == 0)
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
		// ручная плавильня и площадки/линии старых целей — у них своя заправка; нулевой
		// этап разбирается — его точки не заправлять
		if s.StartRemovable && startRole(o.Role) || o.Role == "smelter" || strings.HasPrefix(o.Role, "mine-") || strings.HasPrefix(o.Role, "smelt-") {
			continue
		}
		// кластеры (этапы "stage-N") — уголь лентой; бот только заправляет при стройке
		if strings.HasPrefix(o.Role, "stage-") {
			continue
		}
		// угольная шахта есть — до полного слота (печь: 50 угля ≈ 37 мин): обходы реже,
		// но крупнее; на старте угля мало — по минимуму
		full := fuelFor(s, o.Name)
		if rich {
			full = fuelCap(s, o.Name)
		}
		have := o.In("fuel", "coal")
		count(have < full/4, have == 0 && o.Status != "working")
		if have < full*3/4 {
			p.Roles = append(p.Roles, o.Role)
			p.Ns = append(p.Ns, full-have)
			caps = append(caps, fuelCap(s, o.Name)-have)
			need += full - have
		}
	}
	low = empty || lowN > 0 && lowN*5 >= total
	// за один обход — сколько унести (уголь не влезет — бот добирает по 5 бесконечно):
	// самые пустые первыми
	if need > maxRefuelCarry {
		idx := make([]int, len(p.Roles))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return caps[idx[a]] > caps[idx[b]] })
		q := Primitive{Kind: p.Kind, Item: p.Item}
		var qc []int
		need = 0
		for _, i := range idx {
			if need+p.Ns[i] > maxRefuelCarry && len(q.Roles) > 0 {
				continue
			}
			q.Roles, q.Ns, qc = append(q.Roles, p.Roles[i]), append(q.Ns, p.Ns[i]), append(qc, caps[i])
			need += p.Ns[i]
		}
		p, caps = q, qc
	}
	return p, caps, need, low
}

// maxRefuelCarry — угля за один обход дозаправки (16 слотов по 50).
const maxRefuelCarry = 800

// FeedRound — дозаправка одним обходом (исправление «кормушка почти пуста», когда уголь
// есть): все кормушки и печи разом, а не по рейсу на каждую.
type FeedRound struct{}

func (g FeedRound) Key() string    { return "feed-round" }
func (g FeedRound) String() string { return "дозаправить всех обходом" }
func (g FeedRound) Satisfied(s *State) bool {
	_, _, _, low := refuelPlan(s)
	return !low
}
func (g FeedRound) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	return refuelRound(pl, s, c)
}

// startJunk — что остаётся от точек нулевого этапа после разбора и больше не нужно (в
// склад); печи и сундуки идут в кластеры (лишнее — разгрузкой Unload).
var startJunk = []string{"burner-mining-drill"}

// startRole — постройка нулевого этапа: точки старта, ручная плавильня, площадки бурами.
func startRole(role string) bool {
	if _, _, ok := moduleOf(role); ok {
		return true
	}
	return role == "smelter" || strings.HasPrefix(role, "mine-") || strings.HasPrefix(role, "smelt-")
}

// Dismantled — нулевой этап разобран (решение пользователя: как только есть электричество,
// электробуры и плавильные кластеры — угольные буры и печи на руде не нужны): постройки
// подняты и сложены в склад storage-N.
type Dismantled struct{}

func (g Dismantled) Key() string    { return "dismantled" }
func (g Dismantled) String() string { return "разобрать нулевой этап" }
func (g Dismantled) Satisfied(s *State) bool {
	for _, o := range s.Owned {
		if startRole(o.Role) {
			return false
		}
	}
	if _, room := storageRoom(s); room == 0 {
		return true // склада нет или он полон
	}
	for _, it := range startJunk {
		if s.Inventory[it] > 0 {
			return false
		}
	}
	return true
}

func (g Dismantled) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	p := c.prim(Primitive{Kind: "pickup", Item: "нулевой этап", Mark: "dismantled"})
	for _, o := range s.Owned {
		if startRole(o.Role) {
			p.Roles = append(p.Roles, o.Role)
		}
	}
	if len(p.Roles) > 0 {
		return p, nil
	}
	// в склад — где есть место
	for _, it := range startJunk {
		if n := s.Inventory[it]; n > 0 {
			if role, room := storageRoom(s); room > 0 {
				stack := max(1, s.Protos.Items[it].StackSize)
				return c.prim(Primitive{Kind: "insert", Role: role, Item: it, N: min(n, room*stack)}), nil
			}
		}
	}
	return nil, nil
}

// unloadKeep — что бот носит навалом и сколько оставить себе; лишнее при полном
// инвентаре — в склад (крафт с полным инвентарём встаёт).
var unloadKeep = map[string]int{"coal": 600, "stone": 50, "iron-ore": 0, "copper-ore": 0,
	"iron-plate": 800, "copper-plate": 600, "stone-brick": 100, "burner-mining-drill": 0,
	"stone-furnace": 10, "wooden-chest": 10}

// unloadFree — сколько ячеек держать свободными.
const unloadFree = 4

// Unload — в инвентаре есть свободные ячейки: лишнее навалом сложено в склад storage-N.
type Unload struct{}

func (g Unload) Key() string    { return "unload" }
func (g Unload) String() string { return "разгрузить инвентарь в склад" }
func (g Unload) Satisfied(s *State) bool {
	if s.SlotsFree < 0 || s.SlotsFree >= unloadFree || g.pick(s) == "" {
		return true
	}
	_, room := storageRoom(s)
	return room == 0
}

// pick — что выгружать: больше всего лишних ячеек.
func (g Unload) pick(s *State) string {
	best, slots := "", 0
	for it, keep := range unloadKeep {
		stack := max(1, s.Protos.Items[it].StackSize)
		if n := (s.Inventory[it] - keep) / stack; n > slots || n == slots && n > 0 && it < best {
			best, slots = it, n
		}
	}
	return best
}

func (g Unload) next(pl *Planner, s *State, c *ctx) (*Primitive, error) {
	it := g.pick(s)
	if it == "" {
		return nil, nil
	}
	// в ящик склада, где больше места; лишнее сверх запаса (нужное стройке остаётся)
	role, room := storageRoom(s)
	if room == 0 {
		return nil, nil
	}
	stack := max(1, s.Protos.Items[it].StackSize)
	return c.prim(Primitive{Kind: "insert", Role: role, Item: it, N: min(s.Inventory[it]-unloadKeep[it], room*stack)}), nil
}

// storageRoom — ящик склада, где больше свободных ячеек, и сколько их.
func storageRoom(s *State) (role string, room int) {
	for _, r := range s.StorageRoles {
		o := s.Role(r)
		if o == nil {
			continue
		}
		free := chestSlots[o.Name]
		for name, v := range o.Inv["chest"] {
			stack := max(1, s.Protos.Items[name].StackSize)
			free -= (v + stack - 1) / stack
		}
		if free > room {
			role, room = r, free
		}
	}
	return role, room
}

// chestSlots — ячеек в сундуке.
var chestSlots = map[string]int{"wooden-chest": 16, "iron-chest": 32, "steel-chest": 48}
