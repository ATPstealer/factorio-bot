package layout

import (
	"errors"
	"fmt"

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

// RoutePipeTo — труба от соединения from до любого из соединений to (подвод к блоку цистерн).
func (l *Layout) RoutePipeTo(from Conn, to []Conn, tag string) ([]Placement, error) {
	own := map[world.Tile]bool{from.At: true}
	goals := map[world.Tile]int{}
	for _, c := range to {
		own[c.At] = true
		goals[c.To] = (c.Dir + 8) % 16
	}
	return l.routePipe(from, goals, own, tag)
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
	// путь бывает проходит тайл дважды (came переписан более дешёвым путём уже после проверки
	// used у потомков: подземный выход и труба на одном тайле, 567690334) — как у лент
	// (RouteBelt): тайлы повтора запретить и заново
	var banned []world.Tile
	defer func() {
		for _, t := range banned {
			delete(l.Reserved, t)
		}
	}()
	for try := 0; try < 6; try++ {
		res, err := l.routePipeOnce(from, goals, own, tag)
		if err != nil {
			return nil, err
		}
		seen := map[world.Tile]bool{}
		var dup []world.Tile
		for _, p := range res {
			t := world.TileOf(p.Pos)
			if seen[t] {
				dup = append(dup, t)
			}
			seen[t] = true
		}
		if len(dup) == 0 {
			for _, p := range res {
				l.Add(p)
			}
			return res, nil
		}
		for _, t := range dup {
			if _, goal := goals[t]; t != from.To && !goal && !l.Reserved[t] {
				l.Reserved[t] = true
				banned = append(banned, t)
			}
		}
	}
	return nil, fmt.Errorf("%w: путь проходит тайл дважды", ErrNoPipe)
}

func (l *Layout) routePipeOnce(from Conn, goals map[world.Tile]int, own map[world.Tile]bool, tag string) ([]Placement, error) {
	const pipe, ptg = "pipe", "pipe-to-ground"
	maxUG := l.P.Entities[ptg].MaxUndergroundDistance
	start := from.To
	pipeOK := func(t world.Tile) bool { return l.Free(t, AnyLand) && !l.Solid[t] && !l.fluidNeighbour(t, own) }
	if !pipeOK(start) {
		return nil, fmt.Errorf("%w: начало (%d, %d) закрыто (занято, Solid или чужая труба рядом)", ErrNoPipe, start.X, start.Y)
	}
	// ugBlock — тайл между концами подземной пары (и сам выход), где пары быть не может: другая
	// подземка этой оси (спарятся не так) или Solid; занят k-й от входа — и все дальше
	ugBlock := func(a world.Tile, d int) bool {
		p := l.At(a)
		return p != nil && p.Name == ptg && (p.Dir == d || p.Dir == (d+8)%16) || l.Solid[a]
	}
	// goalOK — подземка, вышедшая на out по d, на цели смотрит туда, куда надо
	goalOK := func(out world.Tile, d int) bool {
		gd, ok := goals[out]
		return !ok || gd == d
	}
	h := func(t world.Tile) float64 {
		m := -1
		for g := range goals {
			if d := abs(t.X-g.X) + abs(t.Y-g.Y); m < 0 || d < m {
				m = d
			}
		}
		return float64(m)
	}
	// состояния поиска — номерами (как у лент: карта структур — больше половины времени
	// нефтетрассы); atStart — подземный вход — сам начальный тайл (вокруг начала — ленты: порт
	// под головой ряда в окружении подводов, 158846952 — смазка электродвигателей)
	type pinfo struct {
		s       rstate
		g       float64
		from    int32
		atStart bool
	}
	s0 := rstate{T: start, D: plain}
	nodes := []pinfo{{s: s0, from: -1}}
	index := map[uint64]int32{stateKey(s0): 0}
	open := &rqueue{}
	open.push(rnode{n: 0, f: h(start)})
	// used — тайл уже занят путём до n (сам путь или вход подземки): A* по состояниям
	// «тайл + направление выхода подземки» иначе может пройти тайл дважды
	used := func(n int32, t world.Tile) bool {
		for ; n != 0; n = nodes[n].from {
			s := nodes[n].s
			if s.T == t || s.D != plain && Step(nodes[nodes[n].from].s.T, s.D) == t {
				return true
			}
		}
		return s0.T == t
	}
	push := func(s rstate, from int32, atStart bool, g float64) {
		key := stateKey(s)
		j, seen := index[key]
		switch {
		case !seen:
			j = int32(len(nodes))
			index[key] = j
			nodes = append(nodes, pinfo{s: s, g: g, from: from, atStart: atStart})
		case g >= nodes[j].g:
			return
		default:
			nodes[j].g, nodes[j].from, nodes[j].atStart = g, from, atStart
		}
		open.push(rnode{n: j, g: g, f: g + h(s.T)})
	}
	plainCost := 1.0
	if l.PipeUnder {
		plainCost = 3 // подземная пара (7 + …) дешевле уже на 3 тайлах
	}
	// предел поиска — по расстоянию (дальняя нефтетрасса — до 2M состояний)
	far := int(h(start)) + 60
	limit := min(max(16*far*far, 200_000), 2_000_000)
	end := int32(-1)
	ugOK := l.usable(ptg)
	probed := false
	for len(*open) > 0 && len(nodes) < limit {
		// долго не находится — может, к целям не подойти вовсе (как у лент: routeProbe; 1098794301 —
		// нефтетрасса 1.7M состояний, 3.5 мин, пути нет)
		if !probed && len(nodes) >= routeProbe {
			probed = true
			if !l.pipeReach(start, goals, maxUG, ugOK, limit/8, pipeOK, ugBlock) {
				return nil, fmt.Errorf("%w: пути нет (состояний %d из %d, заливка от цели)", ErrNoPipe, len(nodes), limit)
			}
		}
		cur := open.pop()
		cs := nodes[cur.n].s
		if cur.g > nodes[cur.n].g {
			continue
		}
		t := cs.T
		if _, ok := goals[t]; ok {
			end = cur.n
			break
		}
		dirs := []int{N, E, S, W}
		if cs.D != plain {
			dirs = []int{cs.D} // выход подземки соединён только вперёд
		}
		if cur.n == 0 && ugOK {
			// подземный вход на самом начале: ход — от from.At через начало, наземное соединение
			// входа — назад, к from.At
			d := -1
			for _, dd := range []int{N, E, S, W} {
				if Step(from.At, dd) == start {
					d = dd
				}
			}
			if d >= 0 {
				out := start
				for k := 1; k <= maxUG; k++ {
					if out = Step(out, d); ugBlock(out, d) {
						break
					}
					if k < 2 || !l.Free(out, AnyLand) || !goalOK(out, d) {
						continue
					}
					push(rstate{T: out, D: d}, cur.n, true, cur.g+7+0.1*float64(k))
				}
			}
		}
		for _, d := range dirs {
			n := Step(t, d)
			if pipeOK(n) && !used(cur.n, n) {
				push(rstate{T: n, D: plain}, cur.n, false, cur.g+plainCost)
			}
			if !ugOK || !l.Free(n, AnyLand) || used(cur.n, n) {
				continue
			}
			out := n
			for k := 1; k <= maxUG; k++ {
				if out = Step(out, d); ugBlock(out, d) {
					break
				}
				if k < 2 || !l.Free(out, AnyLand) || !goalOK(out, d) || used(cur.n, out) {
					continue
				}
				push(rstate{T: out, D: d}, cur.n, false, cur.g+7+0.1*float64(k))
			}
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("%w: пути нет (состояний %d из %d)", ErrNoPipe, len(nodes), limit)
	}
	var res []Placement
	startUG := false // начало — подземный вход (не обычная труба)
	for n := end; n != 0; n = nodes[n].from {
		e := nodes[n]
		s := e.s
		if s.D != plain {
			// вход — сосед прошлого по ходу (или само начало); наземное соединение входа смотрит
			// назад, выхода — вперёд
			in := Step(nodes[e.from].s.T, s.D)
			if e.atStart {
				in, startUG = start, true
			}
			res = append(res,
				Placement{Name: ptg, Pos: s.T.Center(), Dir: s.D, Tag: tag},
				Placement{Name: ptg, Pos: in.Center(), Dir: (s.D + 8) % 16, Tag: tag})
		} else {
			res = append(res, Placement{Name: pipe, Pos: s.T.Center(), Tag: tag})
		}
	}
	if !startUG {
		res = append(res, Placement{Name: pipe, Pos: start.Center(), Tag: tag})
	}
	return res, nil
}

// pipeReach — от start до какой-нибудь цели труба может дойти по правилам ходов поиска, кроме
// направлений и повторов тайла (заливка назад от целей по тайлам): обычная труба — в тайл, где
// ей можно (pipeOK), с соседнего свободного; подземная пара — выход x (свободен, не ugBlock, на
// цели — смотрит куда надо), вход через k = 2…maxUG тайлов (свободен, между — не ugBlock), с
// соседнего перед входом (или вход — само начало). Заливка кончилась, start не встретился — пути
// нет; больше most тайлов — не знаем (true). Ответ поиска не меняется.
func (l *Layout) pipeReach(start world.Tile, goals map[world.Tile]int, maxUG int, ugOK bool, most int,
	pipeOK func(world.Tile) bool, ugBlock func(world.Tile, int) bool) bool {
	seen := map[uint64]bool{}
	var queue []world.Tile
	for g := range goals {
		if g == start {
			return true
		}
		seen[tileKey(g)] = true
		queue = append(queue, g)
	}
	hit := false
	visit := func(p world.Tile) {
		if p == start {
			hit = true
			return
		}
		key := tileKey(p)
		if seen[key] || !l.Free(p, AnyLand) {
			return
		}
		seen[key] = true
		queue = append(queue, p)
	}
	for len(queue) > 0 && !hit {
		if len(seen) > most {
			return true
		}
		x := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, d := range []int{N, E, S, W} { // пришли в x по d
			back := (d + 8) % 16
			if pipeOK(x) {
				visit(Step(x, back)) // обычная труба
			}
			if !ugOK || !l.Free(x, AnyLand) || ugBlock(x, d) {
				continue
			}
			if gd, goal := goals[x]; goal && gd != d {
				continue
			}
			q := x
			for k := 1; k <= maxUG; k++ {
				q = Step(q, back) // вход при длине k
				if k >= 2 && l.Free(q, AnyLand) {
					if q == start {
						hit = true
						break
					}
					visit(Step(q, back))
				}
				if ugBlock(q, d) {
					break // q — между концами дальних пар
				}
			}
		}
	}
	return hit
}
