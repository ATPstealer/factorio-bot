package layout

import (
	"math"

	"factorio-bot/internal/world"
)

// Poles ставит столбы pole: сначала жадно покрывает электрические машины (каждый новый
// столб — где он запитает больше всего непокрытых), потом связывает сети цепочкой столбов
// по маршруту между ближайшими столбами разных сетей.
func (l *Layout) Poles(pole, tag string) int {
	added := 0
	need := func() []int {
		var res []int
		poles := l.poleList()
		for i, p := range l.List {
			e := l.P.Entities[p.Name]
			if e.Electric && !isPole(p.Name) && !l.powered(p, poles) {
				res = append(res, i)
			}
		}
		return res
	}
	r := l.P.Entities[pole].SupplyAreaDistance
	for iter := 0; iter < 500; iter++ {
		un := need()
		if len(un) == 0 {
			break
		}
		// кандидаты — свободные тайлы рядом с первой непокрытой машиной
		target := l.List[un[0]]
		best, bestN := world.Tile{}, 0
		c := world.TileOf(target.Pos)
		R := int(r) + 3
		for dy := -R; dy <= R; dy++ {
			for dx := -R; dx <= R; dx++ {
				t := world.Tile{X: c.X + dx, Y: c.Y + dy}
				if !l.Free(t, AnyLand) {
					continue
				}
				cand := Placement{Name: pole, Pos: t.Center()}
				n := 0
				for _, i := range un {
					if l.powered(l.List[i], []Placement{cand}) {
						n++
					}
				}
				if n > bestN {
					best, bestN = t, n
				}
			}
		}
		if bestN == 0 {
			break // не запитать — Validate скажет
		}
		l.Add(Placement{Name: pole, Pos: best.Center(), Tag: tag})
		added++
	}
	added += l.connectPoles(pole, tag)
	return added
}

// connectPoles — пока сетей больше одной: цепочка столбов от ближайшей пары разных сетей.
func (l *Layout) connectPoles(pole, tag string) int {
	added := 0
	for iter := 0; iter < 50; iter++ {
		poles := l.poleList()
		comp := components(l, poles)
		if maxComp(comp) <= 1 {
			return added
		}
		// ближайшая пара из сети 0 и любой другой
		bi, bj, bd := -1, -1, math.Inf(1)
		for i := range poles {
			for j := range poles {
				if comp[i] == 0 && comp[j] != 0 {
					if d := math.Hypot(poles[i].Pos.X-poles[j].Pos.X, poles[i].Pos.Y-poles[j].Pos.Y); d < bd {
						bi, bj, bd = i, j, d
					}
				}
			}
		}
		n := l.poleChain(poles[bi], poles[bj], pole, tag)
		if n == 0 {
			return added
		}
		added += n
	}
	return added
}

// poleChain — столбы от a до b с наименьшим числом новых: поиск в ширину по местам под
// столб (свободные тайлы и уже стоящие столбы), шаг — не длиннее провода. Провод идёт над
// лентами и постройками, поэтому сплошной проход не нужен.
func (l *Layout) poleChain(a, b Placement, pole, tag string) int {
	wire := l.P.Entities[pole].MaxWireDistance - 0.3
	R := int(wire)
	start, goal := world.TileOf(a.Pos), world.TileOf(b.Pos)
	minX, maxX := min(start.X, goal.X)-20, max(start.X, goal.X)+20
	minY, maxY := min(start.Y, goal.Y)-20, max(start.Y, goal.Y)+20
	spot := func(t world.Tile) bool {
		if t.X < minX || t.X > maxX || t.Y < minY || t.Y > maxY {
			return false
		}
		if l.Free(t, AnyLand) || l.anchorAt(t) {
			return true
		}
		p := l.At(t)
		return p != nil && isPole(p.Name)
	}
	near := func(x, y world.Tile) bool {
		return math.Hypot(float64(x.X-y.X), float64(x.Y-y.Y)) <= wire
	}
	prev := map[world.Tile]world.Tile{start: start}
	q := []world.Tile{start}
	var end *world.Tile
	for len(q) > 0 && end == nil && len(prev) < 60000 {
		t := q[0]
		q = q[1:]
		if near(t, goal) {
			e := t
			end = &e
			break
		}
		for dy := -R; dy <= R; dy++ {
			for dx := -R; dx <= R; dx++ {
				n := world.Tile{X: t.X + dx, Y: t.Y + dy}
				if _, ok := prev[n]; ok || !near(t, n) || !spot(n) {
					continue
				}
				prev[n] = t
				q = append(q, n)
			}
		}
	}
	if end == nil {
		return 0
	}
	added := 0
	for t := *end; t != start; t = prev[t] {
		if p := l.At(t); p != nil && isPole(p.Name) || l.anchorAt(t) {
			continue
		}
		l.Add(Placement{Name: pole, Pos: t.Center(), Tag: tag})
		added++
	}
	return added
}

func components(l *Layout, poles []Placement) []int {
	comp := make([]int, len(poles))
	for i := range comp {
		comp[i] = -1
	}
	n := 0
	var dfs func(i int)
	dfs = func(i int) {
		comp[i] = n
		for j := range poles {
			if comp[j] < 0 && wireOK(l, poles[i], poles[j]) {
				dfs(j)
			}
		}
	}
	for i := range poles {
		if comp[i] < 0 {
			dfs(i)
			n++
		}
	}
	return comp
}

func maxComp(comp []int) int {
	m := 0
	for _, c := range comp {
		m = max(m, c+1)
	}
	return m
}
