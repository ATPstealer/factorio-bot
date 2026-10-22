package cluster

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Дописывание плана (stages.md, стадия 18): построенное по плану — препятствие, новые кластеры
// встают рядом в свои зоны, ленты шин — новые ряды или продолжение старых.

// FillLanes — ленты шин buses по раскладке прошлого плана list: предмет, ряд, голова, самый
// ранний подвод (отводы — только ниже по течению).
func FillLanes(buses []*ZBus, list []layout.Placement) {
	for _, bus := range buses {
		tag := Tag(bus.Name)
		rows := map[string]map[int]int{} // предмет → ряд → лент
		for _, p := range list {
			if p.Tag == tag && p.Name == "transport-belt" && p.Recipe != "" {
				if rows[p.Recipe] == nil {
					rows[p.Recipe] = map[int]int{}
				}
				rows[p.Recipe][world.TileOf(p.Pos).Y]++
			}
		}
		bus.Lanes = nil
		for it, ys := range rows {
			y, n := 0, -1
			for yy, k := range ys { // ряд ленты — где её больше всего (обходы неровностей — в стороне)
				if k > n || k == n && yy < y {
					y, n = yy, k
				}
			}
			ln := ZoneLane{Item: it, Y: y, Start: bus.Rect.Max.X}
			for _, p := range list {
				if t := world.TileOf(p.Pos); p.Tag == tag && p.Recipe == it && t.Y == y {
					ln.Start = min(ln.Start, t.X)
				}
			}
			// подводы: ленты логистики этой шины, смотрящие в ряд сбоку или в голову
			prefix := LogisticsTag + "/" + bus.Name + "/"
			for _, p := range list {
				if !strings.HasPrefix(p.Tag, prefix) || p.Name != "transport-belt" {
					continue
				}
				t := world.TileOf(p.Pos)
				if t.Y == y-1 && p.Dir == layout.S || t.Y == y+1 && p.Dir == layout.N || t.Y == y && t.X == ln.Start-1 && p.Dir == layout.E {
					ln.feed(t.X)
				}
			}
			if !ln.Fed { // поставщик есть (лента построена), подвода не нашли — отводы где угодно
				ln.feed(ln.Start)
			}
			bus.Lanes = append(bus.Lanes, ln)
		}
		sort.Slice(bus.Lanes, func(i, j int) bool { return bus.Lanes[i].Y < bus.Lanes[j].Y })
	}
}

// laneLast — X последней ленты ряда ln шины bus.
func laneLast(l *layout.Layout, bus *ZBus, ln *ZoneLane) int {
	last := ln.Start
	for _, p := range l.List {
		if t := world.TileOf(p.Pos); p.Tag == Tag(bus.Name) && p.Recipe == ln.Item && t.Y == ln.Y {
			last = max(last, t.X)
		}
	}
	return last
}

// ConnectMore — логистика новых кластеров fresh в уже связанной базе: недостающие ленты шин —
// новыми рядами, нужные дальше — продолжением; выходы — боком в ленты шины над собой,
// сквозные — отводом с шины ниже, входы — делителем; extra — новые входы старых кластеров.
func (b *Base) ConnectMore(l *layout.Layout, buses []*ZBus, fresh []*Cluster, extra map[*Cluster][]Port) error {
	on := b.zoneSources(len(buses))
	need := b.zoneNeed(on, len(buses))
	for k, bus := range buses {
		for _, it := range sortedItems(need[k], on, k) {
			end := b.laneEnd(buses, k, it)
			if ln := bus.lane(it); ln != nil { // старая лента — продолжить, если нужна дальше
				j := 0
				for bus.Lanes[j].Item != it {
					j++
				}
				if last := laneLast(l, bus, ln); end > last+1 {
					var gaps []laneGap
					b.layLane(l, buses, k, j, last+1, end, last, true, &gaps)
					if err := fixGaps(l, buses, gaps); err != nil {
						return err
					}
				}
				continue
			}
			// новая — в свободный ряд; обход неровности не проводится (там обходы старых лент) —
			// в следующий
			used := map[int]bool{}
			for _, ln := range bus.Lanes {
				used[ln.Y-bus.Rect.Min.Y] = true
			}
			var err error = fmt.Errorf("cluster: %s: нет ряда под ленту %s", bus.Name, it)
			for j := 0; laneV(j) < bus.Rect.H(); j++ {
				v := laneV(j)
				if used[v] {
					continue
				}
				saved := slices.Clone(l.List)
				bus.Lanes = append(bus.Lanes, ZoneLane{Item: it, Y: bus.Rect.Min.Y + v, Start: bus.Rect.Min.X})
				var gaps []laneGap
				b.layLane(l, buses, k, len(bus.Lanes)-1, bus.Rect.Min.X, end, -1, false, &gaps)
				if err = fixGaps(l, buses, gaps); err == nil {
					break
				}
				bus.Lanes = bus.Lanes[:len(bus.Lanes)-1]
				l.List = saved
				l.Rollback(-1)
			}
			if err != nil {
				return err
			}
		}
	}
	g := guardPorts(l, fresh)
	for _, ps := range extra {
		for _, p := range ps {
			g.close(p)
		}
	}
	for _, c := range fresh {
		if c.Kind == Mine { // ещё одна добыча того же ресурса — боком у головы ленты
			if err := feedMine(l, buses, c); err != nil {
				return &ConnectError{c, err}
			}
			continue
		}
		if err := b.feedOut(l, buses, c, g); err != nil {
			return err
		}
	}
	if err := feedThrough(l, buses); err != nil {
		return err
	}
	for _, c := range fresh {
		if floorOf(c) == 0 {
			continue
		}
		for _, p := range c.Ports {
			if p.In {
				if err := feedIn(l, buses, c, p, g); err != nil {
					return err
				}
			}
		}
	}
	for c, ps := range extra {
		for _, p := range ps {
			if err := feedIn(l, buses, c, p, g); err != nil {
				return err
			}
		}
	}
	return nil
}

// AddInput — второй вход уже построенного кластера с одним входом (лаборатории — зелёная наука):
// лента с другой стороны ряда — боком в столбец u=0 ленты входа (как второй вход шаблона),
// порт — у края Area. Построенное не трогает; поток item rate — во входы кластера.
func AddInput(l *layout.Layout, c *Cluster, item string, rate float64, tag string) (Port, error) {
	if len(c.In) != 1 {
		return Port{}, fmt.Errorf("cluster %s: второй вход — только к одному (входов %d)", c.Name, len(c.In))
	}
	f := c.frame()
	const belts = 2 // ряд ленты входа (Interior)
	for v := c.Cross - 1; v > belts; v-- {
		p := f.at(l, "transport-belt", 0, v, layout.N, tag)
		t := world.TileOf(p.Pos)
		if ok, _ := l.W.Buildable(t); !ok || l.At(t) != nil {
			return Port{}, fmt.Errorf("cluster %s: второй вход не встаёт (%s)", c.Name, p)
		}
		l.Add(p)
	}
	port := Port{T: f.tile(0, c.Cross), Dir: f.dir(layout.N), Item: item, In: true}
	c.Ports = append(c.Ports, port)
	addFlow(&c.In, item, rate)
	return port, nil
}
