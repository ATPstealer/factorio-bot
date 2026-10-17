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

// 5 каменных печей с пустого инвентаря: добыть камень 25 → крафт ×5.
func TestFurnacesFromScratch(t *testing.T) {
	_, steps := run(t, newState(t), HaveItem{"stone-furnace", 5})
	if len(steps) != 2 || steps[0].Kind != "gather" || steps[0].N != 25 || steps[1].Kind != "craft" || steps[1].N != 5 {
		t.Errorf("шаги: %s", kinds(steps))
	}
	if r := steps[0].Reason(); !strings.Contains(r, "иметь stone ×25") || !strings.Contains(r, "иметь stone-furnace ×5") {
		t.Errorf("почему: %s", r)
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
	// всё собирается за один заход: камень, крафт, уголь, руда — затем 5 × (поставить, уголь, руда)
	if len(steps) != 4+5*3 {
		t.Errorf("%d шагов, ждали 19: %s", len(steps), kinds(steps))
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
