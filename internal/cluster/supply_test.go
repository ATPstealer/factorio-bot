package cluster

import (
	"testing"

	"factorio-bot/internal/world"
)

// Поставщики по выпуску при расстановке (zones.md): потребитель — правее стольких поставщиков
// слева направо, сколько покрывают его вход; больше всего выпуска — правее всех; жидкости — правее
// самого правого.
func TestSupplyMinX(t *testing.T) {
	small := &Cluster{Name: "adv-circuit-2", Area: Rect{Min: world.Tile{X: 133}, Max: world.Tile{X: 142}},
		Out: []Flow{{"advanced-circuit", 0.25}}}
	big := &Cluster{Name: "adv-circuit-1", Area: Rect{Min: world.Tile{X: 177}, Max: world.Tile{X: 186}},
		Out: []Flow{{"advanced-circuit", 1.25}}}
	for _, c := range []struct {
		need  float64
		fluid bool
		want  int
	}{
		{0.2, false, 133},  // малому хватает малого
		{0.75, false, 177}, // модулям — и большой (366255770)
		{2.25, false, 177}, // электропечам больше всего выпуска — правее всех
		{0.1, true, 177},   // жидкость — правее самого правого
	} {
		x, ok := supplyMinX([]*Cluster{big, small}, Flow{"advanced-circuit", c.need}, c.fluid)
		if !ok || x != c.want {
			t.Errorf("вход %.2f (жидкость %v): %d, ждали %d", c.need, c.fluid, x, c.want)
		}
	}
	if _, ok := supplyMinX(nil, Flow{"advanced-circuit", 1}, false); ok {
		t.Errorf("без поставщиков — есть место")
	}
}

// Отвод с ленты шины (zones.md): ниже по течению стольких подводов слева направо, сколько
// покрывают вход потребителя или всё, что входит в ленту; подвод без меры (добыча, сквозная) —
// хватает его одного.
func TestLaneCover(t *testing.T) {
	ln := &ZoneLane{Item: "advanced-circuit"}
	ln.feed(184, true, 1.25)
	ln.feed(140, true, 0.25)
	for _, c := range []struct {
		need float64
		want int
	}{{0.2, 140}, {0.75, 184}, {2.25, 184}} {
		if x := ln.cover(c.need, 1000); x != c.want {
			t.Errorf("вход %.2f: отвод ниже %d, ждали %d", c.need, x, c.want)
		}
	}
	// потребитель левее подвода 184 (правый край 170) — 184 не в счёт: ниже того, что левее
	if x := ln.cover(0.75, 170); x != 140 {
		t.Errorf("подвод правее потребителя: отвод ниже %d, ждали 140", x)
	}
	if ln.From != 140 {
		t.Errorf("самый ранний подвод %d, ждали 140", ln.From)
	}
	head := &ZoneLane{Item: "iron-ore"}
	head.feed(10, true, feedAny)
	head.feed(90, true, 3)
	if x := head.cover(20, 1000); x != 10 {
		t.Errorf("подвод в голову без меры: отвод ниже %d, ждали 10", x)
	}
	if x := (&ZoneLane{From: 55}).cover(1, 1000); x != 55 {
		t.Errorf("подводов не знаем: %d, ждали самый ранний 55", x)
	}
}

// Сквозная — с ленты нижней шины, куда есть подвод (как у потребителей): первая годится — она;
// первая пустая — с той, куда подаёт блок, что строится сейчас.
func TestThroughSource(t *testing.T) {
	bus := &ZBus{Lanes: []ZoneLane{{Item: "stone", Y: 1}, {Item: "stone", Y: 2, Fed: true}, {Item: "stone", Y: 3, Fed: true, Now: true}, {Item: "coal", Y: 4}}}
	if ln := throughSource(bus, "stone"); ln == nil || ln.Y != 3 {
		t.Errorf("первая пустая: %+v, ждали ленту 3 (подвод сейчас)", ln)
	}
	bus.Lanes[0].Fed, bus.Lanes[0].Now = true, true
	if ln := throughSource(bus, "stone"); ln == nil || ln.Y != 1 {
		t.Errorf("первая с подводом: %+v, ждали её", ln)
	}
	if ln := throughSource(bus, "coal"); ln == nil || ln.Y != 4 {
		t.Errorf("подвода нет ни в одну: %+v, ждали первую", ln)
	}
	if throughSource(bus, "iron-plate") != nil {
		t.Errorf("лент предмета нет — нашлась")
	}
}
