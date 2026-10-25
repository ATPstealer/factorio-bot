// Package world — модель карты в памяти бота: только открытые (charted) чанки, как их видит игрок.
package world

import (
	"math"
	"strings"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
)

const ChunkSize = 32

// Tile — тайл мира (целые координаты левого верхнего угла).
type Tile struct{ X, Y int }

func TileOf(p bridge.Pos) Tile { return Tile{int(math.Floor(p.X)), int(math.Floor(p.Y))} }

func (t Tile) Center() bridge.Pos { return bridge.Pos{X: float64(t.X) + 0.5, Y: float64(t.Y) + 0.5} }

func ChunkOf(t Tile) bridge.ChunkPos {
	return bridge.ChunkPos{X: floorDiv(t.X, ChunkSize), Y: floorDiv(t.Y, ChunkSize)}
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// Классы тайлов (как в моде): '.' суша, 'w' вода, 's' мелководье, 'x' край карты, '?' не открыто.
const (
	Land    = '.'
	Water   = 'w'
	Shallow = 's'
	OutMap  = 'x'
	Unknown = '?'
)

type Resource struct {
	Name   string
	Amount int
}

// Object — не-ресурсная сущность: дерево, камень, обломок, постройка.
type Object struct {
	Name      string
	Type      string
	Pos       bridge.Pos
	Direction int // 2.0: 0 север, 4 восток, 8 юг, 12 запад
	Unit      int
	Own       bool
	Status    string
	Box       *bridge.Box // рамка из игры (утёсы); nil — из справочника
}

type Chunk struct {
	Pos       bridge.ChunkPos
	Tiles     string // 1024 символа по строкам
	Resources map[Tile]Resource
	Objects   []Object

	blocked map[Tile]bool      // лениво: тайлы под коллизиями объектов
	hard    map[Tile]bool      // из них — занятые не деревьями (деревья можно срубить)
	solid   map[Tile]bool      // занятые тем, что руками не убрать (утёсы, постройки)
	cells   map[Cell]*cellInfo // лениво: ячейки навигации под объектами (path.go)
}

// clearable — объект можно убрать руками перед стройкой: дерево, камень, обломок корабля.
func clearable(o Object) bool {
	return !o.Own && (o.Type == "tree" || o.Type == "simple-entity" || strings.HasPrefix(o.Name, "crash-site-"))
}

type World struct {
	Protos   *knowledge.Prototypes // nil — коллизии объектов считаются 1×1
	Chunks   map[bridge.ChunkPos]*Chunk
	EventSeq int           // последнее учтённое событие мода
	Stuck    map[Cell]bool // ячейки, где персонаж упёрся (MarkStuck)
	// Squeeze — маршрут через запас у рамок (не через сами рамки): обычного пути нет,
	// а персонаж зашёл сюда через узкую щель
	Squeeze bool
}

func New(protos *knowledge.Prototypes) *World {
	return &World{Protos: protos, Chunks: map[bridge.ChunkPos]*Chunk{}}
}

// Apply кладёт результаты сканирования; неоткрытые чанки удаляет.
func (w *World) Apply(scans []bridge.ChunkScan) {
	for _, s := range scans {
		cp := bridge.ChunkPos{X: s.X, Y: s.Y}
		if !s.Charted || len(s.Tiles) != ChunkSize*ChunkSize {
			delete(w.Chunks, cp)
			continue
		}
		c := &Chunk{Pos: cp, Tiles: s.Tiles, Resources: make(map[Tile]Resource, len(s.Resources))}
		for _, r := range s.Resources {
			c.Resources[TileOf(bridge.Pos{X: r.X, Y: r.Y})] = Resource{Name: r.Name, Amount: r.Amount}
		}
		for _, e := range s.Entities {
			o := Object{Name: e.Name, Type: e.Type, Pos: bridge.Pos{X: e.X, Y: e.Y}, Unit: e.Unit, Own: e.Own, Status: e.Status, Box: e.Box}
			if e.Direction != nil {
				o.Direction = *e.Direction
			}
			c.Objects = append(c.Objects, o)
		}
		w.Chunks[cp] = c
	}
}

func (w *World) chunk(t Tile) *Chunk { return w.Chunks[ChunkOf(t)] }

// TileAt — класс тайла или Unknown для неоткрытого.
func (w *World) TileAt(t Tile) byte {
	c := w.chunk(t)
	if c == nil {
		return Unknown
	}
	x, y := t.X-c.Pos.X*ChunkSize, t.Y-c.Pos.Y*ChunkSize
	return c.Tiles[y*ChunkSize+x]
}

func (w *World) ResourceAt(t Tile) (Resource, bool) {
	c := w.chunk(t)
	if c == nil {
		return Resource{}, false
	}
	r, ok := c.Resources[t]
	return r, ok
}

// Objects — все объекты открытой карты.
func (w *World) Objects() []Object {
	var res []Object
	for _, c := range w.Chunks {
		res = append(res, c.Objects...)
	}
	return res
}

// CollisionBox — рамка коллизии объекта в мировых координатах (с поворотом на 90°).
func (w *World) CollisionBox(o Object) bridge.Box {
	if o.Box != nil {
		return *o.Box
	}
	b := bridge.Box{LeftTop: bridge.Pos{X: -0.4, Y: -0.4}, RightBottom: bridge.Pos{X: 0.4, Y: 0.4}}
	if w.Protos != nil {
		if e, ok := w.Protos.Entities[o.Name]; ok {
			b = e.CollisionBox
		}
	}
	return place(b, o)
}

// SelectionBox — рамка выделения объекта (куда наведение курсора выделяет его).
func (w *World) SelectionBox(o Object) bridge.Box {
	b := bridge.Box{LeftTop: bridge.Pos{X: -0.5, Y: -0.5}, RightBottom: bridge.Pos{X: 0.5, Y: 0.5}}
	if w.Protos != nil {
		if e, ok := w.Protos.Entities[o.Name]; ok {
			b = e.SelectionBox
		}
	}
	return place(b, o)
}

// ObjectsNear — объекты, чьи позиции ближе r к p.
func (w *World) ObjectsNear(p bridge.Pos, r float64) []Object {
	var res []Object
	c := ChunkOf(TileOf(p))
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			ch := w.Chunks[bridge.ChunkPos{X: c.X + dx, Y: c.Y + dy}]
			if ch == nil {
				continue
			}
			for _, o := range ch.Objects {
				if math.Hypot(o.Pos.X-p.X, o.Pos.Y-p.Y) <= r {
					res = append(res, o)
				}
			}
		}
	}
	return res
}

// place — рамка прототипа b, повёрнутая по направлению объекта и сдвинутая в его позицию.
func place(b bridge.Box, o Object) bridge.Box {
	b = RotateBox(b, o.Direction)
	return bridge.Box{
		LeftTop:     bridge.Pos{X: o.Pos.X + b.LeftTop.X, Y: o.Pos.Y + b.LeftTop.Y},
		RightBottom: bridge.Pos{X: o.Pos.X + b.RightBottom.X, Y: o.Pos.Y + b.RightBottom.Y},
	}
}

// RotateBox — рамка прототипа (при «север») при направлении dir (по часовой: восток — на 90°).
// Несимметричные рамки (помпа) у востока и запада разные.
func RotateBox(b bridge.Box, dir int) bridge.Box {
	lt, rb := b.LeftTop, b.RightBottom
	switch dir {
	case 4:
		return bridge.Box{LeftTop: bridge.Pos{X: -rb.Y, Y: lt.X}, RightBottom: bridge.Pos{X: -lt.Y, Y: rb.X}}
	case 8:
		return bridge.Box{LeftTop: bridge.Pos{X: -rb.X, Y: -rb.Y}, RightBottom: bridge.Pos{X: -lt.X, Y: -lt.Y}}
	case 12:
		return bridge.Box{LeftTop: bridge.Pos{X: lt.Y, Y: -rb.X}, RightBottom: bridge.Pos{X: rb.Y, Y: -lt.X}}
	}
	return b
}

// BoxTiles — тайлы, которые рамка задевает изнутри (касание края не считается).
func BoxTiles(b bridge.Box) []Tile {
	const eps = 1e-6
	bb := b.Bounds()
	var res []Tile
	for y := int(math.Floor(bb.LeftTop.Y + eps)); float64(y) < bb.RightBottom.Y-eps; y++ {
		for x := int(math.Floor(bb.LeftTop.X + eps)); float64(x) < bb.RightBottom.X-eps; x++ {
			// повёрнутая рамка (утёс-угол) — только тайлы, которые она задевает
			if t := (Tile{x, y}); b.Orientation == 0 || b.Contains(t.Center(), 0.5) {
				res = append(res, t)
			}
		}
	}
	return res
}

// noCollision — типы, сквозь которые персонаж проходит.
var noCollision = map[string]bool{"fish": true, "resource": true, "corpse": true, "item-entity": true}

func (w *World) blockedSet(c *Chunk) map[Tile]bool {
	if c.blocked != nil {
		return c.blocked
	}
	c.blocked, c.hard, c.solid = map[Tile]bool{}, map[Tile]bool{}, map[Tile]bool{}
	for _, o := range c.Objects {
		if noCollision[o.Type] {
			continue
		}
		for _, t := range BoxTiles(w.CollisionBox(o)) {
			c.blocked[t] = true
			if o.Type != "tree" {
				c.hard[t] = true
			}
			if !clearable(o) {
				c.solid[t] = true
			}
		}
	}
	return c.blocked
}

// TreeOnly — тайл суши занят только деревьями: путь можно прорубить.
func (w *World) TreeOnly(t Tile) bool {
	switch w.TileAt(t) {
	case Land, Shallow:
	default:
		return false
	}
	cp := ChunkOf(t)
	tree := false
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			c := w.Chunks[bridge.ChunkPos{X: cp.X + dx, Y: cp.Y + dy}]
			if c == nil {
				continue
			}
			w.blockedSet(c)
			if c.hard[t] {
				return false
			}
			tree = tree || c.blocked[t]
		}
	}
	return tree
}

// Blocked — занят ли тайл объектом (без учёта воды). Объект у края чанка может
// перекрывать тайлы соседнего — проверяем и соседей.
func (w *World) Blocked(t Tile) bool {
	cp := ChunkOf(t)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if c := w.Chunks[bridge.ChunkPos{X: cp.X + dx, Y: cp.Y + dy}]; c != nil && w.blockedSet(c)[t] {
				return true
			}
		}
	}
	return false
}

// Walkable — можно ли пройти по тайлу: открыт, суша или мелководье, без объектов.
func (w *World) Walkable(t Tile) bool {
	switch w.TileAt(t) {
	case Land, Shallow:
		return !w.Blocked(t)
	}
	return false
}

// FreeSpot — ближайшая к near позиция центра постройки w×h тайлов, все тайлы которой
// открыты и проходимы (суша, без объектов) и не задевают avoid (например, игрока).
// Центр: для чётного размера — на углу тайлов, для нечётного — в центре тайла.
func (w *World) FreeSpot(near bridge.Pos, width, height, radius int, avoid *bridge.Pos) (bridge.Pos, bool) {
	return w.FreeSpotWhere(near, width, height, radius, avoid, nil)
}

// FreeSpotWhere — FreeSpot с дополнительным условием на каждый тайл площадки
// (например, «на руде» для бура: в 2.0 буры ставятся только на ресурсы).
func (w *World) FreeSpotWhere(near bridge.Pos, width, height, radius int, avoid *bridge.Pos, tileOK func(Tile) bool) (bridge.Pos, bool) {
	c := TileOf(near)
	best, bestD := bridge.Pos{}, math.Inf(1)
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			lt := Tile{c.X + dx, c.Y + dy} // левый верхний тайл площадки
			ok := true
			for y := 0; y < height && ok; y++ {
				for x := 0; x < width && ok; x++ {
					t := Tile{lt.X + x, lt.Y + y}
					ok = w.TileAt(t) == Land && !w.Blocked(t) && (tileOK == nil || tileOK(t))
					if ok && avoid != nil {
						a := TileOf(*avoid)
						ok = math.Abs(float64(t.X-a.X)) > 1 || math.Abs(float64(t.Y-a.Y)) > 1
					}
				}
			}
			if !ok {
				continue
			}
			pos := bridge.Pos{X: float64(lt.X) + float64(width)/2, Y: float64(lt.Y) + float64(height)/2}
			if d := math.Hypot(pos.X-near.X, pos.Y-near.Y); d < bestD {
				best, bestD = pos, d
			}
		}
	}
	return best, !math.IsInf(bestD, 1)
}

// NearestUnknown — ближайший к from неоткрытый чанк на краю открытой карты (рядом с открытым),
// кольцами вокруг чанка from до max чанков; skip — недоступные. Центр чанка, ok.
func (w *World) NearestUnknown(from bridge.Pos, max int, skip map[bridge.ChunkPos]bool) (bridge.Pos, bool) {
	return w.NearestUnknownWhere(from, max, skip, nil)
}

// NearestUnknownWhere — NearestUnknown только среди чанков, для которых ok (nil — любые).
func (w *World) NearestUnknownWhere(from bridge.Pos, max int, skip map[bridge.ChunkPos]bool, ok func(bridge.ChunkPos) bool) (bridge.Pos, bool) {
	c0 := ChunkOf(TileOf(from))
	for r := 1; r <= max; r++ {
		best, bd := bridge.ChunkPos{}, math.Inf(1)
		for _, d := range ringChunks(r) {
			c := bridge.ChunkPos{X: c0.X + d.X, Y: c0.Y + d.Y}
			if w.Chunks[c] != nil || skip[c] || ok != nil && !ok(c) {
				continue
			}
			edge := false // рядом открытый — дойти можно по открытой земле
			for _, n := range []bridge.ChunkPos{{X: c.X + 1, Y: c.Y}, {X: c.X - 1, Y: c.Y}, {X: c.X, Y: c.Y + 1}, {X: c.X, Y: c.Y - 1}} {
				edge = edge || w.Chunks[n] != nil
			}
			if !edge {
				continue
			}
			p := bridge.Pos{X: float64(c.X*ChunkSize + ChunkSize/2), Y: float64(c.Y*ChunkSize + ChunkSize/2)}
			if d := math.Hypot(p.X-from.X, p.Y-from.Y); d < bd {
				best, bd = c, d
			}
		}
		if !math.IsInf(bd, 1) {
			return bridge.Pos{X: float64(best.X*ChunkSize + ChunkSize/2), Y: float64(best.Y*ChunkSize + ChunkSize/2)}, true
		}
	}
	return bridge.Pos{}, false
}

// FrontierFrom — неоткрытые чанки, до которых можно дойти от from (разведка не бежит за воду):
// открытые чанки связаны, если через общую границу есть проходимая пара тайлов (внутри чанка —
// считаются связными), неоткрытый — если у достижимого соседа граница к нему проходима.
func (w *World) FrontierFrom(from bridge.Pos) map[bridge.ChunkPos]bool {
	// side — проходимые тайлы края чанка c в сторону d (0 — +X, 1 — −X, 2 — +Y, 3 — −Y)
	edge := func(c bridge.ChunkPos, d int, i int) Tile {
		x0, y0 := c.X*ChunkSize, c.Y*ChunkSize
		switch d {
		case 0:
			return Tile{X: x0 + ChunkSize - 1, Y: y0 + i}
		case 1:
			return Tile{X: x0, Y: y0 + i}
		case 2:
			return Tile{X: x0 + i, Y: y0 + ChunkSize - 1}
		}
		return Tile{X: x0 + i, Y: y0}
	}
	step := []bridge.ChunkPos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}}
	opp := []int{1, 0, 3, 2}
	start := ChunkOf(TileOf(from))
	seen := map[bridge.ChunkPos]bool{start: true}
	queue := []bridge.ChunkPos{start}
	res := map[bridge.ChunkPos]bool{}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		for d, s := range step {
			n := bridge.ChunkPos{X: c.X + s.X, Y: c.Y + s.Y}
			if seen[n] && w.Chunks[n] != nil {
				continue
			}
			pass := false
			for i := 0; i < ChunkSize && !pass; i++ {
				a := edge(c, d, i)
				if !w.passable(a) {
					continue
				}
				if w.Chunks[n] == nil {
					pass = true // за краем — неоткрытое: дойти до края можно
				} else {
					pass = w.passable(edge(n, opp[d], i))
				}
			}
			if !pass {
				continue
			}
			if w.Chunks[n] == nil {
				res[n] = true
				continue
			}
			seen[n] = true
			queue = append(queue, n)
		}
	}
	return res
}

// passable — по тайлу можно пройти, если надо — срубив (деревья, камни): суша без утёсов и
// построек или мелководье.
func (w *World) passable(t Tile) bool {
	if ok, _ := w.Buildable(t); ok {
		return true
	}
	return w.Walkable(t)
}

// ringChunks — чанки квадратного кольца r вокруг чанка (0,0).
func ringChunks(r int) []bridge.ChunkPos {
	if r == 0 {
		return []bridge.ChunkPos{{}}
	}
	var res []bridge.ChunkPos
	for i := -r; i < r; i++ {
		res = append(res, bridge.ChunkPos{X: i, Y: -r}, bridge.ChunkPos{X: r, Y: i}, bridge.ChunkPos{X: -i, Y: r}, bridge.ChunkPos{X: -r, Y: -i})
	}
	return res
}

// Buildable — можно ли строить на тайле: открытая суша, без неубираемых объектов.
// clear — на тайле есть деревья/камни/обломки, их надо убрать перед стройкой.
func (w *World) Buildable(t Tile) (ok, clear bool) {
	if w.TileAt(t) != Land {
		return false, false
	}
	cp := ChunkOf(t)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			c := w.Chunks[bridge.ChunkPos{X: cp.X + dx, Y: cp.Y + dy}]
			if c == nil {
				continue
			}
			w.blockedSet(c)
			if c.solid[t] {
				return false, false
			}
			clear = clear || c.blocked[t]
		}
	}
	return true, clear
}
