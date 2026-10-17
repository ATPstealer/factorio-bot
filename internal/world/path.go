package world

import (
	"container/heap"
	"errors"
	"math"

	"factorio-bot/internal/bridge"
)

// ErrNoPath — до цели по открытой проходимой карте не дойти.
var ErrNoPath = errors.New("world: пути нет")

type node struct {
	t    Tile
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
func tdist(a Tile, b bridge.Pos) float64 { c := a.Center(); return math.Hypot(c.X-b.X, c.Y-b.Y) }

// Path ищет путь A* по тайлам открытой карты от from до любого тайла, центр которого
// ближе near к to. Диагональ — только если оба соседних по прямой тайла проходимы
// (персонаж не срезает углы). Тайл старта считается проходимым (персонаж может стоять
// у края рамки). Возвращает точки поворота (центры тайлов), последняя — у цели.
func (w *World) Path(from, to bridge.Pos, near float64, maxNodes int) ([]bridge.Pos, error) {
	if maxNodes <= 0 {
		maxNodes = 200_000
	}
	start := TileOf(from)
	near = math.Max(near, 0.5)
	walk := func(t Tile) bool { return t == start || w.Walkable(t) }

	open := &openList{}
	nodes := map[Tile]*node{}
	came := map[Tile]Tile{}
	closed := map[Tile]bool{}
	h := func(t Tile) float64 {
		c := t.Center()
		return math.Max(0, octile(to.X-c.X, to.Y-c.Y)-near)
	}
	s := &node{t: start, f: h(start)}
	nodes[start] = s
	heap.Push(open, s)

	for open.Len() > 0 {
		cur := heap.Pop(open).(*node)
		if closed[cur.t] {
			continue
		}
		closed[cur.t] = true
		if tdist(cur.t, to) <= near {
			return w.waypoints(start, cur.t, came), nil
		}
		if len(closed) > maxNodes {
			break
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				n := Tile{cur.t.X + dx, cur.t.Y + dy}
				if closed[n] || !walk(n) {
					continue
				}
				cost := 1.0
				if dx != 0 && dy != 0 {
					if !walk(Tile{cur.t.X + dx, cur.t.Y}) || !walk(Tile{cur.t.X, cur.t.Y + dy}) {
						continue
					}
					cost = math.Sqrt2
				}
				g := cur.g + cost
				if old, ok := nodes[n]; ok && old.g <= g {
					continue
				}
				nn := &node{t: n, g: g, f: g + h(n)}
				nodes[n] = nn
				came[n] = cur.t
				heap.Push(open, nn)
			}
		}
	}
	return nil, ErrNoPath
}

// waypoints — восстановление пути и сжатие: из цепочки тайлов остаются точки, между
// которыми видимость по проходимым тайлам (персонаж идёт по прямой).
func (w *World) waypoints(start, goal Tile, came map[Tile]Tile) []bridge.Pos {
	var tiles []Tile
	for t := goal; ; t = came[t] {
		tiles = append(tiles, t)
		if t == start {
			break
		}
	}
	for i, j := 0, len(tiles)-1; i < j; i, j = i+1, j-1 {
		tiles[i], tiles[j] = tiles[j], tiles[i]
	}
	var res []bridge.Pos
	anchor := 0
	for i := 2; i < len(tiles); i++ {
		if !w.clearLine(tiles[anchor], tiles[i], start) {
			res = append(res, tiles[i-1].Center())
			anchor = i - 1
		}
	}
	res = append(res, tiles[len(tiles)-1].Center())
	return res
}

// clearLine — все тайлы, которые задевает отрезок между центрами a и b (с запасом на
// ширину персонажа), проходимы.
func (w *World) clearLine(a, b, start Tile) bool {
	ac, bc := a.Center(), b.Center()
	d := math.Hypot(bc.X-ac.X, bc.Y-ac.Y)
	steps := int(d*4) + 1
	const r = 0.3 // полуширина персонажа с запасом
	for i := 0; i <= steps; i++ {
		f := float64(i) / float64(steps)
		x, y := ac.X+(bc.X-ac.X)*f, ac.Y+(bc.Y-ac.Y)*f
		for _, o := range [][2]float64{{-r, -r}, {r, -r}, {-r, r}, {r, r}} {
			t := TileOf(bridge.Pos{X: x + o[0], Y: y + o[1]})
			if t != start && !w.Walkable(t) {
				return false
			}
		}
	}
	return true
}
