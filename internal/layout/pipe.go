package layout

import (
	"container/heap"
	"errors"

	"factorio-bot/internal/world"
)

// ErrNoPipe — трубу не провести.
var ErrNoPipe = errors.New("layout: трубу не провести")

// plain — состояние «на тайле обычная труба» (соединена во все стороны).
const plain = 16

// fluidNeighbour — рядом с t чужое жидкостное соединение, смотрящее на t: обычная труба
// на t с ним соединится и жидкости смешаются. own — свои крайние тайлы (с ними можно).
func (l *Layout) fluidNeighbour(t world.Tile, own map[world.Tile]bool) bool {
	for _, d := range []int{N, E, S, W} {
		n := Step(t, d)
		if own[n] {
			continue
		}
		p := l.At(n)
		if p == nil {
			continue
		}
		for _, c := range Connections(l.P, *p) {
			if c.At == n && c.To == t {
				return true
			}
		}
	}
	return false
}

// RoutePipe прокладывает трубу от соединения from до соединения to (At — крайний тайл
// постройки, To — тайл перед ним, где труба начинается/кончается): A* по свободной суше,
// 4 направления. Обычная труба соединяется во все стороны, поэтому не касается чужих
// жидкостных соединений; под ними и под препятствиями — подземная пара (pipe-to-ground,
// до дальности прототипа: у концов одно наземное соединение, вдоль оси). Подземка дороже
// (пара — 10 труб и 5 железа), но длинная прямая из обычных — стена, через которую не
// пройти: на 8+ тайлах подземка выгоднее — в трубопроводе остаются проходы. Размещения добавлены в раскладку.
func (l *Layout) RoutePipe(from, to Conn, tag string) ([]Placement, error) {
	own := map[world.Tile]bool{from.At: true, to.At: true}
	return l.routePipe(from, map[world.Tile]int{to.To: (to.Dir + 8) % 16}, own, tag)
}

// JoinPipe — труба от соединения from до уже проложенной трубы с тем же tag (та же
// жидкость): к ней можно прикасаться.
func (l *Layout) JoinPipe(from Conn, tag string) ([]Placement, error) {
	own := map[world.Tile]bool{from.At: true}
	var pipes []world.Tile
	for _, p := range l.List {
		if p.Tag == tag && (p.Name == "pipe" || p.Name == "pipe-to-ground") {
			t := world.TileOf(p.Pos)
			own[t] = true
			if p.Name == "pipe" {
				pipes = append(pipes, t)
			}
		}
	}
	goals := map[world.Tile]int{}
	for _, t := range pipes {
		for _, d := range []int{N, E, S, W} {
			if n := Step(t, d); !own[n] && l.Free(n, AnyLand) {
				goals[n] = (d + 8) % 16
			}
		}
	}
	if len(goals) == 0 {
		return nil, ErrNoPipe
	}
	return l.routePipe(from, goals, own, tag)
}

// routePipe — A* от from.To до любого тайла goals (значение — куда должен смотреть выход
// подземки, пришедшей на этот тайл: на постройку/трубу). own — тайлы, которых можно касаться.
func (l *Layout) routePipe(from Conn, goals map[world.Tile]int, own map[world.Tile]bool, tag string) ([]Placement, error) {
	const pipe, ptg = "pipe", "pipe-to-ground"
	maxUG := l.P.Entities[ptg].MaxUndergroundDistance
	start := from.To
	pipeOK := func(t world.Tile) bool { return l.Free(t, AnyLand) && !l.fluidNeighbour(t, own) }
	if !pipeOK(start) {
		return nil, ErrNoPipe
	}
	// ugClear — между концами подземной пары нет других подземок на этой оси (спарятся не так)
	ugClear := func(a world.Tile, d, k int) bool {
		for i := 1; i <= k; i++ {
			a = Step(a, d)
			if p := l.At(a); p != nil && p.Name == ptg && (p.Dir == d || p.Dir == (d+8)%16) {
				return false
			}
		}
		return true
	}
	// goalOK — подземка, вышедшая на out по d, на цели смотрит туда, куда надо
	goalOK := func(out world.Tile, d int) bool {
		gd, ok := goals[out]
		return !ok || gd == d
	}
	type edge struct{ from rstate }
	h := func(t world.Tile) float64 {
		m := -1
		for g := range goals {
			if d := abs(t.X-g.X) + abs(t.Y-g.Y); m < 0 || d < m {
				m = d
			}
		}
		return float64(m)
	}
	s0 := rstate{start, plain}
	best := map[rstate]float64{s0: 0}
	came := map[rstate]edge{}
	open := &rheap{}
	heap.Push(open, &rnode{s: s0, f: h(start)})
	// used — тайл уже занят путём до s (сам путь или вход подземки): A* по состояниям
	// «тайл + направление выхода подземки» иначе может пройти тайл дважды
	used := func(s rstate, t world.Tile) bool {
		for s != s0 {
			e := came[s]
			if s.T == t || s.D != plain && Step(e.from.T, s.D) == t {
				return true
			}
			s = e.from
		}
		return s0.T == t
	}
	push := func(s rstate, e edge, g float64) {
		if old, seen := best[s]; seen && g >= old {
			return
		}
		best[s], came[s] = g, e
		heap.Push(open, &rnode{s: s, g: g, f: g + h(s.T)})
	}
	var end rstate
	found := false
	for open.Len() > 0 && len(best) < 200000 {
		cur := heap.Pop(open).(*rnode)
		if cur.g > best[cur.s] {
			continue
		}
		t := cur.s.T
		if _, ok := goals[t]; ok {
			end, found = cur.s, true
			break
		}
		dirs := []int{N, E, S, W}
		if cur.s.D != plain {
			dirs = []int{cur.s.D} // выход подземки соединён только вперёд
		}
		for _, d := range dirs {
			n := Step(t, d)
			if pipeOK(n) && !used(cur.s, n) {
				push(rstate{n, plain}, edge{from: cur.s}, cur.g+1)
			}
			if !l.usable(ptg) || !l.Free(n, AnyLand) || used(cur.s, n) {
				continue
			}
			out := n
			for k := 1; k <= maxUG; k++ {
				out = Step(out, d)
				if k < 2 || !l.Free(out, AnyLand) || !goalOK(out, d) || !ugClear(n, d, k) || used(cur.s, out) {
					continue
				}
				push(rstate{out, d}, edge{from: cur.s}, cur.g+7+0.1*float64(k))
			}
		}
	}
	if !found {
		return nil, ErrNoPipe
	}
	var res []Placement
	for s := end; s != s0; {
		e := came[s]
		if s.D != plain {
			// вход — сосед e.from по ходу; наземное соединение входа смотрит назад, выхода — вперёд
			in := Step(e.from.T, s.D)
			res = append(res,
				Placement{Name: ptg, Pos: s.T.Center(), Dir: s.D, Tag: tag},
				Placement{Name: ptg, Pos: in.Center(), Dir: (s.D + 8) % 16, Tag: tag})
		} else {
			res = append(res, Placement{Name: pipe, Pos: s.T.Center(), Tag: tag})
		}
		s = e.from
	}
	res = append(res, Placement{Name: pipe, Pos: start.Center(), Tag: tag})
	for _, p := range res {
		l.Add(p)
	}
	return res, nil
}
