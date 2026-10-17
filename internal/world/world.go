// Package world — модель карты в памяти бота: только открытые (charted) чанки, как их видит игрок.
package world

import (
	"math"
	"sort"
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

	blocked map[Tile]bool // лениво: тайлы под коллизиями объектов
	hard    map[Tile]bool // из них — занятые не деревьями (деревья можно срубить)
	solid   map[Tile]bool // занятые тем, что руками не убрать (утёсы, постройки)
}

// clearable — объект можно убрать руками перед стройкой: дерево, камень, обломок корабля.
func clearable(o Object) bool {
	return !o.Own && (o.Type == "tree" || o.Type == "simple-entity" || strings.HasPrefix(o.Name, "crash-site-"))
}

type World struct {
	Protos   *knowledge.Prototypes // nil — коллизии объектов считаются 1×1
	Chunks   map[bridge.ChunkPos]*Chunk
	EventSeq int // последнее учтённое событие мода
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
	if o.Direction == 4 || o.Direction == 12 { // восток/запад: рамка повёрнута
		b = bridge.Box{
			LeftTop:     bridge.Pos{X: -b.RightBottom.Y, Y: b.LeftTop.X},
			RightBottom: bridge.Pos{X: -b.LeftTop.Y, Y: b.RightBottom.X},
		}
	}
	return bridge.Box{
		LeftTop:     bridge.Pos{X: o.Pos.X + b.LeftTop.X, Y: o.Pos.Y + b.LeftTop.Y},
		RightBottom: bridge.Pos{X: o.Pos.X + b.RightBottom.X, Y: o.Pos.Y + b.RightBottom.Y},
	}
}

// BoxTiles — тайлы, которые рамка задевает изнутри (касание края не считается).
func BoxTiles(b bridge.Box) []Tile {
	const eps = 1e-6
	var res []Tile
	for y := int(math.Floor(b.LeftTop.Y + eps)); float64(y) < b.RightBottom.Y-eps; y++ {
		for x := int(math.Floor(b.LeftTop.X + eps)); float64(x) < b.RightBottom.X-eps; x++ {
			res = append(res, Tile{x, y})
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

// Frontier — открытые чанки на краю открытой зоны (хоть один сосед по стороне не открыт),
// ближние к from — первыми.
func (w *World) Frontier(from bridge.Pos) []bridge.ChunkPos {
	var res []bridge.ChunkPos
	for cp := range w.Chunks {
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			if w.Chunks[bridge.ChunkPos{X: cp.X + d[0], Y: cp.Y + d[1]}] == nil {
				res = append(res, cp)
				break
			}
		}
	}
	center := func(c bridge.ChunkPos) bridge.Pos {
		return bridge.Pos{X: float64(c.X*ChunkSize + ChunkSize/2), Y: float64(c.Y*ChunkSize + ChunkSize/2)}
	}
	sort.Slice(res, func(i, j int) bool {
		a, b := center(res[i]), center(res[j])
		da, db := math.Hypot(a.X-from.X, a.Y-from.Y), math.Hypot(b.X-from.X, b.Y-from.Y)
		if da != db {
			return da < db
		}
		return res[i].X*100000+res[i].Y < res[j].X*100000+res[j].Y
	})
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

// ClearablesAt — убираемые объекты (деревья, камни, обломки), задевающие тайл.
func (w *World) ClearablesAt(t Tile) []Object {
	var res []Object
	for _, o := range w.ObjectsNear(t.Center(), 4) {
		if !clearable(o) {
			continue
		}
		for _, bt := range BoxTiles(w.CollisionBox(o)) {
			if bt == t {
				res = append(res, o)
				break
			}
		}
	}
	return res
}
