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
			// ряды лент — где их больше всего (обходы неровностей — в стороне, в зазорах); у
			// добытого лент две — второй ряд не короче половины первого
			n := 0
			for yy, k := range ys {
				if laneRow(yy-bus.Rect.Min.Y, bus.Rect.H()) {
					n = max(n, k)
				}
			}
			for y, k := range ys {
				if laneRow(y-bus.Rect.Min.Y, bus.Rect.H()) && 2*k >= n {
					bus.Lanes = append(bus.Lanes, fillLane(bus, list, it, y))
				}
			}
		}
		sort.Slice(bus.Lanes, func(i, j int) bool { return bus.Lanes[i].Y < bus.Lanes[j].Y })
	}
}

// fillLane — лента it в ряду y шины bus по раскладке list: голова, самый ранний подвод.
func fillLane(bus *ZBus, list []layout.Placement, it string, y int) ZoneLane {
	tag := Tag(bus.Name)
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
	return ln
}

// newLane — новая лента it шины k до end — в свободный ряд; обход неровности не проводится
// (там обходы других лент, утёс у самого ряда) — в следующий свободный.
func (b *Base) newLane(l *layout.Layout, buses []*ZBus, k int, it string, end int) error {
	bus := buses[k]
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
			return nil
		}
		bus.Lanes = bus.Lanes[:len(bus.Lanes)-1]
		l.List = saved
		l.Rollback(-1)
	}
	return err
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
			n := 0
			for j := range bus.Lanes { // старые ленты — продолжить, если нужны дальше
				ln := &bus.Lanes[j]
				if ln.Item != it {
					continue
				}
				n++
				if last := laneLast(l, bus, ln); end > last+1 {
					var gaps []laneGap
					b.layLane(l, buses, k, j, last+1, end, last, true, &gaps)
					if err := fixGaps(l, buses, gaps); err != nil {
						return err
					}
				}
			}
			for ; n < b.laneCount(k, it); n++ {
				if err := b.newLane(l, buses, k, it, end); err != nil {
					return err
				}
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
		if c.Kind == Mine { // ещё одна добыча того же ресурса — боком в подвод до головы
			if err := b.feedMine(l, buses, c, nil); err != nil {
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

// AddInput — второй пакет ленты A уже построенного блока лабораторий (cluster.md §2а «Лаборатории»): подача
// с другой стороны ленты — боком в её голову (на свою полосу), буфер — напротив подачи, на
// оставленных шаблоном местах. Построенное не трогает; поток item rate — во входы кластера.
func AddInput(l *layout.Layout, c *Cluster, item string, rate float64, tag string) (Port, error) {
	if c.Kind != Lab || len(c.In) != 1 {
		return Port{}, fmt.Errorf("cluster %s: второй вход — только у блока лабораторий с одним пакетом", c.Name)
	}
	f := c.frame()
	for _, t := range labReserve(f, c) {
		delete(l.Reserved, t)
	}
	// подача — с торца снизу в ряду A+1 и поворотом боком в голову ленты A (cluster.md §6б)
	p := f.at(l, "transport-belt", 0, labA+1, layout.N, tag)
	if t := world.TileOf(p.Pos); l.At(t) != nil {
		return Port{}, fmt.Errorf("cluster %s: второй вход не встаёт (%s)", c.Name, p)
	} else if ok, _ := l.W.Buildable(t); !ok {
		return Port{}, fmt.Errorf("cluster %s: второй вход не встаёт (%s)", c.Name, p)
	}
	l.Add(p)
	if err := bufferAt(l, f, c, labA, labA-1, labA-2, (c.Head-2)/2, item, tag); err != nil {
		return Port{}, err
	}
	// столбы к новым инсертерам — внутри площади, на свободных тайлах (не на месте под будущее)
	keep := map[world.Tile]bool{}
	for _, t := range labReserve(f, c) {
		keep[t] = true
	}
	var opened []world.Tile
	for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
		for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
			t := world.Tile{X: x, Y: y}
			if l.Reserved[t] && !keep[t] && l.At(t) == nil {
				delete(l.Reserved, t)
				opened = append(opened, t)
			}
		}
	}
	l.PoleTag = tag
	l.Poles("small-electric-pole", tag)
	l.PoleTag = ""
	for _, t := range opened {
		if l.At(t) == nil {
			l.Reserved[t] = true
		}
	}
	port := Port{T: f.tile(-1, labA+1), Dir: f.dir(layout.E), Item: item, In: true}
	c.Ports = append(c.Ports, port)
	addFlow(&c.In, item, rate)
	return port, nil
}
