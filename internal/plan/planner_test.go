package plan

import (
	"strings"
	"sync"
	"testing"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
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
