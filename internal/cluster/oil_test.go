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
	far = slices.DeleteFunc(far, func(pl layout.Placement) bool {
		return pl.Tag != tag || rectDist(tank.Area, world.TileOf(pl.Pos)) <= inletCut+10
	})
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

// Склад нефти — к трубе нефти шины через насос на выходе (стадия 36 на 1114315936: насос стоял
// на вход — качал из трубы в склад, до НПЗ нефть не шла): поток насоса — от цистерны.
func TestTankJoinPumpOutlet(t *testing.T) {
	p := pt(t)
	l := layout.New(p, nil)
	tank := &Cluster{Name: "oil-tank-1", Kind: Tank, Product: "crude-oil", Area: Rect{world.Tile{X: 0, Y: 0}, world.Tile{X: 6, Y: 6}}}
	for i, c := range [][2]float64{{1.5, 1.5}, {4.5, 1.5}, {1.5, 4.5}, {4.5, 4.5}} {
		l.Add(layout.Placement{Name: "storage-tank", Pos: world.Tile{X: int(c[0]), Y: int(c[1])}.Center(), Dir: 4 * (i % 2), Tag: Tag(tank.Name)})
	}
	tag := "c/bus-1/crude-oil"
	for x := -10; x <= 16; x++ {
		l.Add(layout.Placement{Name: "pipe", Pos: world.Tile{X: x, Y: -12}.Center(), Tag: tag})
	}
	if err := TankJoinPump(l, tank, tag); err != nil {
		t.Fatal(err)
	}
	pumps := 0
	for _, pl := range l.List {
		if pl.Name != "pump" {
			continue
		}
		pumps++
		for _, c := range layout.Connections(p, pl) {
			inTank := c.To.X >= 0 && c.To.X < 6 && c.To.Y >= 0 && c.To.Y < 6
			if c.Flow == "output" && inTank {
				t.Errorf("насос %v качает в склад, а не из него", pl.Pos)
			}
		}
	}
	if pumps != 1 {
		t.Fatalf("насосов %d, ждали 1", pumps)
	}
}

// Столбы вдоль трубы из двух кусков не на одной прямой (стадия 34 на 1098794301): вдоль каждого
// куска, вплотную к трубе; скачок между кусками — не по диагонали (раньше «промежуток» шёл
// диагональю за конец — цепочка через озеро, ничего не питающая).
func TestPolesAlongJump(t *testing.T) {
	l := layout.New(pt(t), nil)
	tag := LogisticsTag + "/oil-1"
	var leg []layout.Placement
	for x := 0; x < 20; x++ { // кусок 1: y = 0
		leg = append(leg, layout.Placement{Name: "pipe", Pos: world.Tile{X: x, Y: 0}.Center(), Tag: tag})
	}
	for x := 40; x < 60; x++ { // кусок 2: y = 30, вбок от первого
		leg = append(leg, layout.Placement{Name: "pipe", Pos: world.Tile{X: x, Y: 30}.Center(), Tag: tag})
	}
	for _, pl := range leg {
		l.Add(pl)
	}
	polesAlong(l, leg, tag)
	poles := 0
	for _, pl := range l.List {
		if pl.Name != "small-electric-pole" {
			continue
		}
		poles++
		if t0 := world.TileOf(pl.Pos); t0.Y < -1 || t0.Y > 1 && t0.Y < 29 || t0.Y > 31 {
			t.Errorf("столб %v — не у трубы", pl.Pos)
		}
	}
	if poles < 6 {
		t.Errorf("столбов %d, ждали вдоль обоих кусков (не меньше 6)", poles)
	}
}
