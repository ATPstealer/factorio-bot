package cluster

import (
	"strings"
	"testing"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Труба шины внутри чужой подземной пары на той же линии (189715922, y −412.5: вода bus-1 — обычными
// трубами x 459–466 внутри пары тяжёлого масла 457→467, проложенной в обход): под землю её не
// уводить — пара внутри чужой игра сцепит не так (cluster.md §5). Validate такую вложенность ловит.
func TestUnderLaneNotInsideOtherPair(t *testing.T) {
	p := pt(t)
	at := func(x, y int) world.Tile { return world.Tile{X: x, Y: y} }
	const oil, water = "c/bus-2/heavy-oil", "c/bus-1/water"
	build := func() *layout.Layout {
		l := layout.New(p, nil)
		l.Add(layout.Placement{Name: "pipe-to-ground", Pos: at(0, 0).Center(), Dir: layout.W, Tag: oil})
		l.Add(layout.Placement{Name: "pipe-to-ground", Pos: at(10, 0).Center(), Dir: layout.E, Tag: oil})
		for x := 1; x <= 9; x++ {
			l.Add(layout.Placement{Name: "pipe", Pos: at(x, 0).Center(), Tag: water})
		}
		return l
	}
	l := build()
	UnderLane(l, water, 0, nil)
	for _, pl := range l.List {
		if pl.Tag == water && pl.Name == "pipe-to-ground" {
			t.Errorf("подземная труба воды внутри пары тяжёлого масла: %v", pl.Pos)
		}
	}
	for _, e := range l.Validate() {
		if strings.Contains(e, "чужой") {
			t.Errorf("раскладка: %s", e)
		}
	}
	// та же вложенность руками — Validate её видит
	l = build()
	l.List = l.List[:2]
	l.Rollback(-1)
	l.Add(layout.Placement{Name: "pipe-to-ground", Pos: at(3, 0).Center(), Dir: layout.W, Tag: water})
	l.Add(layout.Placement{Name: "pipe-to-ground", Pos: at(7, 0).Center(), Dir: layout.E, Tag: water})
	n := 0
	for _, e := range l.Validate() {
		if strings.Contains(e, "чужой") {
			n++
		}
	}
	if n == 0 {
		t.Error("Validate не видит пару внутри чужой")
	}
}
