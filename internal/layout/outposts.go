package layout

import (
	"fmt"
	"math"
	"sort"

	"factorio-bot/internal/world"
)

// Добывающие точки начала игры на burner-бурах (стадия 7.0): без лент и инсертеров —
// бур выдаёт прямо в соседа.

// onOre — все тайлы размещения на руде ore и свободны.
func (l *Layout) onOre(pl Placement, ore string) bool {
	for _, t := range Tiles(l.P, pl) {
		if !l.Free(t, AnyLand) {
			return false
		}
		if l.W != nil {
			if r, ok := l.W.ResourceAt(t); !ok || r.Name != ore {
				return false
			}
		}
	}
	return true
}

func contains(ts []world.Tile, t world.Tile) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

// byDistance — тайлы по расстоянию до near.
func byDistance(tiles []world.Tile, near world.Tile) []world.Tile {
	ts := append([]world.Tile(nil), tiles...)
	sort.Slice(ts, func(i, j int) bool {
		di := math.Hypot(float64(ts[i].X-near.X), float64(ts[i].Y-near.Y))
		dj := math.Hypot(float64(ts[j].X-near.X), float64(ts[j].Y-near.Y))
		return di < dj
	})
	return ts
}

// CoalPair — два бура на руде ore выходами друг в друга (каждый заправляет другого);
// ближайшее к near место на патче.
func (l *Layout) CoalPair(patch []world.Tile, ore, drill string, near world.Tile, tag string) error {
	for _, a := range byDistance(patch, near) {
		for _, d := range []int{E, S} {
			pa := Placement{Name: drill, Pos: CenterAt(l.P, drill, d, a), Dir: d, Tag: tag}
			w, h := Size(l.P, drill, d)
			b := world.Tile{X: a.X + w, Y: a.Y}
			if d == S {
				b = world.Tile{X: a.X, Y: a.Y + h}
			}
			pb := Placement{Name: drill, Pos: CenterAt(l.P, drill, (d+8)%16, b), Dir: (d + 8) % 16, Tag: tag}
			if !contains(Tiles(l.P, pb), DrillOutput(l.P, pa)) || !contains(Tiles(l.P, pa), DrillOutput(l.P, pb)) {
				continue
			}
			if l.onOre(pa, ore) && l.onOre(pb, ore) {
				l.Add(pa)
				l.Add(pb)
				return nil
			}
		}
	}
	return fmt.Errorf("layout: нет места под пару буров на %s", ore)
}

// DrillFurnace — бур на руде ore выходом в печь (печь рядом, не на буре).
func (l *Layout) DrillFurnace(patch []world.Tile, ore, drill, furnace string, near world.Tile, tag string) error {
	fw, fh := Size(l.P, furnace, N)
	for _, a := range byDistance(patch, near) {
		for _, d := range []int{N, E, S, W} {
			pd := Placement{Name: drill, Pos: CenterAt(l.P, drill, d, a), Dir: d, Tag: tag}
			if !l.onOre(pd, ore) {
				continue
			}
			out := DrillOutput(l.P, pd)
			// печь накрывает выход бура: перебор её левого верхнего угла
			for dy := -(fh - 1); dy <= 0; dy++ {
				for dx := -(fw - 1); dx <= 0; dx++ {
					pf := Placement{Name: furnace, Pos: CenterAt(l.P, furnace, N, world.Tile{X: out.X + dx, Y: out.Y + dy}), Tag: tag}
					if !l.CanPlace(pf, AnyLand) || overlaps(l, pf, pd) {
						continue
					}
					l.Add(pd)
					l.Add(pf)
					return nil
				}
			}
		}
	}
	return fmt.Errorf("layout: нет места под бур с печью на %s", ore)
}

// StoneChest — n буров на руде ore выходами в один сундук.
func (l *Layout) StoneChest(patch []world.Tile, ore, drill, chest string, n int, near world.Tile, tag string) error {
	for _, t := range byDistance(patch, near) {
		pc := Placement{Name: chest, Pos: t.Center(), Tag: tag}
		if !l.CanPlace(pc, AnyLand) {
			continue
		}
		mark := l.Mark()
		l.Add(pc)
		got := 0
		for _, d := range []int{N, E, S, W} {
			if got == n {
				break
			}
			// бур направления d, чей выход — сундук
			placed := false
			for dy := -3; dy <= 3 && !placed; dy++ {
				for dx := -3; dx <= 3 && !placed; dx++ {
					pd := Placement{Name: drill, Pos: CenterAt(l.P, drill, d, world.Tile{X: t.X + dx, Y: t.Y + dy}), Dir: d, Tag: tag}
					if DrillOutput(l.P, pd) == t && l.onOre(pd, ore) {
						l.Add(pd)
						got++
						placed = true
					}
				}
			}
		}
		if got == n {
			return nil
		}
		l.Rollback(mark)
	}
	return fmt.Errorf("layout: нет места под %d буров в сундук на %s", n, ore)
}

func overlaps(l *Layout, a, b Placement) bool {
	tb := Tiles(l.P, b)
	for _, t := range Tiles(l.P, a) {
		if contains(tb, t) {
			return true
		}
	}
	return false
}

// ChestMine — электробуры на руде ore парами навстречу (верхний — на юг, нижний — на север)
// с общим сундуком между ними: n пар в ряд; бот забирает добытое из сундуков большими
// партиями (уголь для кормушек бойлеров и печей). Столбы — отдельно (Poles).
func (l *Layout) ChestMine(patch []world.Tile, ore string, n int, near world.Tile, tag string) error {
	drill, chest := "electric-mining-drill", "wooden-chest"
	dw, dh := Size(l.P, drill, N)
	for _, a := range byDistance(patch, near) {
		mark := l.Mark()
		ok := true
		for i := 0; i < n && ok; i++ {
			x := a.X + i*dw
			top := Placement{Name: drill, Pos: CenterAt(l.P, drill, S, world.Tile{X: x, Y: a.Y}), Dir: S, Tag: tag}
			c := DrillOutput(l.P, top)
			bottom := Placement{Name: drill, Pos: CenterAt(l.P, drill, N, world.Tile{X: x, Y: c.Y + 1}), Dir: N, Tag: tag}
			pc := Placement{Name: chest, Pos: c.Center(), Tag: tag}
			if c.Y != a.Y+dh || DrillOutput(l.P, bottom) != c || !l.onOre(top, ore) || !l.onOre(bottom, ore) || !l.CanPlace(pc, AnyLand) {
				ok = false
				break
			}
			l.Add(top)
			l.Add(pc)
			l.Add(bottom)
		}
		if ok {
			return nil
		}
		l.Rollback(mark)
	}
	return fmt.Errorf("layout: на %s нет места под %d пар буров", ore, n)
}

// FeedBelt — лента от from (пришли с направлением dir) мимо потребителей: у каждого —
// инсертер с ленты в один из тайлов группы (кормушка бойлера или сам бойлер: уголь без
// бота). Порядок — ближайший следующий; лента кончается тупиком у последнего (уголь
// копится на ленте). Кого не подвести (обставлен) — пропуск; fed — сколько подведено.
func (l *Layout) FeedBelt(from world.Tile, dir int, groups [][]world.Tile, tag string) (fed int, err error) {
	const ins = "inserter"
	type spot struct{ in, b, c world.Tile }
	// место под инсертер и ленту у каждого — заранее (маршруты их не займут)
	// сторона — ближайшая к шахте: с дальней (у воды за станцией) ленту не провести
	var spots []spot
	for _, g := range groups {
		best, bd := spot{}, math.Inf(1)
		for _, c := range g {
			for _, d := range []int{N, E, S, W} {
				in, b := Step(c, d), Step(Step(c, d), d)
				if dd := math.Hypot(float64(b.X-from.X), float64(b.Y-from.Y)); l.Free(in, AnyLand) && l.Free(b, AnyLand) && dd < bd {
					best, bd = spot{in, b, c}, dd
				}
			}
		}
		if !math.IsInf(bd, 1) {
			spots = append(spots, best)
			l.Reserved[best.in], l.Reserved[best.b] = true, true
		}
	}
	if len(spots) == 0 {
		return 0, fmt.Errorf("layout: ни к одной кормушке не подвести ленту")
	}
	defer func() {
		for _, sp := range spots {
			delete(l.Reserved, sp.in)
			delete(l.Reserved, sp.b)
		}
	}()
	left := append([]spot(nil), spots...)
	cur, curDir := from, dir
	for len(left) > 0 {
		k := 0
		for i, sp := range left {
			if math.Hypot(float64(sp.b.X-cur.X), float64(sp.b.Y-cur.Y)) < math.Hypot(float64(left[k].b.X-cur.X), float64(left[k].b.Y-cur.Y)) {
				k = i
			}
		}
		sp := left[k]
		left = append(left[:k], left[k+1:]...)
		delete(l.Reserved, sp.b) // место инсертера — занято до его постановки
		ok := false
		for _, bd := range []int{N, E, S, W} {
			next := Step(sp.b, bd)
			if len(left) > 0 && !l.Free(next, AnyLand) {
				continue
			}
			if _, err := l.RouteBelt(cur, curDir, sp.b, bd, tag); err == nil {
				cur, curDir, ok = next, bd, true
				break
			}
		}
		if !ok {
			continue // не подвести — пусть кормит бот
		}
		l.Add(Placement{Name: ins, Pos: sp.in.Center(), Dir: InserterDir(l.P, ins, sp.in, sp.b), Tag: tag})
		fed++
	}
	if fed == 0 {
		return 0, fmt.Errorf("layout: ленту ни к одной кормушке не провести")
	}
	l.AllowEnd(cur)
	l.Reserved[cur] = true // тупик: за ним пусто
	return fed, nil
}
