package world

import (
	"math"
	"strings"
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
)

// chunk00 — чанк (0,0): вода в первой строке, руда и объекты по координатам.
func chunk00(res []bridge.ScanResource, ents []bridge.ScanEntity) bridge.ChunkScan {
	tiles := strings.Repeat("w", ChunkSize) + strings.Repeat(".", ChunkSize*(ChunkSize-1))
	return bridge.ChunkScan{X: 0, Y: 0, Charted: true, Tiles: tiles, Resources: res, Entities: ents}
}

func ore(name string, x, y, a int) bridge.ScanResource {
	return bridge.ScanResource{Name: name, X: float64(x) + 0.5, Y: float64(y) + 0.5, Amount: a}
}

func TestTilesAndUnknown(t *testing.T) {
	w := New(nil)
	w.Apply([]bridge.ChunkScan{chunk00(nil, nil)})
	if c := w.TileAt(Tile{5, 0}); c != Water {
		t.Errorf("(5,0) = %c", c)
	}
	if c := w.TileAt(Tile{5, 1}); c != Land {
		t.Errorf("(5,1) = %c", c)
	}
	if c := w.TileAt(Tile{-1, 5}); c != Unknown {
		t.Errorf("неоткрытый тайл = %c", c)
	}
	if w.Walkable(Tile{5, 0}) || !w.Walkable(Tile{5, 1}) || w.Walkable(Tile{40, 5}) {
		t.Error("проходимость: вода/суша/неоткрытое")
	}
	// повторное сканирование чанка как неоткрытого удаляет его
	w.Apply([]bridge.ChunkScan{{X: 0, Y: 0, Charted: false}})
	if len(w.Chunks) != 0 {
		t.Error("неоткрытый чанк остался")
	}
}

func TestChunkOfNegative(t *testing.T) {
	cases := map[Tile]bridge.ChunkPos{{0, 0}: {X: 0, Y: 0}, {31, 31}: {X: 0, Y: 0}, {32, -1}: {X: 1, Y: -1}, {-32, -33}: {X: -1, Y: -2}}
	for tl, want := range cases {
		if got := ChunkOf(tl); got != want {
			t.Errorf("ChunkOf(%v) = %v, ждали %v", tl, got, want)
		}
	}
}

func TestPatches(t *testing.T) {
	w := New(nil)
	w.Apply([]bridge.ChunkScan{chunk00([]bridge.ScanResource{
		// уголь: 2×2 + диагональный сосед — одно месторождение
		ore("coal", 2, 2, 100), ore("coal", 3, 2, 100), ore("coal", 2, 3, 100), ore("coal", 3, 3, 100), ore("coal", 4, 4, 50),
		// отдельное железо и уголь, не касающиеся первого
		ore("iron-ore", 10, 10, 1000),
		ore("coal", 20, 20, 7),
		// железо, касающееся угля, — другое месторождение (другой ресурс)
		ore("iron-ore", 5, 5, 1),
		// нефть россыпью — одно месторождение
		ore("crude-oil", 10, 25, 300), ore("crude-oil", 16, 28, 300),
	}, nil)})
	ps := w.Patches()
	got := map[string][]int{}
	for _, p := range ps {
		got[p.Name] = append(got[p.Name], p.Amount)
	}
	if len(got["coal"]) != 2 || got["coal"][0] != 450 || got["coal"][1] != 7 {
		t.Errorf("уголь: %v", got["coal"])
	}
	if len(got["iron-ore"]) != 2 {
		t.Errorf("железо: %v", got["iron-ore"])
	}
	if len(got["crude-oil"]) != 1 || got["crude-oil"][0] != 600 {
		t.Errorf("нефть: %v", got["crude-oil"])
	}
	if ps[0].Name != "iron-ore" || ps[0].Amount != 1000 {
		t.Errorf("первым — самое большое: %+v", ps[0])
	}
	for _, p := range ps {
		if p.Name == "coal" && p.Amount == 450 {
			if p.Min != (Tile{2, 2}) || p.Max != (Tile{4, 4}) || len(p.Tiles) != 5 {
				t.Errorf("границы угля: %+v", p)
			}
			if d := p.Distance(bridge.Pos{X: 2.5, Y: 0.5}); d != 2 {
				t.Errorf("Distance = %v", d)
			}
		}
	}
}

func TestBlockedByCollisionBox(t *testing.T) {
	protos := &knowledge.Prototypes{Entities: map[string]knowledge.Entity{
		// 3×2 рамка, как у некоторых камней
		"big": {CollisionBox: bridge.Box{LeftTop: bridge.Pos{X: -1.4, Y: -0.9}, RightBottom: bridge.Pos{X: 1.4, Y: 0.9}}},
	}}
	w := New(protos)
	w.Apply([]bridge.ChunkScan{chunk00(nil, []bridge.ScanEntity{
		{Name: "big", Type: "simple-entity", X: 10.5, Y: 10},
		{Name: "fish", Type: "fish", X: 3.5, Y: 0.5},
	})})
	for _, tl := range []Tile{{9, 9}, {10, 9}, {11, 9}, {9, 10}, {11, 10}} {
		if !w.Blocked(tl) {
			t.Errorf("%v должен быть занят", tl)
		}
	}
	for _, tl := range []Tile{{8, 10}, {12, 10}, {10, 8}, {10, 11}, {3, 0}} {
		if w.Blocked(tl) {
			t.Errorf("%v должен быть свободен", tl)
		}
	}

	// поворот на восток: 3×2 → 2×3
	w2 := New(protos)
	dir := 4
	w2.Apply([]bridge.ChunkScan{chunk00(nil, []bridge.ScanEntity{{Name: "big", Type: "x", X: 10, Y: 10.5, Direction: &dir}})})
	if !w2.Blocked(Tile{9, 9}) || !w2.Blocked(Tile{10, 11}) || w2.Blocked(Tile{11, 10}) {
		t.Error("повёрнутая рамка")
	}
}

func TestRenderColors(t *testing.T) {
	w := New(nil)
	w.Apply([]bridge.ChunkScan{chunk00([]bridge.ScanResource{ore("coal", 5, 5, 10)},
		[]bridge.ScanEntity{{Name: "tree-01", Type: "tree", X: 8.5, Y: 8.5}})})
	p := bridge.Pos{X: 20.5, Y: 20.5}
	img := w.Render(RenderOptions{Center: Tile{16, 16}, Radius: 20, Scale: 2, Player: &p})
	at := func(tl Tile) [4]uint8 {
		c := img.RGBAAt((tl.X-(16-20))*2, (tl.Y-(16-20))*2)
		return [4]uint8{c.R, c.G, c.B, c.A}
	}
	want := map[Tile][4]uint8{
		{5, 5}:   rgba(ResourceColors["coal"]),
		{3, 0}:   rgba(ColorWater),
		{3, 3}:   rgba(ColorLand),
		{8, 8}:   rgba(ColorTree),
		{-2, 5}:  rgba(ColorUnknown),
		{20, 20}: rgba(ColorPlayer),
	}
	for tl, c := range want {
		if got := at(tl); got != c {
			t.Errorf("пиксель тайла %v = %v, ждали %v", tl, got, c)
		}
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 80 {
		t.Errorf("размер %v", b)
	}
}

func rgba(c interface{ RGBA() (r, g, b, a uint32) }) [4]uint8 {
	r, g, b, a := c.RGBA()
	return [4]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

// Утёс: рамка из игры (зависит от ориентации) важнее рамки справочника.
func TestCliffBox(t *testing.T) {
	w := New(nil)
	box := &bridge.Box{LeftTop: bridge.Pos{X: 4, Y: 10}, RightBottom: bridge.Pos{X: 8, Y: 11.5}}
	w.Apply([]bridge.ChunkScan{chunk00(nil, []bridge.ScanEntity{{Name: "cliff", Type: "cliff", X: 6, Y: 10.75, Box: box}})})
	for x := 4; x < 8; x++ {
		if !w.Blocked(Tile{x, 10}) || !w.Blocked(Tile{x, 11}) {
			t.Errorf("тайл (%d, 10..11) под утёсом свободен", x)
		}
	}
	if w.Blocked(Tile{8, 10}) || w.Blocked(Tile{3, 10}) || w.TreeOnly(Tile{5, 10}) {
		t.Error("рамка утёса шире, чем в игре, или утёс считается деревом")
	}
}

// Кольцо своих труб: изнутри наружу пути нет; без одной трубы — есть.
func TestWithoutOpensRing(t *testing.T) {
	w := New(nil)
	var ents []bridge.ScanEntity
	for i := 5; i <= 15; i++ {
		for _, p := range [][2]int{{i, 5}, {i, 15}, {5, i}, {15, i}} {
			ents = append(ents, bridge.ScanEntity{Name: "pipe", Type: "pipe", X: float64(p[0]) + 0.5, Y: float64(p[1]) + 0.5, Own: true})
		}
	}
	w.Apply([]bridge.ChunkScan{chunk00(nil, ents)})
	in, out := bridge.Pos{X: 10.5, Y: 10.5}, bridge.Pos{X: 25.5, Y: 10.5}
	if _, err := w.Route(in, out, 0.5, 0, 0); err == nil {
		t.Fatal("из кольца есть путь")
	}
	var o Object
	for _, x := range w.ObjectsNear(bridge.Pos{X: 15.5, Y: 10.5}, 0.1) {
		o = x
	}
	ok := false
	w.Without(o, func() { _, err := w.Route(in, out, 0.5, 0, 0); ok = err == nil })
	if !ok {
		t.Error("без трубы пути нет")
	}
	if _, err := w.Route(in, out, 0.5, 0, 0); err == nil {
		t.Error("труба не вернулась")
	}
}

// Вдоль ленты не идти (сносит; между бурами из коридора не выйти) — рядом по земле.
func TestRouteAvoidsBeltAlong(t *testing.T) {
	w := New(nil)
	var ents []bridge.ScanEntity
	for x := 3; x <= 20; x++ {
		ents = append(ents, bridge.ScanEntity{Name: "transport-belt", Type: "transport-belt", X: float64(x) + 0.5, Y: 10.5, Own: true})
	}
	w.Apply([]bridge.ChunkScan{chunk00(nil, ents)})
	r, err := w.Route(bridge.Pos{X: 2.5, Y: 10.5}, bridge.Pos{X: 21.5, Y: 10.5}, 0.5, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	on := 0
	for i := 1; i < len(r.Points); i++ { // точки по отрезкам маршрута через 0.25 тайла
		a, b := r.Points[i-1], r.Points[i]
		n := int(math.Hypot(b.X-a.X, b.Y-a.Y)/0.25) + 1
		for k := 0; k <= n; k++ {
			x, y := a.X+(b.X-a.X)*float64(k)/float64(n), a.Y+(b.Y-a.Y)*float64(k)/float64(n)
			if y > 10 && y < 11 && x > 6 && x < 19 {
				on++
			}
		}
	}
	if on > 0 {
		t.Errorf("путь вдоль ленты: %v", r.Points)
	}
}

func TestSaveLoad(t *testing.T) {
	w := New(nil)
	box := &bridge.Box{}
	w.Apply([]bridge.ChunkScan{chunk00([]bridge.ScanResource{ore("coal", 3, 4, 500)},
		[]bridge.ScanEntity{{Name: "cliff", Type: "cliff", X: 7, Y: 8, Box: box}})})
	path := t.TempDir() + "/w.gob"
	if err := w.Save(path); err != nil {
		t.Fatal(err)
	}
	v := New(nil)
	if err := v.Load(path); err != nil {
		t.Fatal(err)
	}
	if r, ok := v.ResourceAt(Tile{3, 4}); !ok || r.Amount != 500 || v.TileAt(Tile{5, 0}) != Water {
		t.Errorf("после загрузки: руда %v %v, тайл %c", r, ok, v.TileAt(Tile{5, 0}))
	}
	if len(v.Objects()) != 1 || v.Objects()[0].Box == nil {
		t.Errorf("объекты: %v", v.Objects())
	}
}
