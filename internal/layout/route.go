package layout

import (
	"container/heap"
	"errors"

	"factorio-bot/internal/world"
)

// ErrNoRoute — ленту не провести.
var ErrNoRoute = errors.New("layout: ленту не провести")

type rstate struct {
	T world.Tile
	D int // направление, в котором пришли (лента предыдущего тайла смотрит сюда)
}

type rnode struct {
	s    rstate
	g, f float64
	i    int
}
type rheap []*rnode

func (h rheap) Len() int           { return len(h) }
func (h rheap) Less(i, j int) bool { return h[i].f < h[j].f }
func (h rheap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].i = i; h[j].i = j }
func (h *rheap) Push(x any)        { n := x.(*rnode); n.i = len(*h); *h = append(*h, n) }
func (h *rheap) Pop() any          { o := *h; n := o[len(o)-1]; *h = o[:len(o)-1]; return n }

type redge struct {
	from rstate
	ug   int // 0 — обычная лента; >0 — подземка длиной ug тайлов под землёй
	dir  int
}

// RouteBelt прокладывает ленту от тайла from (пришли в него с направлением inDir) до
// тайла to, где лента должна смотреть toDir (в цель). Ленты — по свободным тайлам суши,
// руда — со штрафом; препятствие — подземкой (вход и выход свободны, между — что угодно).
func (l *Layout) RouteBelt(from world.Tile, inDir int, to world.Tile, toDir int, tag string) (int, error) {
	// A* различает «тайл + направление» и может пересечь собственный путь — тогда
	// повторить, запретив тайлы самопересечения
	var banned []world.Tile
	defer func() {
		for _, t := range banned {
			delete(l.Reserved, t)
		}
	}()
	for try := 0; try < 6; try++ {
		out, err := l.routeOnce(from, inDir, to, toDir, tag)
		if err != nil {
			return 0, err
		}
		seen := map[world.Tile]bool{}
		var dup []world.Tile
		for _, p := range out {
			t := world.TileOf(p.Pos)
			if seen[t] {
				dup = append(dup, t)
			}
			seen[t] = true
		}
		if len(dup) == 0 {
			for _, p := range out {
				l.Add(p)
			}
			return len(out), nil
		}
		for _, t := range dup {
			if t != from && t != to && !l.Reserved[t] {
				l.Reserved[t] = true
				banned = append(banned, t)
			}
		}
	}
	return 0, ErrNoRoute
}

func (l *Layout) routeOnce(from world.Tile, inDir int, to world.Tile, toDir int, tag string) ([]Placement, error) {
	belt, ug := "transport-belt", "underground-belt"
	maxUG := l.P.Entities[ug].MaxUndergroundDistance
	cost := func(t world.Tile) (float64, bool) {
		if !l.Free(t, AnyLand) {
			return 0, false
		}
		c := 1.0
		if l.W != nil {
			if _, ore := l.W.ResourceAt(t); ore {
				c += 2
			}
			if _, clear := l.W.Buildable(t); clear {
				c += 1
			}
		}
		return c, true
	}
	h := func(t world.Tile) float64 {
		return float64(abs(t.X-to.X) + abs(t.Y-to.Y))
	}
	start := rstate{from, inDir}
	if _, ok := cost(from); !ok {
		return nil, ErrNoRoute
	}
	if _, ok := cost(to); !ok {
		return nil, ErrNoRoute // цель занята
	}
	open := &rheap{}
	best := map[rstate]float64{start: 0}
	came := map[rstate]redge{}
	heap.Push(open, &rnode{s: start, f: h(from)})
	var goal *rstate
	for open.Len() > 0 && len(best) < 300000 {
		cur := heap.Pop(open).(*rnode)
		if cur.g > best[cur.s] {
			continue
		}
		if cur.s.T == to && (cur.s.D+8)%16 != toDir {
			g := cur.s
			goal = &g
			break
		}
		for _, d := range []int{N, E, S, W} {
			if (cur.s.D+8)%16 == d {
				continue // назад нельзя
			}
			// обычная лента на cur.T в направлении d
			nt := Step(cur.s.T, d)
			if c, ok := cost(nt); ok {
				g := cur.g + c
				if d != cur.s.D {
					g += 0.5 // поворот
				}
				ns := rstate{nt, d}
				if old, ok := best[ns]; !ok || g < old {
					best[ns] = g
					came[ns] = redge{from: cur.s, dir: d}
					heap.Push(open, &rnode{s: ns, g: g, f: g + h(nt)})
				}
			}
			// подземка: вход на cur.T (смотрит d), выход через k тайлов
			if d != cur.s.D || !l.usable(ug) {
				continue // вход подземки — без поворота
			}
			for k := 2; k <= maxUG; k++ { // выход не дальше дальности прототипа
				outT := cur.s.T
				for i := 0; i < k; i++ {
					outT = Step(outT, d)
				}
				if _, ok := cost(outT); !ok {
					continue
				}
				after := Step(outT, d)
				if _, ok := cost(after); !ok {
					continue
				}
				g := cur.g + float64(k) + 4
				ns := rstate{after, d}
				if old, ok := best[ns]; !ok || g < old {
					best[ns] = g
					came[ns] = redge{from: cur.s, ug: k, dir: d}
					heap.Push(open, &rnode{s: ns, g: g, f: g + h(after)})
				}
			}
		}
	}
	if goal == nil {
		return nil, ErrNoRoute
	}
	// восстановить: идём от цели назад
	var out []Placement
	out = append(out, Placement{Name: belt, Pos: goal.T.Center(), Dir: toDir, Tag: tag})
	for s := *goal; s != start; {
		e := came[s]
		if e.ug > 0 {
			outT := e.from.T
			for i := 0; i < e.ug; i++ {
				outT = Step(outT, e.dir)
			}
			out = append(out,
				Placement{Name: ug, Pos: outT.Center(), Dir: e.dir, Recipe: "output", Tag: tag},
				Placement{Name: ug, Pos: e.from.T.Center(), Dir: e.dir, Recipe: "input", Tag: tag})
		} else {
			out = append(out, Placement{Name: belt, Pos: e.from.T.Center(), Dir: e.dir, Tag: tag})
		}
		s = e.from
	}
	return out, nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
