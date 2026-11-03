package cluster

import (
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Инсертер пары: провод → схема на сборщике второго уровня — 4.5/с, обычный с бонусом носит 1.7 —
// быстрый; шестерня → инсертер — 1.5/с, обычного хватает. Сохранённый план: обычный между
// сборщиками провода (снизу) и схем (сверху) — быстрый, сбоку от сборщика схем — как был.
func TestPairInserter(t *testing.T) {
	p := pt(t)
	if got := PairInserter(p, "inserter", "assembling-machine-1", "electronic-circuit", "copper-cable"); got != "fast-inserter" {
		t.Errorf("провод → схема: %s, ждал fast-inserter", got)
	}
	if got := PairInserter(p, "inserter", "assembling-machine-1", "inserter", "iron-gear-wheel"); got != "inserter" {
		t.Errorf("шестерня → инсертер: %s, ждал inserter", got)
	}
	at := world.Tile{X: 0, Y: 3}
	list := []layout.Placement{
		{Name: "assembling-machine-1", Pos: bridge.Pos{X: 0.5, Y: 1.5}, Recipe: "electronic-circuit"},
		{Name: "assembling-machine-1", Pos: bridge.Pos{X: 0.5, Y: 5.5}, Recipe: "copper-cable"},
		{Name: "inserter", Pos: at.Center(), Dir: layout.InserterDir(p, "inserter", at, world.Tile{X: 0, Y: 4})},
		{Name: "inserter", Pos: bridge.Pos{X: 2.5, Y: 1.5}, Dir: layout.W},
	}
	if n := FastPairs(p, list); n != 1 || list[2].Name != "fast-inserter" || list[3].Name != "inserter" {
		t.Errorf("заменено %d: в паре %s, сбоку %s", n, list[2].Name, list[3].Name)
	}
}
