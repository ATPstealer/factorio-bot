package cluster

import (
	"fmt"
	"math"
	"slices"

	"factorio-bot/internal/layout"
)

// Ленты шин плана (cluster.md §5): новая лента — в свободный ряд, от того места, где продукт
// приходит на шину (или от головы), до последнего пользователя.

// LaterNames — имена будущих блоков добычи (Later) среди cs: их подводы ленту «сейчас» не кормят.
func LaterNames(cs []*Cluster) map[string]bool {
	res := map[string]bool{}
	for _, c := range cs {
		if c.Later {
			res[c.Name] = true
		}
	}
	return res
}

// newLane — новая лента it шины k до end — в свободный ряд; обход неровности не проводится
// (там обходы других лент, утёс у самого ряда) — в следующий свободный.
func (b *Base) newLane(l *layout.Layout, buses []*ZBus, k int, it string, end int) error {
	_, _, err := b.newLaneFrom(l, buses, k, it, end, 0)
	return err
}

// newLaneFrom — newLane в первый свободный ряд не раньше ряда j0 (по порядку сверху вниз):
// Y ленты и номер следующего ряда.
func (b *Base) newLaneFrom(l *layout.Layout, buses []*ZBus, k int, it string, end, j0 int) (int, int, error) {
	bus := buses[k]
	used := map[int]bool{}
	for _, ln := range bus.Lanes {
		used[ln.Y-bus.Rect.Min.Y] = true
	}
	var err error = fmt.Errorf("cluster: %s: нет ряда под ленту %s", bus.Name, it)
	for j := j0; laneV(j) < bus.Rect.H(); j++ {
		v := laneV(j)
		if used[v] {
			continue
		}
		saved := slices.Clone(l.List)
		start := b.laneStart(bus, k, it)
		bus.Lanes = append(bus.Lanes, ZoneLane{Item: it, Y: bus.Rect.Min.Y + v, Start: start})
		var gaps []laneGap
		b.layLane(l, buses, k, len(bus.Lanes)-1, start, end, -1, false, &gaps)
		if err = fixGaps(l, buses, gaps); err == nil {
			return bus.Rect.Min.Y + v, j + 1, nil
		}
		bus.Lanes = bus.Lanes[:len(bus.Lanes)-1]
		l.List = saved
		l.Rollback(-1)
	}
	return 0, 0, err
}

// laneStart — откуда новая лента it шины k: продукт этажа под шиной кладётся у столбца своего
// кластера — лента начинается у самого левого из них (у головы шины тесно от подводов и
// отводов: обходы ломались, 859088572); добытое и сквозное — от головы.
func (b *Base) laneStart(bus *ZBus, k int, it string) int {
	start := math.MaxInt
	for _, c := range b.Clusters {
		if floorOf(c) == k && k > 0 && slices.ContainsFunc(c.Out, func(f Flow) bool { return f.Item == it }) {
			start = min(start, c.Area.Min.X-laneTail)
		}
	}
	if start == math.MaxInt {
		return bus.Rect.Min.X
	}
	return max(start, bus.Rect.Min.X)
}

// Tier2 — постройки второго уровня на место первых (размеры те же — замена по имени в
// раскладке; cluster.md §7): инсертеры — стадия FastFrom, печи и сборщики — Tier2From, ленты —
// стадия 68; длинные инсертеры второго уровня не имеют; столбы и сундуки — только первого уровня
// (решение пользователя).
var Tier2 = map[string]string{
	"transport-belt":       "fast-transport-belt",
	"underground-belt":     "fast-underground-belt",
	"splitter":             "fast-splitter",
	"inserter":             "fast-inserter",
	"stone-furnace":        "steel-furnace",
	"assembling-machine-1": "assembling-machine-2",
}

// YellowRate — жёлтая лента, шт/с.
const YellowRate = 15.0

// FastLanes — ленты шин выше bus-1, которым жёлтой мало (cluster.md §5): шина → предметы, у
// которых выпуск поставщиков с этажа под шиной (на втором уровне машин) больше 15/с; ряд такой
// ленты — быстрыми со второго уровня.
func FastLanes(cs []*Cluster) map[string]map[string]bool {
	rate := map[string]map[string]float64{}
	for _, c := range cs {
		f := floorOf(c)
		op, ok := outPort(c)
		if f == 0 || !ok {
			continue
		}
		bus := fmt.Sprintf("bus-%d", f+1)
		if rate[bus] == nil {
			rate[bus] = map[string]float64{}
		}
		for _, o := range c.Out {
			if o.Item == op.Item {
				rate[bus][o.Item] += o.Rate
			}
		}
	}
	res := map[string]map[string]bool{}
	for bus, items := range rate {
		for it, r := range items {
			if r > YellowRate+1e-9 {
				if res[bus] == nil {
					res[bus] = map[string]bool{}
				}
				res[bus][it] = true
			}
		}
	}
	return res
}
