package world

import (
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
