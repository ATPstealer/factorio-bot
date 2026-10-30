package act

import (
	"math"
	"reflect"
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
)

func TestWalkKeys(t *testing.T) {
	o := bridge.Pos{}
	cases := []struct {
		to   bridge.Pos
		want []string
	}{
		{bridge.Pos{X: 5}, []string{"d"}},
		{bridge.Pos{X: -5, Y: -5}, []string{"a", "w"}},
		{bridge.Pos{X: 0.2, Y: 3}, []string{"s"}},
		{bridge.Pos{X: 0.1, Y: -0.1}, nil},
	}
	for _, c := range cases {
		got := walkKeys(o, c.to)
		want := map[string]bool{}
		for _, k := range c.want {
			want[k] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("walkKeys(%v) = %v, ждали %v", c.to, got, want)
		}
	}
}

func TestSidestepPerpendicular(t *testing.T) {
	if s := sidestep(map[string]bool{"d": true}, false); !s["w"] || len(s) != 1 {
		t.Errorf("вправо → %v", s)
	}
	if s := sidestep(map[string]bool{"s": true}, true); !s["d"] || len(s) != 1 {
		t.Errorf("вниз → %v", s)
	}
}

func TestHoverPoints(t *testing.T) {
	e := bridge.Entity{Position: bridge.Pos{X: 5, Y: 5}}
	if pts := hoverPoints(e); len(pts) != 1 || pts[0] != e.Position {
		t.Fatalf("без рамки: %v", pts)
	}
	e.SelectionBox = &bridge.Box{LeftTop: bridge.Pos{X: 4, Y: 3}, RightBottom: bridge.Pos{X: 6, Y: 5}}
	pts := hoverPoints(e)
	if pts[0] != (bridge.Pos{X: 5, Y: 4}) {
		t.Errorf("первая точка — центр рамки, а не %v", pts[0])
	}
	for i, p := range pts {
		if p.X <= 4 || p.X >= 6 || p.Y <= 3 || p.Y >= 5 {
			t.Errorf("точка %v вне рамки", p)
		}
		if i > 1 && dist(p, pts[0]) < dist(pts[i-1], pts[0]) {
			t.Errorf("точки не по удалению от центра: %v", pts)
			break
		}
	}
}

func TestClosestOnBox(t *testing.T) {
	b := bridge.Box{LeftTop: bridge.Pos{X: -2, Y: -1}, RightBottom: bridge.Pos{X: 2, Y: 1}}
	cases := map[bridge.Pos]bridge.Pos{
		{X: 10, Y: 0}:  {X: 2, Y: 0},   // справа — на правую грань
		{X: -5, Y: -5}: {X: -2, Y: -1}, // по диагонали — в угол
		{X: 0, Y: 3}:   {X: 0, Y: 1},   // снизу
		{X: 1, Y: 0}:   {X: 1, Y: 0},   // внутри — сама точка
	}
	for p, want := range cases {
		if got := closestOnBox(p, b); got != want {
			t.Errorf("closestOnBox(%v) = %v, ждали %v", p, got, want)
		}
	}
}

// TestCollisionBoxRotated — рамка коллизии прямоугольной постройки по направлению: делитель
// (2×1 при «север») на восток — 1 по X и 2 по Y; паровой двигатель (3×5 при «север») на
// восток — 5 по X и 3 по Y. Рамка поворачивалась дважды — выходила поперёк настоящей.
func TestCollisionBoxRotated(t *testing.T) {
	p, err := knowledge.Load("../knowledge/testdata/prototypes.json")
	if err != nil {
		t.Fatal(err)
	}
	h := &Hands{Protos: p}
	cases := []struct {
		name string
		dir  int
		w, h float64 // ширина по X и высота по Y, тайлов (с округлением)
	}{
		{"splitter", North, 2, 1},
		{"splitter", East, 1, 2},
		{"splitter", South, 2, 1},
		{"splitter", West, 1, 2},
		{"steam-engine", North, 3, 5},
		{"steam-engine", East, 5, 3},
	}
	for _, c := range cases {
		d := c.dir
		b := h.collisionBox(bridge.Entity{Name: c.name, Position: bridge.Pos{}, Direction: &d})
		if b == nil {
			t.Fatalf("%s: нет рамки", c.name)
		}
		w, hh := math.Round(b.RightBottom.X-b.LeftTop.X), math.Round(b.RightBottom.Y-b.LeftTop.Y)
		if w != c.w || hh != c.h {
			t.Errorf("%s на %d: рамка %v×%v, ждали %v×%v", c.name, c.dir, w, hh, c.w, c.h)
		}
	}
}

// TestCraftSearchOneTab — слово поиска крафта: совпадения только на одной вкладке (в 2.1 поиск
// остаётся на текущей вкладке: iron-chest по «iron» — клик по iron-plate промежуточных).
func TestCraftSearchOneTab(t *testing.T) {
	p, err := knowledge.Load("../knowledge/testdata/prototypes.json")
	if err != nil {
		t.Fatal(err)
	}
	h := &Hands{Protos: p}
	avail := []string{"iron-chest", "wooden-chest", "iron-gear-wheel", "iron-plate", "copper-plate", "transport-belt", "inserter"}
	all := func(knowledge.Recipe) bool { return true }
	if _, _, err := craftSlot(p, avail, all, "iron-chest", "iron"); err == nil {
		t.Error("«iron» — совпадения на двух вкладках, а ошибки нет")
	}
	text := h.searchWord("iron-chest", func(w string) bool {
		_, _, err := craftSlot(p, avail, all, "iron-chest", w)
		return err == nil
	})
	if _, _, err := craftSlot(p, avail, all, "iron-chest", text); err != nil {
		t.Errorf("слово %q: %v", text, err)
	}
}

// Совпадения на разных вкладках при любом слове (насос «pump»: ещё помпа и качалка на
// «производстве», стадия 34) — сначала слово только с вкладки насоса, потом «pump» на ней.
func TestCraftSearchOtherTab(t *testing.T) {
	p, err := knowledge.Load("../knowledge/testdata/prototypes.json")
	if err != nil {
		t.Fatal(err)
	}
	h := &Hands{Protos: p}
	avail := []string{"pump", "offshore-pump", "pumpjack", "transport-belt", "inserter", "pipe", "iron-gear-wheel"}
	all := func(knowledge.Recipe) bool { return true }
	if _, _, err := craftSlot(p, avail, all, "pump", "pump"); err == nil {
		t.Fatal("«pump» — совпадения на двух вкладках, а ошибки нет")
	}
	g := p.Recipes["pump"].Group
	w := h.tabWord(avail, all, g)
	if w == "" {
		t.Fatalf("нет слова только с вкладки %s", g)
	}
	if _, _, err := craftSlotIn(p, avail, all, "pump", "pump", g); err != nil {
		t.Errorf("насос на вкладке %s: %v", g, err)
	}
	t.Logf("вкладка %s, слово %q", g, w)
}

// TestUgOrder — подземки ставятся по ходу ленты: список «выход, вход» (трасса от конца к
// началу) — сначала вход; группы направлений — в порядке списка.
func TestUgOrder(t *testing.T) {
	pos := []bridge.Pos{{X: 271.5, Y: 523.5}, {X: 276.5, Y: 523.5}, {X: 266, Y: 512}, {X: 266, Y: 518}, {X: 280, Y: 523.5}}
	dirs := []int{12, 12, 0, 0, 12}
	got := ugOrder(pos, dirs)
	want := []int{4, 1, 0, 3, 2} // запад: 280 → 276.5 → 271.5; север: y 518 → 512
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок %v, ждали %v", got, want)
		}
	}
}
