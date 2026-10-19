package plan

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

var (
	once   sync.Once
	protos *knowledge.Prototypes
)

// newState — пустой инвентарь, ничего не изучено; добыть можно базовое сырьё;
// места под постройки — по порядку вдоль оси X.
func newState(t *testing.T) *State {
	t.Helper()
	once.Do(func() {
		var err error
		protos, err = knowledge.Load("../knowledge/testdata/prototypes.json")
		if err != nil {
			t.Fatal(err)
		}
	})
	minable := map[string]bool{"stone": true, "coal": true, "iron-ore": true, "copper-ore": true, "wood": true}
	s := &State{Protos: protos, Av: protos.NewAvailability(nil, false), Inventory: map[string]int{}}
	s.Minable = func(item string) bool { return minable[item] }
	s.FindSpot = func(s *State, entity, role string, avoid []bridge.Pos) (bridge.Pos, bool) {
		for x := 0.0; x < 100; x += 2 {
			p := bridge.Pos{X: x, Y: 0}
			free := true
			for _, o := range s.Owned {
				free = free && o.Pos != p
			}
			for _, a := range avoid {
				free = free && a != p
			}
			if free {
				return p, true
			}
		}
		return bridge.Pos{}, false
	}
	return s
}

func kinds(steps []*Primitive) string {
	var k []string
	for _, p := range steps {
		k = append(k, p.String())
	}
	return strings.Join(k, "; ")
}

func run(t *testing.T, s *State, goals ...Goal) (*State, []*Primitive) {
	t.Helper()
	pl := NewPlanner()
	steps, err := Simulate(pl, s, goals, 200)
	if err != nil {
		t.Fatalf("%v; шаги: %s", err, kinds(steps))
	}
	end := s.Clone()
	for _, p := range steps {
		if err := Apply(end, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range goals {
		if !g.Satisfied(end) {
			t.Fatalf("цель %s не выполнена после: %s", g, kinds(steps))
		}
	}
	return end, steps
}

// 3 каменные печи с пустого инвентаря: 15 камня — руками (мало для бура) → крафт ×3.
func TestFurnacesFromScratch(t *testing.T) {
	_, steps := run(t, newState(t), HaveItem{"stone-furnace", 3})
	if len(steps) != 2 || steps[0].Kind != "gather" || steps[0].N != 15 || steps[1].Kind != "craft" || steps[1].N != 3 {
		t.Errorf("шаги: %s", kinds(steps))
	}
	if r := steps[0].Reason(); !strings.Contains(r, "иметь stone ×15") || !strings.Contains(r, "иметь stone-furnace ×3") {
		t.Errorf("почему: %s", r)
	}
}

// Много угля — бурами: площадка (бур + сундук + уголь в бур), ждать, забрать.
// Руками — только мелочь (для бура: пластины, уголь на розжиг).
func TestManyCoalUsesDrill(t *testing.T) {
	end, steps := run(t, newState(t), HaveItem{"coal", 60})
	got := kinds(steps)
	for _, want := range []string{"поставить burner-mining-drill", "как mine-coal-1", "поставить wooden-chest", "mine-coal-1-out", "положить coal ×5 в mine-coal-1", "ждать coal", "забрать из mine-coal-1-out"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет «%s»: %s", want, got)
		}
	}
	for _, p := range steps {
		if p.Kind == "gather" && p.N >= DrillThreshold {
			t.Errorf("руками много: %s", p)
		}
	}
	if end.Inventory["coal"] < 60 {
		t.Errorf("угля %d", end.Inventory["coal"])
	}
}

// Бур на угле подкидывает топливо из своего же сундука (уголь в инвентаре кончился).
func TestCoalDrillRefuelsFromOwnChest(t *testing.T) {
	s := newState(t)
	s.Owned = []*Owned{
		{Name: Drill, Role: SiteRole("coal", 1), Pos: bridge.Pos{X: 50}, Inv: map[string]map[string]int{}},
		{Name: Chest, Role: OutRole("coal", 1), Pos: bridge.Pos{X: 52}, Inv: map[string]map[string]int{"chest": {"coal": 30}}},
	}
	p, err := NewPlanner().Next(s, MiningSite{"coal", 1})
	if err != nil || p == nil {
		t.Fatalf("%v %v", p, err)
	}
	if p.Kind != "take" || p.Role != OutRole("coal", 1) {
		t.Errorf("первый шаг %s, ждали «забрать из сундука бура»", p)
	}
}

// Камень уже есть — сразу крафт.
func TestFurnacesWithStone(t *testing.T) {
	s := newState(t)
	s.Inventory["stone"] = 30
	_, steps := run(t, s, HaveItem{"stone-furnace", 5})
	if len(steps) != 1 || steps[0].Kind != "craft" {
		t.Errorf("шаги: %s", kinds(steps))
	}
}

// Камень в сундуке рядом (вне реестра): сначала забрать оттуда, добыть — только остаток.
func TestTakeFromStoreFirst(t *testing.T) {
	s := newState(t)
	pos := bridge.Pos{X: 5, Y: 5}
	s.Stores = []*Owned{{Name: Chest, Pos: pos, Role: StoreRole(pos),
		Inv: map[string]map[string]int{"chest": {"stone": 7}}}}
	_, steps := run(t, s, HaveItem{"stone", 10})
	if len(steps) != 2 || steps[0].Kind != "take" || steps[1].Kind != "gather" || steps[1].N != 3 {
		t.Errorf("шаги: %s", kinds(steps))
	}
}

// Шестерни без пластин: печь (камень → крафт → поставить) → уголь → руда → ждать → забрать → крафт.
func TestGearsNeedSmelting(t *testing.T) {
	end, steps := run(t, newState(t), HaveItem{"iron-gear-wheel", 10})
	var seq []string
	for _, p := range steps {
		seq = append(seq, p.Kind)
	}
	got := strings.Join(seq, ",")
	for _, want := range []string{"place", "insert", "wait", "take"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет шага %s: %s", want, kinds(steps))
		}
	}
	if steps[len(steps)-1].Kind != "craft" || steps[len(steps)-1].Recipe != "iron-gear-wheel" {
		t.Errorf("последний шаг — не крафт шестерён: %s", kinds(steps))
	}
	if end.Role("smelter") == nil {
		t.Error("печь для плавки не поставлена")
	}
}

// Критерий стадии (на симуляторе): линия из 5 печей на железе с углём с нуля.
func TestSmeltingLineFromScratch(t *testing.T) {
	g := SmeltingLine{Ore: "iron-ore", Fuel: "coal", N: 5}
	end, steps := run(t, newState(t), g)
	for i := 1; i <= 5; i++ {
		o := end.Role(LineRole("iron-ore", i))
		if o == nil || o.In("fuel", "coal") < 1 || o.In("crafter_input", "iron-ore") < 1 {
			t.Errorf("печь %d: %+v", i, o)
		}
	}
	// камень, уголь и руда (по 25+) — бурами; руками — ничего крупного
	got := kinds(steps)
	// и с камнем в инвентаре: топливо и руда — всё равно бурами (линия тратит их постоянно)
	for _, site := range []string{"mine-stone-1", "mine-coal-1", "mine-iron-ore-1"} {
		if !strings.Contains(got, "как "+site) {
			t.Errorf("нет площадки %s: %s", site, got)
		}
	}
	for _, p := range steps {
		if p.Kind == "gather" && p.N >= DrillThreshold {
			t.Errorf("руками много: %s", p)
		}
	}
	if len(steps) > 70 {
		t.Errorf("%d шагов — много: %s", len(steps), got)
	}
	seen := map[bridge.Pos]bool{}
	for _, o := range end.Owned {
		if seen[o.Pos] {
			t.Errorf("две печи в %v", o.Pos)
		}
		seen[o.Pos] = true
	}
}

// Восстановление: печь линии пропала — планировщик ставит новую (камень есть — крафт и место).
func TestSmeltingLineRepair(t *testing.T) {
	g := SmeltingLine{Ore: "iron-ore", Fuel: "coal", N: 5}
	end, _ := run(t, newState(t), g)
	// «игрок» убрал печь №3
	var kept []*Owned
	for _, o := range end.Owned {
		if o.Role != LineRole("iron-ore", 3) {
			kept = append(kept, o)
		}
	}
	end.Owned = kept
	if g.Satisfied(end) {
		t.Fatal("линия без печи считается целой")
	}
	_, steps := run(t, end, g)
	if steps[0].Kind != "gather" && steps[0].Kind != "craft" {
		t.Errorf("ремонт начинается с %s", kinds(steps))
	}
	placed := 0
	for _, p := range steps {
		if p.Kind == "place" {
			placed++
			if p.Role != LineRole("iron-ore", 3) {
				t.Errorf("поставлена %s вместо печи №3", p.Role)
			}
		}
	}
	if placed != 1 {
		t.Errorf("поставлено %d печей: %s", placed, kinds(steps))
	}
}

// Отказ постройки — бан места, следующая попытка в другом месте.
func TestPlaceFailureOtherSpot(t *testing.T) {
	s := newState(t)
	s.Inventory["stone-furnace"] = 1
	pl := NewPlanner()
	p1, err := pl.Next(s, Placed{Furnace, "smelter"})
	if err != nil || p1 == nil || p1.Kind != "place" {
		t.Fatalf("%v %v", p1, err)
	}
	if !pl.Failed(p1) {
		t.Error("место не забанено")
	}
	p2, err := pl.Next(s, Placed{Furnace, "smelter"})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Pos == p1.Pos {
		t.Errorf("снова то же место %v", p2.Pos)
	}
}

// Добыча падает трижды — бан: способа нет → ошибка, а не бесконечный цикл; бан истекает.
func TestGatherBanAndExpiry(t *testing.T) {
	s := newState(t)
	pl := NewPlanner()
	now := time.Now()
	pl.Now = func() time.Time { return now }
	g := HaveItem{"coal", 5}
	for i := 0; i < 3; i++ {
		p, err := pl.Next(s, g)
		if err != nil || p.Kind != "gather" {
			t.Fatalf("попытка %d: %v %v", i, p, err)
		}
		pl.Failed(p)
	}
	if _, err := pl.Next(s, g); err == nil {
		t.Error("после бана добычи угля должна быть ошибка «нет способа»")
	}
	now = now.Add(6 * time.Minute)
	if p, err := pl.Next(s, g); err != nil || p == nil || p.Kind != "gather" {
		t.Errorf("после истечения бана: %v %v", p, err)
	}
}

// Цель выполнилась извне посреди плана — шагов больше нет.
func TestSatisfiedExternally(t *testing.T) {
	s := newState(t)
	pl := NewPlanner()
	g := HaveItem{"stone-furnace", 2}
	if p, _ := pl.Next(s, g); p == nil || p.Kind != "gather" {
		t.Fatalf("первый шаг: %v", p)
	}
	s.Inventory["stone-furnace"] = 2 // кто-то положил
	if p, err := pl.Next(s, g); p != nil || err != nil {
		t.Errorf("лишний шаг %v (%v)", p, err)
	}
}

// Недостижимое (уран без кислоты и не добывается) — ошибка с объяснением, быстро.
func TestUnreachable(t *testing.T) {
	_, err := NewPlanner().Next(newState(t), HaveItem{"uranium-ore", 1})
	if err == nil || !strings.Contains(err.Error(), "uranium-ore") {
		t.Errorf("err = %v", err)
	}
}

// Ресурса нет на открытой карте — сначала разведка, потом добыча.
func TestExploreWhenNotVisible(t *testing.T) {
	s := newState(t)
	s.Minable = func(item string) bool { return item != "copper-ore" }
	end, steps := run(t, s, HaveItem{"copper-ore", 10})
	if len(steps) != 2 || steps[0].Kind != "explore" || steps[1].Kind != "gather" {
		t.Errorf("шаги: %s", kinds(steps))
	}
	if end.Inventory["copper-ore"] != 10 {
		t.Errorf("медь %d", end.Inventory["copper-ore"])
	}
}

// Камень и уголь уже есть (попутно с камней) — бур на угле и руде всё равно ставится.
func TestLineAlwaysDrillsFuelAndOre(t *testing.T) {
	s := newState(t)
	s.Inventory["stone"] = 100
	s.Inventory["coal"] = 100
	_, steps := run(t, s, SmeltingLine{Ore: "iron-ore", Fuel: "coal", N: 5})
	got := kinds(steps)
	for _, site := range []string{"как mine-coal-1", "как mine-iron-ore-1"} {
		if !strings.Contains(got, site) {
			t.Errorf("нет %s: %s", site, got)
		}
	}
	if strings.Contains(got, "как mine-stone-1") {
		t.Errorf("камня хватает, а бур на камень ставится: %s", got)
	}
}

// Раскладка с пустого инвентаря: материалы → постройка слоями (печи → инсертеры → ленты),
// подземки — парами «вход, выход».
func TestBuildLayout(t *testing.T) {
	s := newState(t)
	s.Inventory["underground-belt"] = 2 // до logistics не скрафтить
	l := layout.New(s.Protos, nil)
	l.SmeltingColumn(world.Tile{X: 10, Y: 10}, 2, "stone-furnace", "burner-inserter", "smelt")
	l.Add(layout.Placement{Name: "underground-belt", Pos: bridge.Pos{X: 30.5, Y: 0.5}, Dir: layout.E, Recipe: "output"})
	l.Add(layout.Placement{Name: "underground-belt", Pos: bridge.Pos{X: 26.5, Y: 0.5}, Dir: layout.E, Recipe: "input"})
	g := Built{Tag: "col", List: l.List}
	end, steps := run(t, s, g)
	var order []string
	for _, p := range steps {
		if p.Kind == "build" {
			order = append(order, fmt.Sprintf("%s×%d", p.Item, len(p.Spots)))
			if p.Item == "underground-belt" && p.Spots[0].X != 26.5 {
				t.Errorf("подземки не с входа: %v", p.Spots)
			}
		}
	}
	want := "stone-furnace×2 burner-inserter×4 transport-belt×8 underground-belt×2"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("порядок стройки %q, ждали %q", got, want)
	}
	if len(end.Owned) < len(l.List) {
		t.Errorf("в реестре %d из %d", len(end.Owned), len(l.List))
	}
}

// Помпа и столб с нуля: рецепты закрыты trigger-технологиями 2.0 (steam-power — 50
// железных пластин, electronics — 10 медных) — бот плавит до открытия и крафтит.
func TestTriggerTechUnlocksRecipe(t *testing.T) {
	end, steps := run(t, newState(t), HaveItem{"offshore-pump", 1}, HaveItem{"small-electric-pole", 1})
	for _, tech := range []string{"steam-power", "electronics"} {
		if !end.Av.Researched[tech] {
			t.Errorf("%s не изучена", tech)
		}
	}
	if end.Produced["iron-plate"] < 50 {
		t.Errorf("железа выплавлено %v; шаги: %s", end.Produced["iron-plate"], kinds(steps))
	}
}

// automation с нуля: trigger-технологии (пластины, лаборатория), место с лабораторией,
// наука руками в лабораторию, запуск, ожидание.
func TestResearchByScience(t *testing.T) {
	s := newState(t)
	l := layout.New(s.Protos, nil)
	l.Add(layout.Placement{Name: "lab", Pos: bridge.Pos{X: 50.5, Y: 50.5}})
	s.Design = func(s *State, kind string) (Built, bool) { return Built{Tag: kind, List: l.List}, true }
	end, steps := run(t, s, Researched{Tech: "automation"})
	k := kinds(steps)
	for _, want := range []string{"скрафтить lab", "построить lab", "положить automation-science-pack ×10 в research-1", "запустить исследование automation", "ждать исследования automation"} {
		if !strings.Contains(k, want) {
			t.Errorf("нет шага %q", want)
		}
	}
	if !end.Av.Recipe("assembling-machine-1") {
		t.Error("сборщик не открыт")
	}
}

// Заправка 6 burner-построек (30 угля): уголь бурами — немного руками в бур, остальное из
// сундука бура; уголь для бура не уходит в постройки, руками ≤ порога.
func TestFuelLayoutViaCoalDrill(t *testing.T) {
	s := newState(t)
	l := layout.New(s.Protos, nil)
	for i := 0; i < 6; i++ {
		l.Add(layout.Placement{Name: "stone-furnace", Pos: bridge.Pos{X: float64(10 + 3*i), Y: 10}})
	}
	s.Inventory["stone-furnace"] = 6
	_, steps := run(t, s, Built{Tag: "f", List: l.List})
	hand := 0
	for i, p := range steps {
		if p.Kind == "gather" && p.Item == "coal" {
			hand += p.N
		}
		if p.Kind == "insert" && p.Item == "coal" && strings.HasPrefix(p.Role, "f-") && i > 0 {
			for _, q := range steps[i:] {
				if q.Kind == "insert" && q.Role == SiteRole("coal", 1) {
					t.Errorf("уголь в постройки раньше, чем в бур: %s", kinds(steps))
					return
				}
			}
		}
	}
	if hand >= DrillThreshold {
		t.Errorf("руками %d угля: %s", hand, kinds(steps))
	}
	if !strings.Contains(kinds(steps), "забрать из "+OutRole("coal", 1)) {
		t.Errorf("не забирал из сундука бура: %s", kinds(steps))
	}
}

// В печи медь (и железо в выходе), нужно железо: дождаться меди, забрать, потом своё.
func TestSmelterBusyWithOtherOre(t *testing.T) {
	s := newState(t)
	pos := bridge.Pos{X: 1, Y: 1}
	s.Owned = []*Owned{{Name: Furnace, Pos: pos, Role: "smelter", Inv: map[string]map[string]int{
		"fuel": {"coal": 5}, "crafter_input": {"copper-ore": 10}, "crafter_output": {"iron-plate": 1}}}}
	_, steps := run(t, s, HaveItem{"iron-plate", 5})
	k := kinds(steps)
	if !strings.HasPrefix(k, "забрать из smelter; ждать copper-plate ×10 в smelter; забрать из smelter") {
		t.Errorf("шаги: %s", k)
	}
}

// Кирпич — 2 камня: в печи 1 камень, не хватает 1 кирпича — доложить камень, а не ждать.
func TestSmelterRatio(t *testing.T) {
	s := newState(t)
	s.Inventory["stone"] = 100
	s.Inventory["stone-brick"] = 9
	s.Owned = []*Owned{{Name: Furnace, Pos: bridge.Pos{X: 1, Y: 1}, Role: "smelter", Inv: map[string]map[string]int{
		"fuel": {"coal": 5}, "crafter_input": {"stone": 1}}}}
	p, err := NewPlanner().Next(s, HaveItem{"stone-brick", 10})
	if err != nil || p == nil || p.Kind != "insert" || p.Item != "stone" {
		t.Errorf("шаг: %v %v", p, err)
	}
}

// Лаборатория без питания, но не подключена к сети (станция ещё строится) — не нехватка
// мощности: блок станции не строить.
func TestDiagnoseUnconnected(t *testing.T) {
	s := newState(t)
	s.Owned = []*Owned{
		{Name: "boiler", Role: "research-2", Status: "working", Inv: map[string]map[string]int{"fuel": {"coal": 5}}},
		{Name: "lab", Role: "research-6", Status: "no_power", Inv: map[string]map[string]int{}},
		{Name: "steam-engine", Role: "research-3", Status: "working", Network: 1, Inv: map[string]map[string]int{}},
		{Name: "lab", Role: "research-7", Status: "no_power", Network: 7, Inv: map[string]map[string]int{}}, // своя сеть без двигателя
	}
	for _, p := range Diagnose(s) {
		if p.Key == "power" || p.Fix != nil {
			t.Errorf("проблема %+v", p)
		}
	}
}

// startDesign — раскладки точек старта для симуляции (геометрия не важна).
func startDesign(s *State, kind string) (Built, bool) {
	ore, ok := ModuleOre(kind)
	if !ok {
		return Built{}, false
	}
	x := float64(len(kind)*7 + int(kind[len(kind)-1])*5)
	d := func(dx float64) layout.Placement {
		return layout.Placement{Name: Drill, Pos: bridge.Pos{X: x + dx, Y: 50}}
	}
	var l []layout.Placement
	switch ore {
	case "coal":
		l = []layout.Placement{d(0), d(2)}
	case "stone":
		l = []layout.Placement{d(0), d(2), d(4), d(6), {Name: Chest, Pos: bridge.Pos{X: x, Y: 53.5}}}
	default:
		l = []layout.Placement{d(0), {Name: Furnace, Pos: bridge.Pos{X: x, Y: 48}}}
	}
	return Built{Tag: kind, List: l}, true
}

// Старт: дерево и камни — руками, потом точки; руду руками — только на первую заправку.
func TestStart(t *testing.T) {
	s := newState(t)
	s.Design = startDesign
	end, steps := run(t, s, Start{}, HaveItem{"iron-plate", 100})
	hand := 0
	for _, p := range steps {
		if p.Kind == "gather" && p.Source == "" {
			hand += p.N
		}
	}
	if hand > 80 { // первые буры — на руду и уголь, добытые руками (точек ещё нет)
		t.Errorf("руками добыто %d: %s", hand, kinds(steps))
	}
	built := 0
	for _, o := range end.Owned {
		if _, _, ok := moduleOf(o.Role); ok {
			built++
		}
	}
	t.Logf("шагов %d, построек точек %d, руками %d", len(steps), built, hand)
}

// Заправка с запасом: угля в инвентаре много — сверх минимума поровну, до полного слота.
func TestRefuelWithSurplus(t *testing.T) {
	for _, tc := range []struct{ inv, want int }{{20, 10}, {30, 15}, {200, 50}} {
		s := newState(t)
		s.Inventory["coal"] = tc.inv
		for i := 1; i <= 2; i++ {
			s.Owned = append(s.Owned, &Owned{Name: Drill, Role: fmt.Sprintf("iron-ore-%d-1", i), Inv: map[string]map[string]int{}})
		}
		p, err := refuelRound(NewPlanner(), s, &ctx{stack: map[string]bool{}})
		if err != nil || p == nil || p.Kind != "refuel" {
			t.Fatalf("уголь %d: %v %v", tc.inv, p, err)
		}
		if p.Ns[0] != tc.want || p.Ns[1] != tc.want {
			t.Errorf("уголь %d: кладём %v, ждали по %d", tc.inv, p.Ns, tc.want)
		}
	}
}

// Ожидание против реальности: пропало питание — лаборатория без питания, бойлер пуст →
// срочная проблема, первым делом уголь в бойлер.
func TestDiagnosePowerOutage(t *testing.T) {
	s := newState(t)
	s.Inventory["coal"] = 50
	s.Owned = []*Owned{
		{Name: "boiler", Role: "research-2", Status: "no_fuel", Inv: map[string]map[string]int{}},
		{Name: "lab", Role: "research-6", Status: "no_power", Network: 1, Inv: map[string]map[string]int{}},
	}
	ps := Diagnose(s)
	if len(ps) == 0 || ps[0].Key != "power" || !ps[0].Urgent || !strings.Contains(ps[0].Cause, "research-2 без топлива") {
		t.Fatalf("проблемы: %+v", ps)
	}
	p, err := NewPlanner().Next(s, Repair{})
	if err != nil || p == nil || p.Kind != "insert" || p.Role != "research-2" || p.Item != "coal" {
		t.Errorf("первый шаг ремонта: %v %v", p, err)
	}
}

// Лаборатории ждут науку при идущем исследовании — причина прослеживается по цепочке.
func TestDiagnoseLabsIdle(t *testing.T) {
	s := newState(t)
	s.Researching = map[string]bool{"logistics": true}
	s.Owned = []*Owned{
		{Name: "lab", Role: "science-40", Status: "missing_science_packs", Inv: map[string]map[string]int{}},
		{Name: "assembling-machine-1", Role: "science-17", Status: "item_ingredient_shortage", Inv: map[string]map[string]int{}},
		{Name: Furnace, Role: "science-5", Status: "no_fuel", Inv: map[string]map[string]int{}},
	}
	var idle *Problem
	for _, p := range Diagnose(s) {
		if p.Key == "labs-idle" {
			idle = &p
		}
	}
	if idle == nil || !strings.Contains(idle.Cause, "science-17: нет ингредиентов") || !strings.Contains(idle.Cause, "science-5: нет топлива") {
		t.Errorf("простой лабораторий: %+v", idle)
	}
}

// Мощности заранее: спрос раскладок (8 сборщиков ×75 кВт и т.п.) больше выработки — сначала
// станция; блоки растут геометрически (1, 2, 4 цепочки).
func TestPowerPlanning(t *testing.T) {
	s := newState(t)
	var factory []layout.Placement
	for i := 0; i < 30; i++ {
		factory = append(factory, layout.Placement{Name: "assembling-machine-1", Pos: bridge.Pos{X: float64(4 * i), Y: 0}})
	}
	station := []layout.Placement{{Name: "steam-engine"}, {Name: "steam-engine"}}
	designs := map[string][]layout.Placement{"science": factory, "research": station}
	s.Designs = func() [][]layout.Placement {
		var res [][]layout.Placement
		for _, l := range designs {
			res = append(res, l)
		}
		return res
	}
	s.Design = func(s *State, kind string) (Built, bool) {
		l, ok := designs[kind]
		return Built{Tag: kind, List: l}, ok
	}
	// 30 × 75 кВт ≈ 2.3 МВт (+ простой) против 1.8 МВт — строить power-1
	if k := powerShort(s, nil); k != "power-1" {
		t.Errorf("при нехватке: %q", k)
	}
	if PowerChains(1) != 1 || PowerChains(2) != 2 || PowerChains(3) != 4 {
		t.Error("блоки не удваиваются")
	}
	designs["research"] = append(station, station...) // 3.6 МВт — хватает с запасом
	if k := powerShort(s, nil); k != "" {
		t.Errorf("хватает, а строит %q", k)
	}
}

// Лента с поворотами — один шаг стройки на всю трассу (свои направления у мест), а не по
// проходу на каждое направление.
func TestBeltOnePass(t *testing.T) {
	s := newState(t)
	var list []layout.Placement
	for i := 0; i < 4; i++ {
		list = append(list, layout.Placement{Name: "transport-belt", Pos: bridge.Pos{X: 10.5 + float64(i), Y: 10.5}, Dir: layout.E})
	}
	for i := 0; i < 4; i++ {
		list = append(list, layout.Placement{Name: "transport-belt", Pos: bridge.Pos{X: 14.5, Y: 10.5 + float64(i)}, Dir: layout.S})
	}
	s.Inventory["transport-belt"] = len(list)
	_, steps := run(t, s, Built{Tag: "b", List: list})
	builds := 0
	for _, p := range steps {
		if p.Kind == "build" {
			builds++
			if len(p.Dirs) != len(list) {
				t.Errorf("направления: %v", p.Dirs)
			}
		}
	}
	if builds != 1 {
		t.Errorf("шагов стройки %d: %s", builds, kinds(steps))
	}
	for _, o := range s.Owned {
		if o.Name == "transport-belt" && o.Pos.X == 14.5 && o.Pos.Y > 11 && *o.Dir != layout.S {
			t.Errorf("%v смотрит %d", o.Pos, *o.Dir)
		}
	}
}
