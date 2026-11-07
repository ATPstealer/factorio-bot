package layout

import (
	"errors"

	"factorio-bot/internal/world"
)

// ErrNoRoute — ленту не провести.
var ErrNoRoute = errors.New("layout: ленту не провести")

type rstate struct {
	T world.Tile
	D int // направление, в котором пришли (лента предыдущего тайла смотрит сюда)
	// UG — пришли сразу из выхода подземки: новую подземку отсюда не начать (игра спарит
	// как попало). В состоянии — иначе равноценный приход обычной лентой отбрасывался бы,
	// и подземка под следующей парой лент не находилась
	UG bool
}

// turnCost — штраф за поворот ленты: обход на тайл в сторону дороже, чем прямо.
const turnCost = 3.0

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

// tileKey, stateKey — ключи карт поиска числом (быстрее структуры: поиск лент — больше половины
// времени плана, почти всё — в картах). Координаты — до ±2^28, направление — до 31 (труба без подземки — plain).
func tileKey(t world.Tile) uint64 { return uint64(uint32(t.X))<<32 | uint64(uint32(t.Y)) }

func stateKey(s rstate) uint64 {
	k := uint64(uint32(t29(s.T.X)))<<35 | uint64(uint32(t29(s.T.Y)))<<6 | uint64(s.D&31)<<1
	if s.UG {
		k |= 1
	}
	return k
}

func t29(x int) int { return x & (1<<29 - 1) }

// rnode — узел открытого списка поиска ленты и трубы: n — номер состояния.
type rnode struct {
	n    int32
	g, f float64
}

// rinfo — состояние поиска ленты: лучшая цена и откуда пришли (from — номер состояния, −1 у начала;
// ug — длина подземки под землёй, 0 — обычная лента).
type rinfo struct {
	s    rstate
	g    float64
	from int32
	ug   int8
	dir  int8
}

// rqueue — двоичная куча узлов по f: тот же порядок, что container/heap (Push — up, Pop — swap
// с последним и down), без выделений на узел.
type rqueue []rnode

func (q *rqueue) push(n rnode) {
	*q = append(*q, n)
	h := *q
	for j := len(h) - 1; ; {
		i := (j - 1) / 2
		if i == j || !(h[j].f < h[i].f) {
			break
		}
		h[i], h[j] = h[j], h[i]
		j = i
	}
}

func (q *rqueue) pop() rnode {
	h := *q
	n := len(h) - 1
	h[0], h[n] = h[n], h[0]
	for i := 0; ; {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && h[j2].f < h[j1].f {
			j = j2
		}
		if !(h[j].f < h[i].f) {
			break
		}
		h[i], h[j] = h[j], h[i]
		i = j
	}
	top := h[n]
	*q = h[:n]
	return top
}

func (l *Layout) routeOnce(from world.Tile, inDir int, to world.Tile, toDir int, tag string) ([]Placement, error) {
	belt, ug := "transport-belt", "underground-belt"
	maxUG := l.P.Entities[ug].MaxUndergroundDistance
	// стоимость тайла — одна на поиск (раскладка за поиск не меняется), а спрашивают её на каждом
	// шаге и у каждой подземки: запомнить (−1 — нельзя)
	costMemo := map[uint64]float64{}
	cost := func(t world.Tile) (float64, bool) {
		key := tileKey(t)
		if c, ok := costMemo[key]; ok {
			return c, c >= 0
		}
		if !l.Free(t, AnyLand) || t != from && l.beltInto(t) {
			costMemo[key] = -1
			return 0, false
		}
		c := 1.0 + l.Costly[t]
		if l.W != nil {
			if _, ore := l.W.ResourceAt(t); ore {
				c += 2
			}
			// убираемое (деревья, камни, обломки) — как свободное: убрать и класть ровно
			// (правило пользователя), а не обходить змейкой
		}
		costMemo[key] = c
		return c, true
	}
	// дальний маршрут (лента с нефти на базу — сотни тайлов через лес): взвешенная оценка —
	// поиск идёт к цели, а не расползается по равноценным обходам; путь почти кратчайший
	w := 1.0
	if abs(from.X-to.X)+abs(from.Y-to.Y) > 100 {
		w = 2
	}
	h := func(t world.Tile) float64 {
		return w * float64(abs(t.X-to.X)+abs(t.Y-to.Y))
	}
	start := rstate{from, inDir, false}
	if _, ok := cost(from); !ok {
		return nil, ErrNoRoute
	}
	if _, ok := cost(to); !ok {
		return nil, ErrNoRoute // цель занята
	}
	open := &rqueue{}
	nodes := []rinfo{{s: start, from: -1}}
	index := map[uint64]int32{stateKey(start): 0}
	// relax — путь в ns дешевле известного: запомнить и в открытый список
	relax := func(ns rstate, g, f float64, from int32, ug, dir int) {
		key := stateKey(ns)
		j, ok := index[key]
		if !ok {
			j = int32(len(nodes))
			index[key] = j
			nodes = append(nodes, rinfo{s: ns, g: g, from: from, ug: int8(ug), dir: int8(dir)})
		} else if g < nodes[j].g {
			nodes[j].g, nodes[j].from, nodes[j].ug, nodes[j].dir = g, from, int8(ug), int8(dir)
		} else {
			return
		}
		open.push(rnode{n: j, g: g, f: f})
	}
	open.push(rnode{n: 0, f: h(from)})
	goal := int32(-1)
	ugOK := l.usable(ug)
	// предел поиска — по расстоянию: пути нет — короткий маршрут (отвод с шины) не обходит всю
	// открытую карту (12 мин на попытку); дальний — как раньше, до 1.5M состояний
	d := abs(from.X-to.X) + abs(from.Y-to.Y) + 60
	limit := min(max(16*d*d, 100_000), 1_500_000)
	probed := false
	for len(*open) > 0 && len(nodes) < limit {
		// долго не находится — может, к цели не подойти вовсе: заливка назад от цели (routeProbe)
		if !probed && len(nodes) >= routeProbe {
			probed = true
			if !l.reachBack(from, to, maxUG, ugOK, limit/8, cost) {
				return nil, ErrNoRoute
			}
		}
		cur := open.pop()
		cs := nodes[cur.n].s
		if cur.g > nodes[cur.n].g {
			continue
		}
		if cs.T == to && (cs.D+8)%16 != toDir {
			goal = cur.n
			break
		}
		for _, d := range []int{N, E, S, W} {
			if (cs.D+8)%16 == d {
				continue // назад нельзя
			}
			// обычная лента на cur.T в направлении d
			nt := Step(cs.T, d)
			if c, ok := cost(nt); ok {
				g := cur.g + c
				if d != cs.D {
					g += turnCost // поворот: лента — ровно, без змейки
				}
				relax(rstate{nt, d, false}, g, g+h(nt), cur.n, 0, d)
			}
			// подземка: вход на cur.T (смотрит d), выход через k тайлов
			if d != cs.D || !ugOK {
				continue // вход подземки — без поворота
			}
			if cs.UG {
				continue // вход сразу за выходом другой — игра спаривает их как попало
			}
			// вход не внутри чужой пары — одно на все k
			if !l.ugBehindClear(cs.T, d, maxUG) {
				continue
			}
			outT := Step(cs.T, d)
			for k := 2; k <= maxUG; k++ { // выход не дальше дальности прототипа
				// между концами — тайлы 1…k−1: занят k−1 — и все дальше
				if l.ugAxis(outT, d) != nil || l.Solid[outT] {
					break
				}
				outT = Step(outT, d)
				if _, ok := cost(outT); !ok {
					continue
				}
				if !l.ugAheadClear(outT, d, maxUG) {
					continue
				}
				after := Step(outT, d)
				if _, ok := cost(after); !ok {
					continue
				}
				// подземка — пара ≈ 17 железа против 1.5 за тайл ленты: только где обход
				// заметно длиннее (вода, постройки, густой лес)
				g := cur.g + float64(k) + 8
				relax(rstate{after, d, true}, g, g+h(after), cur.n, k, d)
			}
		}
	}
	if goal < 0 {
		return nil, ErrNoRoute
	}
	// восстановить: идём от цели назад
	var out []Placement
	out = append(out, Placement{Name: belt, Pos: nodes[goal].s.T.Center(), Dir: toDir, Tag: tag})
	for n := goal; nodes[n].from >= 0; n = nodes[n].from {
		e := nodes[n]
		prev := nodes[e.from].s
		dir := int(e.dir)
		if e.ug > 0 {
			outT := prev.T
			for i := 0; i < int(e.ug); i++ {
				outT = Step(outT, dir)
			}
			out = append(out,
				Placement{Name: ug, Pos: outT.Center(), Dir: dir, Recipe: "output", Tag: tag},
				Placement{Name: ug, Pos: prev.T.Center(), Dir: dir, Recipe: "input", Tag: tag})
		} else {
			out = append(out, Placement{Name: belt, Pos: prev.T.Center(), Dir: dir, Tag: tag})
		}
	}
	return out, nil
}

// routeProbe — столько состояний поиска ленты без цели — и заливка назад (reachBack): неудачный
// поиск перебирал до предела (до 1.5M состояний, десятки на попытку плана — две трети её времени),
// а цель обычно заперта в малом кармане (порт кластера, обступленный лентами и Solid: 567690334 —
// к порту кирпича приходят с двух тайлов, поиск — 590 тыс. состояний, 45 с).
const routeProbe = 30_000

// reachBack — от from до to можно дойти по правилам ходов поиска, кроме направлений (заливка
// назад по тайлам): лента — с соседнего тайла, где ей можно (cost); подземка — с входа a через
// k = 2…maxUG тайлов: выход и вход — где ленте можно, между ними — ни подземок этой оси, ни
// Solid, позади входа и впереди выхода — не чужие пары (ugBehindClear, ugAheadClear). Заливка от
// to кончилась, а from не встретился — пути нет; больше most тайлов — не знаем (true). Всё, куда
// доходит поиск, заливка тоже покрывает: ответ поиска не меняется.
func (l *Layout) reachBack(from, to world.Tile, maxUG int, ugOK bool, most int, cost func(world.Tile) (float64, bool)) bool {
	seen := map[uint64]bool{tileKey(to): true}
	queue := []world.Tile{to}
	hit := false
	// visit — с тайла p можно прийти: в заливку
	visit := func(p world.Tile) {
		if p == from {
			hit = true
			return
		}
		key := tileKey(p)
		if seen[key] {
			return
		}
		if _, ok := cost(p); ok {
			seen[key] = true
			queue = append(queue, p)
		}
	}
	for len(queue) > 0 && !hit {
		if len(seen) > most {
			return true
		}
		x := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, d := range []int{N, E, S, W} { // пришли в x по d
			back := (d + 8) % 16
			outT := Step(x, back)
			visit(outT) // лента
			if !ugOK {
				continue
			}
			// подземка: выход outT, вход a = outT − k·d
			if _, ok := cost(outT); !ok || !l.ugAheadClear(outT, d, maxUG) {
				continue
			}
			m := outT
			for k := 2; k <= maxUG; k++ {
				if m = Step(m, back); l.ugAxis(m, d) != nil || l.Solid[m] {
					break // между концами: тайл outT−(k−1)
				}
				if a := Step(m, back); l.ugBehindClear(a, d, maxUG) {
					visit(a)
				}
			}
		}
	}
	return hit
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// beltInto — в тайл t смотрит конец чужой ленты, выход подземки или выход делителя (в
// раскладке или уже стоящих): лента, проложенная через t, примет их груз (медь со старого ряда
// — на ленту железа; сера с нижнего выхода делителя — на ленту кирпича, 427965634).
func (l *Layout) beltInto(t world.Tile) bool {
	for _, d := range []int{N, E, S, W} {
		n := Step(t, d)
		back := (d + 8) % 16 // лента в n должна смотреть сюда
		if p := l.At(n); p != nil && (isBelt(p.Name) || isSplitter(p.Name) || isUnderground(p.Name) && p.Recipe == "output") && p.Dir == back {
			return true
		}
		if l.W == nil {
			continue
		}
		// ленты и подземки на n, делители у n (центр — на границе двух тайлов поперёк хода)
		for _, o := range l.W.BeltsAt(n) {
			if o.Direction == back {
				return true
			}
		}
	}
	return false
}

// Подземка ленты (вход a, выход через k тайлов по d) не спарится с чужими: между
// её концами нет подземок этой оси (и Solid), вход не внутри чужой пары (позади ближайшая
// подземка оси — не вход по d — ugBehindClear), выход тоже (впереди ближайшая — не выход по d —
// ugAheadClear). Поиск ленты проверяет по частям: позади — одно на все k, между — по тайлу.

// ugAxis — подземка ленты оси d на тайле t (или nil).
func (l *Layout) ugAxis(t world.Tile, d int) *Placement {
	if p := l.At(t); p != nil && p.Name == "underground-belt" && p.Dir%8 == d%8 {
		return p
	}
	return nil
}

// ugBehindClear — позади входа a ближайшая подземка оси — не вход по d.
func (l *Layout) ugBehindClear(a world.Tile, d, maxUG int) bool {
	for i, t := 1, a; i <= maxUG+1; i++ {
		t = Step(t, (d+8)%16)
		if p := l.ugAxis(t, d); p != nil {
			return !(p.Dir == d && p.Recipe == "input")
		}
	}
	return true
}

// ugAheadClear — впереди выхода out ближайшая подземка оси — не выход по d.
func (l *Layout) ugAheadClear(out world.Tile, d, maxUG int) bool {
	for i, t := 1, out; i <= maxUG+1; i++ {
		t = Step(t, d)
		if p := l.ugAxis(t, d); p != nil {
			return !(p.Dir == d && p.Recipe == "output")
		}
	}
	return true
}
