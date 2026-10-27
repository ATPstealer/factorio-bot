package layout

import (
	"fmt"
	"math"
	"sort"

	"factorio-bot/internal/world"
)

// Добывающие точки начала игры на burner-бурах (стадия 5.0): без лент и инсертеров —
// бур выдаёт прямо в соседа.

// onOre — все тайлы размещения на руде ore и свободны, в зоне добычи — только ore.
func (l *Layout) onOre(pl Placement, ore string) bool {
	if _, mixed := l.OreUnder(pl, ore); mixed {
		return false
	}
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

// spaced — вокруг построек одной точки pls — тайл без чужих построек: проход между точками
// (stages.md, стадия 10: иначе бот то и дело зажат своими печами и бурами).
func (l *Layout) spaced(pls ...Placement) bool {
	own := map[world.Tile]bool{}
	for _, p := range pls {
		for _, t := range Tiles(l.P, p) {
			own[t] = true
		}
	}
	for t := range own {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if n := (world.Tile{X: t.X + dx, Y: t.Y + dy}); !own[n] && l.At(n) != nil {
					return false
				}
			}
		}
	}
	return true
}

// snug — сколько тайлов сразу за зазором вокруг точки pls занято (чужой постройкой) или не
// на руде ore: точка прижата к соседям и краю пятна — пятно заполняется без дыр.
func (l *Layout) snug(ore string, pls ...Placement) int {
	own := map[world.Tile]bool{}
	for _, p := range pls {
		for _, t := range Tiles(l.P, p) {
			own[t] = true
		}
	}
	near := func(t world.Tile) bool { // в зазоре (рядом со своей)
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if own[world.Tile{X: t.X + dx, Y: t.Y + dy}] {
					return true
				}
			}
		}
		return false
	}
	seen := map[world.Tile]bool{}
	n := 0
	for t := range own {
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				q := world.Tile{X: t.X + dx, Y: t.Y + dy}
				if seen[q] || own[q] || near(q) {
					continue
				}
				seen[q] = true
				if l.At(q) != nil {
					n++
				} else if l.W != nil {
					if r, ok := l.W.ResourceAt(q); !ok || r.Name != ore {
						n++
					}
				}
			}
		}
	}
	return n
}

// snugTries — из скольких ближних к цели мест точки выбирать самое прижатое (snug).
const snugTries = 40

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

// CoalPair — два бура на руде ore выходами друг в друга (каждый заправляет другого); из
// ближних к near мест на патче — самое прижатое (snug).
func (l *Layout) CoalPair(patch []world.Tile, ore, drill string, near world.Tile, tag string) error {
	var best []Placement
	bs, tries := -1, 0
	for _, a := range byDistance(patch, near) {
		if tries >= snugTries {
			break
		}
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
			if l.onOre(pa, ore) && l.onOre(pb, ore) && l.spaced(pa, pb) {
				tries++
				if s := l.snug(ore, pa, pb); s > bs {
					best, bs = []Placement{pa, pb}, s
				}
			}
		}
	}
	if best == nil {
		return fmt.Errorf("layout: нет места под пару буров на %s", ore)
	}
	for _, p := range best {
		l.Add(p)
	}
	return nil
}

// DrillFurnace — бур на руде ore выходом в печь (печь рядом, не на буре); из ближних к near
// мест — самое прижатое (snug).
func (l *Layout) DrillFurnace(patch []world.Tile, ore, drill, furnace string, near world.Tile, tag string) error {
	fw, fh := Size(l.P, furnace, N)
	var best []Placement
	bs, tries := -1, 0
	for _, a := range byDistance(patch, near) {
		if tries >= snugTries {
			break
		}
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
					if !l.CanPlace(pf, AnyLand) || overlaps(l, pf, pd) || !l.spaced(pd, pf) {
						continue
					}
					tries++
					if s := l.snug(ore, pd, pf); s > bs {
						best, bs = []Placement{pd, pf}, s
					}
				}
			}
		}
	}
	if best == nil {
		return fmt.Errorf("layout: нет места под бур с печью на %s", ore)
	}
	for _, p := range best {
		l.Add(p)
	}
	return nil
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
			pls := append([]Placement(nil), l.List[mark:]...)
			l.Rollback(mark) // проверка зазора — без своих построек в раскладке
			if l.spaced(pls...) {
				for _, p := range pls {
					l.Add(p)
				}
				return nil
			}
			continue
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
