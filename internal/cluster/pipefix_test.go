package cluster

import (
	"strings"
	"testing"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Подземная пара на тайл длиннее дальности (концы x 0 и 11, под ней — ленты x 1–10): найдена и
// заменена обходом; после — длинных пар нет, течение трубы — от x −1 до x 12.
func TestFixLongPipes(t *testing.T) {
	p := pt(t)
	l := layout.New(p, nil)
	const tag = "c/bus-2/sulfuric-acid"
	at := func(x, y int) world.Tile { return world.Tile{X: x, Y: y} }
	l.Add(layout.Placement{Name: "pipe", Pos: at(-1, 0).Center(), Tag: tag})
	l.Add(layout.Placement{Name: "pipe-to-ground", Pos: at(0, 0).Center(), Dir: layout.W, Tag: tag})
	for x := 1; x <= 10; x++ {
		l.Add(layout.Placement{Name: "transport-belt", Pos: at(x, 0).Center(), Dir: layout.N, Tag: "c/logistics/x"})
	}
	l.Add(layout.Placement{Name: "pipe-to-ground", Pos: at(11, 0).Center(), Dir: layout.E, Tag: tag})
	l.Add(layout.Placement{Name: "pipe", Pos: at(12, 0).Center(), Tag: tag})
	if n := len(LongPipes(l)); n != 1 {
		t.Fatalf("длинных пар %d, ждал 1", n)
	}
	removed, added, err := FixLongPipes(l)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || len(added) == 0 {
		t.Errorf("снято %d, проложено %d", len(removed), len(added))
	}
	if n := len(LongPipes(l)); n != 0 {
		t.Errorf("после обхода длинных пар %d", n)
	}
	for _, e := range l.Validate() { // ленты-заглушки обрываются — не о трубе
		if strings.Contains(e, "труб") {
			t.Errorf("раскладка: %s", e)
		}
	}
}
