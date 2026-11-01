package layout

import (
	"testing"

	"factorio-bot/internal/world"
)

// Прямая подземная труба на 400 тайлов от помпы: один участок шире 300 — SplitWide ставит насос
// (от помпы), участков становится два, каждый не шире 300.
func TestSplitWide(t *testing.T) {
	l := New(pt(t), nil)
	l.Add(Placement{Name: "offshore-pump", Pos: world.Tile{X: -1, Y: 0}.Center(), Dir: W, Tag: "w"})
	l.Add(Placement{Name: "pipe", Pos: world.Tile{X: 0, Y: 0}.Center(), Tag: "c/bus-1/water"})
	for x := 1; x+9 <= 400; x += 10 {
		l.Add(Placement{Name: "pipe-to-ground", Pos: world.Tile{X: x, Y: 0}.Center(), Dir: W, Tag: "c/bus-1/water"})
		l.Add(Placement{Name: "pipe-to-ground", Pos: world.Tile{X: x + 9, Y: 0}.Center(), Dir: E, Tag: "c/bus-1/water"})
	}
	if w := l.WideSegments(PipeExtent); len(w) != 1 {
		t.Fatalf("до насосов: %v", w)
	}
	added, err := l.SplitWide(func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if w := l.WideSegments(PipeExtent); len(w) != 0 {
		t.Fatalf("после насосов: %v", w)
	}
	pumps := 0
	for _, pl := range added {
		if pl.Name == "pump" {
			pumps++
			if pl.Dir != E {
				t.Errorf("насос %v — не от помпы", pl)
			}
		}
	}
	if pumps != 1 {
		t.Errorf("насосов %d: %v", pumps, added)
	}
	n := l.pipeNet()
	roots := map[int]bool{}
	for _, r := range n.parts(-1) {
		roots[r] = true
	}
	if len(roots) != 2 {
		t.Errorf("участков %d, нужно 2", len(roots))
	}
	if errs := l.Validate(); len(errs) > 0 {
		t.Errorf("раскладка: %v", errs)
	}
}
