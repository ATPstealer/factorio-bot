package cluster

import (
	"testing"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Ленты руды — цепочкой к печам (cluster.md §2а «Добыча», §5): две ленты, с которых берут печи
// (y 0 и 5, Now), и две будущей добычи (y 10 и 11, рядом): 11 → 10 делителем поперёк пары, 10 → 5
// и 5 → 0 — перекидкой; каждый следующий делитель — ниже по течению прошлого, отводы — ниже
// последнего делителя на своей ленте (After). Было: будущая добыча вливалась только в ближнюю
// старую ленту (24996062: лента второй плавки железа — только с первого месторождения).
func TestChainLanes(t *testing.T) {
	l := layout.New(pt(t), nil)
	const item = "iron-ore"
	rows := []int{0, 5, 10, 11}
	for _, y := range rows {
		for x := range 120 {
			l.Add(layout.Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: y}.Center(), Dir: layout.E, Tag: "c/bus-1", Recipe: item})
		}
	}
	bus := &ZBus{Name: "bus-1"}
	for _, y := range rows {
		bus.Lanes = append(bus.Lanes, ZoneLane{Item: item, Y: y, Fed: true, Now: y < 10})
	}
	b := &Base{Log: t.Logf}
	b.chainLanes(l, bus)

	split := map[int][]int{} // ряд → X делителей на нём
	for _, pl := range l.List {
		if pl.Name != "splitter" {
			continue
		}
		for _, u := range layout.Tiles(l.P, pl) {
			split[u.Y] = append(split[u.Y], u.X)
		}
	}
	want := map[int]int{0: 1, 5: 2, 10: 2, 11: 1}
	for y, n := range want {
		if len(split[y]) != n {
			t.Fatalf("на ленте %d делителей %d (%v), ждал %d", y, len(split[y]), split[y], n)
		}
	}
	lo := func(y int) int { return min(split[y][0], split[y][len(split[y])-1]) }
	hi := func(y int) int { return max(split[y][0], split[y][len(split[y])-1]) }
	// 11 → 10 (пара), 10 → 5, 5 → 0 — по течению друг за другом
	if !(split[11][0] == lo(10) && lo(10) < hi(10) && hi(10) < lo(5) && lo(5) < hi(5) && hi(5) < split[0][0]) {
		t.Errorf("порядок делителей по течению: %v", split)
	}
	// отводы — ниже делителя, которым в ленту отдаёт следующая; у последней (11) его нет
	for i, want := range map[int]*int{0: &split[0][0], 1: new(lo(5)), 2: new(lo(10)), 3: nil} {
		ln := bus.Lanes[i]
		switch {
		case want == nil && ln.After != nil:
			t.Errorf("лента %d: After %d, ждал без него", ln.Y, *ln.After)
		case want != nil && (ln.After == nil || *ln.After != *want):
			t.Errorf("лента %d: After %v, ждал %d (делитель, которым в неё отдаёт следующая)", ln.Y, ln.After, *want)
		}
	}
}
