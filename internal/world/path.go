package world

import (
	"container/heap"
	"errors"
	"math"
	"sort"

	"factorio-bot/internal/bridge"
)

// ErrNoPath — до цели по открытой проходимой карте не дойти. Route тогда возвращает путь
// к ближайшей к цели достижимой точке (если она не стартовая).
var ErrNoPath = errors.New("world: пути нет")

// Навигация — на сетке полутайлов (ячейка 0.5×0.5) по настоящим рамкам коллизий:
// дерево 0.8×0.8 не по сетке тайлов закрывает до 4 тайлов целиком, и лес на тайловой
// сетке — сплошная стена, хотя персонаж (0.4×0.4) между стволами проходит.
const (
	cellSize = 0.5
	charHalf = 0.2  // полуширина персонажа
	margin   = 0.08 // запас: ходьба клавишами чуть виляет вокруг линии пути
)

// Cell — ячейка сетки полутайлов.
type Cell struct{ X, Y int }

func CellOf(p bridge.Pos) Cell {
	return Cell{int(math.Floor(p.X / cellSize)), int(math.Floor(p.Y / cellSize))}
}

func (c Cell) Center() bridge.Pos {
	return bridge.Pos{X: (float64(c.X) + 0.5) * cellSize, Y: (float64(c.Y) + 0.5) * cellSize}
}

func (c Cell) tile() Tile { return TileOf(c.Center()) }

// cellInfo — чем занята ячейка: hard — не дерево (обходить), trees — деревья (можно
// срубить), core — центр ячейки в рамке без запаса (персонажу там не стоять никогда).
type cellInfo struct {
	hard, core bool
	trees      []int // индексы в Chunk.Objects
	belt       bool  // на ленте: пройти можно, но лента сносит (идти вдоль — дорого)
}

// cellSet — ячейки, занятые объектами чанка (рамки, раздутые на полуширину персонажа).
func (w *World) cellSet(c *Chunk) map[Cell]*cellInfo {
	if c.cells != nil {
		return c.cells
	}
	c.cells = map[Cell]*cellInfo{}
	for i, o := range c.Objects {
		if o.Type == "transport-belt" { // по лентам персонаж ходит — с надбавкой
			t := TileOf(o.Pos)
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					cl := Cell{2*t.X + dx, 2*t.Y + dy}
					if c.cells[cl] == nil {
						c.cells[cl] = &cellInfo{}
					}
					c.cells[cl].belt = true
				}
			}
			continue
		}
		if noCollision[o.Type] {
			continue
		}
		b := w.CollisionBox(o)
		bb := b.Bounds()
		r := charHalf + margin
		for y := int(math.Floor((bb.LeftTop.Y - r) / cellSize)); float64(y)*cellSize < bb.RightBottom.Y+r; y++ {
			for x := int(math.Floor((bb.LeftTop.X - r) / cellSize)); float64(x)*cellSize < bb.RightBottom.X+r; x++ {
				cl := Cell{x, y}
				p := cl.Center()
				if !b.Contains(p, r) {
					continue
				}
				in := c.cells[cl]
				if in == nil {
					in = &cellInfo{}
					c.cells[cl] = in
				}
				if b.Contains(p, charHalf) {
					in.core = true
				}
				if o.Type == "tree" {
					in.trees = append(in.trees, i)
				} else {
					in.hard = true
				}
			}
		}
	}
	return c.cells
}

// cellState — проходимость ячейки: free; иначе hard (не пройти) или деревья (с их
// объектами). Объект у края чанка задевает ячейки соседнего — смотрим соседей.
func (w *World) cellState(cl Cell) (free, hard, core bool, trees []Object) {
	if w.Stuck[cl] {
		return false, true, true, nil
	}
	switch w.TileAt(cl.tile()) {
	case Land, Shallow:
	default:
		return false, true, true, nil
	}
	cp := ChunkOf(cl.tile())
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			ch := w.Chunks[bridge.ChunkPos{X: cp.X + dx, Y: cp.Y + dy}]
			if ch == nil {
				continue
			}
			in := w.cellSet(ch)[cl]
			if in == nil {
				continue
			}
			hard = hard || in.hard
			core = core || in.core
			for _, i := range in.trees {
				trees = append(trees, ch.Objects[i])
			}
		}
	}
	return !hard && len(trees) == 0, hard, core, trees
}

// Without — выполнить fn, будто объекта o нет на карте (проверить путь сквозь свою постройку).
func (w *World) Without(o Object, fn func()) {
	c := w.Chunks[ChunkOf(TileOf(o.Pos))]
	idx := -1
	if c != nil {
		for i, x := range c.Objects {
			if x.Name == o.Name && x.Pos == o.Pos {
				idx = i
			}
		}
	}
	if idx < 0 {
		fn()
		return
	}
	saved := c.Objects
	c.Objects = append(append([]Object(nil), saved[:idx]...), saved[idx+1:]...)
	c.blocked, c.hard, c.solid, c.cells = nil, nil, nil, nil
	defer func() {
		c.Objects = saved
		c.blocked, c.hard, c.solid, c.cells = nil, nil, nil, nil
	}()
	fn()
}

// MarkStuck — персонаж в at упёрся, идя по (dx, dy), во что-то, чего модель не видит (щель
// между утёсами уже, чем по рамкам): ячейки впереди считать непроходимыми.
func (w *World) MarkStuck(at bridge.Pos, dx, dy float64) {
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
	}
	if w.Stuck == nil {
		w.Stuck = map[Cell]bool{}
	}
	c := bridge.Pos{X: at.X + dx/l*0.8, Y: at.Y + dy/l*0.8}
	start := CellOf(at)
	for y := c.Y - 0.6; y <= c.Y+0.6; y += 0.25 {
		for x := c.X - 0.6; x <= c.X+0.6; x += 0.25 {
			if cl := CellOf(bridge.Pos{X: x, Y: y}); cl != start {
				w.Stuck[cl] = true
			}
		}
	}
}

// Cut — дерево на маршруте: срубить, дойдя до отметки S (длина пути от старта).
type Cut struct {
	Tree Object
	S    float64
}

// Route — маршрут: точки (первая — старт) и деревья, которые по дороге рубить.
type Route struct {
	Points []bridge.Pos
	Cuts   []Cut
}

// Length — длина маршрута.
func (r Route) Length() float64 {
	l := 0.0
	for i := 1; i < len(r.Points); i++ {
		l += dist(r.Points[i-1], r.Points[i])
	}
	return l
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

type node struct {
	c    Cell
	g, f float64
	i    int
}

type openList []*node

func (o openList) Len() int           { return len(o) }
func (o openList) Less(i, j int) bool { return o[i].f < o[j].f }
func (o openList) Swap(i, j int)      { o[i], o[j] = o[j], o[i]; o[i].i = i; o[j].i = j }
func (o *openList) Push(x any)        { n := x.(*node); n.i = len(*o); *o = append(*o, n) }
func (o *openList) Pop() any          { old := *o; n := old[len(old)-1]; *o = old[:len(old)-1]; return n }

func octile(dx, dy float64) float64 {
	dx, dy = math.Abs(dx), math.Abs(dy)
	return math.Max(dx, dy) + (math.Sqrt2-1)*math.Min(dx, dy)
}

// Path — путь в обход всего (деревья — препятствия): точки после старта.
func (w *World) Path(from, to bridge.Pos, near float64, maxNodes int) ([]bridge.Pos, error) {
	r, err := w.Route(from, to, near, maxNodes, 0)
	if len(r.Points) < 2 {
		return nil, err
	}
	return r.Points[1:], err
}

// Route ищет маршрут от from до точки ближе near к to: A* по ячейкам в 8 направлениях,
// затем спрямление отрезками под любым углом там, где их видно напрямую. treeCost > 0 —
// деревья проходимы с надбавкой treeCost за ячейку (дерево ≈ 3 ячейки поперёк): их
// придётся срубить по дороге (Route.Cuts). Пути нет — маршрут к ближайшей достижимой
// точке и ErrNoPath.
func (w *World) Route(from, to bridge.Pos, near float64, maxNodes int, treeCost float64) (Route, error) {
	if maxNodes <= 0 {
		maxNodes = 400_000
	}
	// стоим в щели уже ячейки (между постройками): сначала выбраться точным поиском
	origin := from
	var prefix []bridge.Pos
	if free, _, _, _ := w.cellState(CellOf(from)); !free {
		if pts, ok := w.escape(from); ok {
			prefix, from = pts, pts[len(pts)-1]
		}
	}
	start := CellOf(from)
	near = math.Max(near, 0.36) // до центра ячейки от любой её точки
	// у старта персонаж может стоять вплотную к стволу (в запасе рамки) — там можно
	// всё, кроме самой рамки
	nearStart := func(c Cell) bool { return abs(c.X-start.X) <= 1 && abs(c.Y-start.Y) <= 1 }
	// персонаж может стоять краем на тайле воды (у берега) — его тайл проходим
	startTile := TileOf(from)
	pass := func(c Cell) (bool, float64) {
		free, hard, core, _ := w.cellState(c)
		switch {
		case c == start, nearStart(c) && !core:
			return true, 0
		case free && w.onBelt(c):
			return true, beltCost
		case free:
			return true, 0
		case nearStart(c) && c.tile() == startTile && w.TileAt(startTile) == Water:
			return true, 0
		case !hard && treeCost > 0:
			return true, treeCost
		case w.Squeeze && hard && !core:
			return true, 3 // запас у рамки: протиснуться можно, но дорого
		}
		return false, 0
	}
	h := func(c Cell) float64 {
		p := c.Center()
		return math.Max(0, octile(to.X-p.X, to.Y-p.Y)-near)
	}

	open := &openList{}
	nodes := map[Cell]*node{}
	came := map[Cell]Cell{}
	closed := map[Cell]bool{}
	s := &node{c: start, f: h(start)}
	nodes[start] = s
	heap.Push(open, s)
	// персонаж может стоять в щели уже ячейки сетки (между инсертером и печью): из его
	// настоящей позиции — сразу в ячейки рядом, если до их центра прямой проход
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			n := Cell{start.X + dx, start.Y + dy}
			if n == start {
				continue
			}
			if ok, extra := pass(n); !ok || extra > 0 || !w.clearSegment(from, n.Center()) {
				continue
			}
			g := dist(from, n.Center())
			nn := &node{c: n, g: g, f: g + h(n)}
			nodes[n] = nn
			came[n] = start
			heap.Push(open, nn)
		}
	}
	best, bestD := start, dist(start.Center(), to)
	var goal *Cell
	for open.Len() > 0 {
		cur := heap.Pop(open).(*node)
		if closed[cur.c] {
			continue
		}
		closed[cur.c] = true
		d := dist(cur.c.Center(), to)
		if d <= near {
			g := cur.c
			goal = &g
			break
		}
		if d < bestD {
			best, bestD = cur.c, d
		}
		if len(closed) > maxNodes {
			break
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				n := Cell{cur.c.X + dx, cur.c.Y + dy}
				if closed[n] {
					continue
				}
				ok, extra := pass(n)
				if !ok {
					continue
				}
				cost := cellSize
				if dx != 0 && dy != 0 {
					// по диагонали — только если оба соседа по прямой проходимы (не срезать угол)
					if a, _ := pass(Cell{cur.c.X + dx, cur.c.Y}); !a {
						continue
					}
					if b, _ := pass(Cell{cur.c.X, cur.c.Y + dy}); !b {
						continue
					}
					cost *= math.Sqrt2
				}
				g := cur.g + cost + extra
				if old, ok := nodes[n]; ok && old.g <= g {
					continue
				}
				nn := &node{c: n, g: g, f: g + h(n)}
				nodes[n] = nn
				came[n] = cur.c
				heap.Push(open, nn)
			}
		}
	}
	var err error
	if goal == nil {
		if best == start {
			return Route{}, ErrNoPath
		}
		goal, err = &best, ErrNoPath
	}
	cells := chain(start, *goal, came)
	// деревья, через которые идёт путь: рубить их; сквозь них можно и спрямлять
	cut := map[bridge.Pos]Object{}
	for _, c := range cells[1:] {
		if free, _, _, trees := w.cellState(c); !free {
			for _, t := range trees {
				cut[t.Pos] = t
			}
		}
	}
	r := Route{Points: w.smooth(from, cells, cut, nearStart)}
	if len(prefix) > 0 {
		r.Points = append(append([]bridge.Pos{origin}, prefix...), r.Points[1:]...)
	}
	for _, t := range cut {
		r.Cuts = append(r.Cuts, Cut{Tree: t, S: r.along(t.Pos)})
	}
	sort.Slice(r.Cuts, func(i, j int) bool { return r.Cuts[i].S < r.Cuts[j].S })
	return r, err
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// chain — ячейки пути от start до goal.
func chain(start, goal Cell, came map[Cell]Cell) []Cell {
	var cells []Cell
	for c := goal; ; c = came[c] {
		cells = append(cells, c)
		if c == start {
			break
		}
	}
	for i, j := 0, len(cells)-1; i < j; i, j = i+1, j-1 {
		cells[i], cells[j] = cells[j], cells[i]
	}
	return cells
}

// smooth — спрямление: от точки к самой дальней ячейке пути, видной напрямую (отрезок
// не задевает занятых ячеек, кроме деревьев из cut). Первая точка — сам старт.
func (w *World) smooth(from bridge.Pos, cells []Cell, cut map[bridge.Pos]Object, nearStart func(Cell) bool) []bridge.Pos {
	ok := func(c Cell) bool {
		free, hard, core, trees := w.cellState(c)
		if free || (nearStart(c) && !core) {
			return true
		}
		if hard {
			return false
		}
		for _, t := range trees {
			if _, in := cut[t.Pos]; !in {
				return false
			}
		}
		return true
	}
	see := func(a, b bridge.Pos) bool {
		n := int(dist(a, b)/0.1) + 1
		step, run := dist(a, b)/float64(n), 0.0
		for i := 0; i <= n; i++ {
			f := float64(i) / float64(n)
			c := CellOf(bridge.Pos{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f})
			if !ok(c) {
				return false
			}
			// поперёк ленты — можно, вдоль (дольше 1.5 тайла подряд) — нет: снесёт
			if w.onBelt(c) {
				if run += step; run > 1.5 {
					return false
				}
			} else {
				run = 0
			}
		}
		return true
	}
	pts := []bridge.Pos{from}
	cur := from
	for i := 1; i < len(cells); {
		j := len(cells) - 1
		for ; j > i; j-- {
			if see(cur, cells[j].Center()) {
				break
			}
		}
		cur = cells[j].Center()
		pts = append(pts, cur)
		i = j + 1
	}
	return pts
}

// along — отметка на маршруте, ближайшая к точке p.
func (r Route) along(p bridge.Pos) float64 {
	best, bestD, s := 0.0, math.Inf(1), 0.0
	for i := 1; i < len(r.Points); i++ {
		a, b := r.Points[i-1], r.Points[i]
		l := dist(a, b)
		t := 0.0
		if l > 0 {
			t = math.Max(0, math.Min(l, ((p.X-a.X)*(b.X-a.X)+(p.Y-a.Y)*(b.Y-a.Y))/l))
		}
		q := bridge.Pos{X: a.X + (b.X-a.X)*t/math.Max(l, 1e-9), Y: a.Y + (b.Y-a.Y)*t/math.Max(l, 1e-9)}
		if d := dist(p, q); d < bestD {
			best, bestD = s+t, d
		}
		s += l
	}
	return best
}

// At — точка маршрута на отметке s (за концом — последняя).
func (r Route) At(s float64) bridge.Pos {
	for i := 1; i < len(r.Points); i++ {
		a, b := r.Points[i-1], r.Points[i]
		l := dist(a, b)
		if s <= l && l > 0 {
			return bridge.Pos{X: a.X + (b.X-a.X)*s/l, Y: a.Y + (b.Y-a.Y)*s/l}
		}
		s -= l
	}
	return r.Points[len(r.Points)-1]
}

// Tight — рядом с точкой (в пределах тайла) есть препятствия: держать линию пути точно.
func (w *World) Tight(p bridge.Pos) bool {
	c := CellOf(p)
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if free, _, _, _ := w.cellState(Cell{c.X + dx, c.Y + dy}); !free {
				return true
			}
		}
	}
	return false
}

// Standable — персонаж может стоять в точке: ячейка свободна и не на ленте (унесёт).
func (w *World) Standable(p bridge.Pos) bool {
	free, _, _, _ := w.cellState(CellOf(p))
	return free && !w.TouchesBelt(p)
}

// beltReach — полуширина рамки персонажа (0.2) с запасом 0.05: задевает ленту — лента уносит
// (0.3 — слишком: точку в 0.3 от края ленты бот обходил стороной, TestWalkAgainstBelt).
const beltReach = 0.25

// TouchesBelt — персонаж в точке p задевает ленту рамкой (не только центром): точка стоянки на
// границе прохода и ленты — лента уносит (стадия 18 на 794457638, «не сойти с ленты»).
func (w *World) TouchesBelt(p bridge.Pos) bool {
	for _, d := range [][2]float64{{0, 0}, {-beltReach, -beltReach}, {beltReach, -beltReach}, {-beltReach, beltReach}, {beltReach, beltReach}} {
		if w.OnBelt(bridge.Pos{X: p.X + d[0], Y: p.Y + d[1]}) {
			return true
		}
	}
	return false
}

// OnBelt — точка на ленте (персонажа на ней несёт).
func (w *World) OnBelt(p bridge.Pos) bool {
	t := TileOf(p)
	for _, o := range w.ObjectsNear(p, 1.5) {
		if (o.Type == "transport-belt" || o.Type == "underground-belt" || o.Type == "splitter") && TileOf(o.Pos) == t {
			return true
		}
	}
	return false
}

// clearSegment — персонаж пройдёт по отрезку a→b: ни одна точка не в рамке объекта
// (раздутой на полуширину персонажа) и не на воде. Точная проверка, без сетки.
func (w *World) clearSegment(a, b bridge.Pos) bool {
	objs := w.ObjectsNear(a, dist(a, b)+4)
	belts := map[Tile]bool{}
	for _, o := range objs {
		if o.Type == "transport-belt" {
			belts[TileOf(o.Pos)] = true
		}
	}
	n := int(dist(a, b)/0.05) + 1
	step, run := dist(a, b)/float64(n), 0.0
	for i := 0; i <= n; i++ {
		f := float64(i) / float64(n)
		p := bridge.Pos{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f}
		switch w.TileAt(TileOf(p)) {
		case Land, Shallow:
		default:
			return false
		}
		// поперёк ленты — можно, вдоль (дольше 1.5 тайла по лентам подряд) — нет: снесёт
		if belts[TileOf(p)] {
			if run += step; run > 1.5 {
				return false
			}
		} else {
			run = 0
		}
		for _, o := range objs {
			if noCollision[o.Type] || o.Type == "transport-belt" {
				continue
			}
			if w.CollisionBox(o).Contains(p, charHalf) {
				return false
			}
		}
	}
	return true
}

// DebugCells — карта ячеек вокруг p (отладка): . свободно, T дерево, m запас, # рамка.
func (w *World) DebugCells(p bridge.Pos, r int) string {
	c := CellOf(p)
	s := ""
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			free, hard, core, trees := w.cellState(Cell{c.X + dx, c.Y + dy})
			ch := "."
			switch {
			case dx == 0 && dy == 0:
				ch = "@"
			case free:
			case core:
				ch = "#"
			case hard:
				ch = "m"
			case len(trees) > 0:
				ch = "T"
			}
			s += ch
		}
		s += "\n"
	}
	return s
}

// escape — выход из щели, где сетка ячеек прохода не видит: поиск в ширину по точкам с
// шагом 0.1 (до 3 тайлов) по настоящим рамкам до свободной ячейки. Путь — точки поворота.
func (w *World) escape(from bridge.Pos) ([]bridge.Pos, bool) {
	objs := w.ObjectsNear(from, 6)
	ok := func(p bridge.Pos) bool {
		switch w.TileAt(TileOf(p)) {
		case Land, Shallow:
		default:
			if TileOf(p) != TileOf(from) { // стоим краем на воде — свой тайл можно
				return false
			}
		}
		for _, o := range objs {
			if noCollision[o.Type] || o.Type == "transport-belt" || o.Type == "character" {
				continue
			}
			if w.CollisionBox(o).Contains(p, charHalf) {
				return false
			}
		}
		return true
	}
	const step, maxR = 0.1, 30
	type pt struct{ x, y int }
	at := func(q pt) bridge.Pos { return bridge.Pos{X: from.X + float64(q.x)*step, Y: from.Y + float64(q.y)*step} }
	came := map[pt]pt{{0, 0}: {0, 0}}
	queue := []pt{{0, 0}}
	for len(queue) > 0 {
		q := queue[0]
		queue = queue[1:]
		if free, _, _, _ := w.cellState(CellOf(at(q))); free && q != (pt{0, 0}) {
			var res []bridge.Pos
			for c := q; c != (pt{0, 0}); c = came[c] {
				res = append(res, at(c))
			}
			for i, j := 0, len(res)-1; i < j; i, j = i+1, j-1 {
				res[i], res[j] = res[j], res[i]
			}
			// сжать: оставить точки, где меняется направление
			var out []bridge.Pos
			for i := range res {
				if i == len(res)-1 {
					out = append(out, res[i])
					break
				}
				prev := from
				if i > 0 {
					prev = res[i-1]
				}
				if math.Abs((res[i].X-prev.X)-(res[i+1].X-res[i].X)) > 1e-9 || math.Abs((res[i].Y-prev.Y)-(res[i+1].Y-res[i].Y)) > 1e-9 {
					out = append(out, res[i])
				}
			}
			// и дойти до центра ячейки (там уже свободно с запасом)
			return append(out, CellOf(res[len(res)-1]).Center()), true
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				n := pt{q.x + dx, q.y + dy}
				if _, seen := came[n]; seen || abs(n.x) > maxR || abs(n.y) > maxR || !ok(at(n)) {
					continue
				}
				came[n] = q
				queue = append(queue, n)
			}
		}
	}
	return nil, false
}

// Enclosed — из p никуда не выйти (во все стороны на 5 тайлов пути нет даже сквозь
// деревья): персонажа обступили постройки.
func (w *World) Enclosed(p bridge.Pos) bool {
	for k := 0; k < 8; k++ {
		a := float64(k) * math.Pi / 4
		to := bridge.Pos{X: p.X + 5*math.Cos(a), Y: p.Y + 5*math.Sin(a)}
		r, err := w.Route(p, to, 1.5, 20000, 6)
		if err == nil || r.Length() > 2 {
			return false
		}
	}
	return true
}

// beltCost — надбавка за ячейку ленты: поперёк перейти — пустяк, вдоль идти — лента сносит,
// из коридора между бурами не выйти (бот застревал).
const beltCost = 2.0

// onBelt — ячейка на ленте.
func (w *World) onBelt(cl Cell) bool {
	ch := w.Chunks[ChunkOf(cl.tile())]
	if ch == nil {
		return false
	}
	in := w.cellSet(ch)[cl]
	return in != nil && in.belt
}

// Hide — убрать объекты из модели (план поверх того, что потом разберут); restore — вернуть.
func (w *World) Hide(objs []Object) (restore func()) {
	type saved struct {
		c    *Chunk
		objs []Object
	}
	var undo []saved
	byChunk := map[*Chunk][]Object{}
	for _, o := range objs {
		if c := w.Chunks[ChunkOf(TileOf(o.Pos))]; c != nil {
			byChunk[c] = append(byChunk[c], o)
		}
	}
	for c, os := range byChunk {
		undo = append(undo, saved{c, c.Objects})
		var keep []Object
		for _, x := range c.Objects {
			hide := false
			for _, o := range os {
				hide = hide || x.Name == o.Name && x.Pos == o.Pos
			}
			if !hide {
				keep = append(keep, x)
			}
		}
		c.Objects = keep
		c.blocked, c.hard, c.solid, c.cells = nil, nil, nil, nil
	}
	return func() {
		for _, u := range undo {
			u.c.Objects = u.objs
			u.c.blocked, u.c.hard, u.c.solid, u.c.cells = nil, nil, nil, nil
		}
	}
}
