package cluster

import (
	"slices"
	"testing"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Построенный до насоса на входе склада план (стадия 36 на 427965634: трасса, склад и труба к НПЗ
// — один участок шире 320): InletPumps перекладывает конец трассы через насос на входе, дальше
// трасса не тронута.
func TestInletPumps(t *testing.T) {
	p := pt(t)
	l := layout.New(p, nil)
	tank := &Cluster{Name: "oil-tank-1", Kind: Tank, Product: "crude-oil", Area: Rect{world.Tile{X: 0, Y: 0}, world.Tile{X: 6, Y: 6}}}
	field := &Cluster{Name: "oil-1", Kind: Mine, Product: "crude-oil", Area: Rect{world.Tile{X: -80, Y: -10}, world.Tile{X: -70, Y: 0}}}
	for i, c := range [][2]float64{{1.5, 1.5}, {4.5, 1.5}, {1.5, 4.5}, {4.5, 4.5}} {
		l.Add(layout.Placement{Name: "storage-tank", Pos: world.Tile{X: int(c[0]), Y: int(c[1])}.Center(), Dir: 4 * (i % 2), Tag: Tag(tank.Name)})
	}
	tag := LogisticsTag + "/" + field.Name
	from := layout.Conn{At: world.Tile{X: -41, Y: 2}, To: world.Tile{X: -40, Y: 2}, Dir: layout.E}
	l.Add(layout.Placement{Name: "pipe", Pos: from.At.Center(), Tag: tag})
	if _, err := l.RoutePipeTo(from, tankConns(l, tank), tag); err != nil {
		t.Fatal(err)
	}
	far := slices.Clone(l.List)
	far = slices.DeleteFunc(far, func(pl layout.Placement) bool { return pl.Tag != tag || rectDist(tank.Area, world.TileOf(pl.Pos)) <= inletCut+10 })
	if hasInlet(l, tag, tank) {
		t.Fatal("насос на входе уже есть до перекладки")
	}
	removed, err := InletPumps(l, []*Cluster{tank, field}, tank)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 {
		t.Error("ничего не убрано")
	}
	if !hasInlet(l, tag, tank) {
		t.Error("насоса на входе склада нет")
	}
	for _, pl := range far {
		if i, ok := l.Index(world.TileOf(pl.Pos)); !ok || l.List[i] != pl {
			t.Errorf("тронута дальняя часть трассы: %v", pl)
		}
	}
	// повторно — ничего: насос уже есть
	if again, err := InletPumps(l, []*Cluster{tank, field}, tank); err != nil || len(again) > 0 {
		t.Errorf("повтор: убрано %d (%v)", len(again), err)
	}
}
