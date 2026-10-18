// Package layout — раскладка построек «на бумаге»: шахты, модули, ленты, столбы, проверка.
package layout

import (
	"fmt"
	"math"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

// Направления 2.0.
const (
	N = 0
	E = 4
	S = 8
	W = 12
)

// DirName — для вывода.
func DirName(d int) string {
	return map[int]string{N: "↑", E: "→", S: "↓", W: "←"}[d]
}

// Rotate — вектор прототипа (задан для «севера») в направление d: по часовой на d/4 × 90°.
func Rotate(x, y float64, d int) (float64, float64) {
	switch d {
	case E:
		return -y, x
	case S:
		return -x, -y
	case W:
		return y, -x
	}
	return x, y
}

// Step — шаг на тайл в направлении d.
func Step(t world.Tile, d int) world.Tile {
	switch d {
	case E:
		return world.Tile{X: t.X + 1, Y: t.Y}
	case S:
		return world.Tile{X: t.X, Y: t.Y + 1}
	case W:
		return world.Tile{X: t.X - 1, Y: t.Y}
	}
	return world.Tile{X: t.X, Y: t.Y - 1}
}

// Placement — одна постройка раскладки.
type Placement struct {
	Name   string     `json:"name"`
	Pos    bridge.Pos `json:"pos"` // центр
	Dir    int        `json:"dir"`
	Recipe string     `json:"recipe,omitempty"`
	Tag    string     `json:"tag,omitempty"` // модуль/назначение: "mine:iron-ore", "smelt:iron", …
}

// Size — размер в тайлах с учётом поворота.
func Size(p *knowledge.Prototypes, name string, dir int) (int, int) {
	e := p.Entities[name]
	w, h := max(e.TileWidth, 1), max(e.TileHeight, 1)
	if dir == E || dir == W {
		w, h = h, w
	}
	return w, h
}

// CenterAt — центр постройки, левый верхний тайл которой lt.
func CenterAt(p *knowledge.Prototypes, name string, dir int, lt world.Tile) bridge.Pos {
	w, h := Size(p, name, dir)
	return bridge.Pos{X: float64(lt.X) + float64(w)/2, Y: float64(lt.Y) + float64(h)/2}
}

// Tiles — тайлы, которые занимает постройка.
func Tiles(p *knowledge.Prototypes, pl Placement) []world.Tile {
	w, h := Size(p, pl.Name, pl.Dir)
	lt := world.Tile{X: int(math.Round(pl.Pos.X - float64(w)/2)), Y: int(math.Round(pl.Pos.Y - float64(h)/2))}
	res := make([]world.Tile, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			res = append(res, world.Tile{X: lt.X + x, Y: lt.Y + y})
		}
	}
	return res
}

func vec(v []float64) (float64, float64) {
	if len(v) < 2 {
		return 0, 0
	}
	return v[0], v[1]
}

// DrillOutput — тайл, куда бур выдаёт добытое.
func DrillOutput(p *knowledge.Prototypes, pl Placement) world.Tile {
	x, y := vec(p.Entities[pl.Name].VectorToPlaceResult)
	x, y = Rotate(x, y, pl.Dir)
	return world.TileOf(bridge.Pos{X: pl.Pos.X + x, Y: pl.Pos.Y + y})
}

// MiningArea — тайлы зоны добычи бура.
func MiningArea(p *knowledge.Prototypes, pl Placement) []world.Tile {
	r := p.Entities[pl.Name].MiningDrillRadius
	b := bridge.Box{LeftTop: bridge.Pos{X: pl.Pos.X - r, Y: pl.Pos.Y - r}, RightBottom: bridge.Pos{X: pl.Pos.X + r, Y: pl.Pos.Y + r}}
	return world.BoxTiles(b)
}

// InserterEnds — откуда инсертер берёт и куда кладёт (тайлы).
func InserterEnds(p *knowledge.Prototypes, pl Placement) (pick, drop world.Tile) {
	e := p.Entities[pl.Name]
	px, py := vec(e.InserterPickup)
	dx, dy := vec(e.InserterDrop)
	px, py = Rotate(px, py, pl.Dir)
	dx, dy = Rotate(dx, dy, pl.Dir)
	return world.TileOf(bridge.Pos{X: pl.Pos.X + px, Y: pl.Pos.Y + py}), world.TileOf(bridge.Pos{X: pl.Pos.X + dx, Y: pl.Pos.Y + dy})
}

// InserterDir — направление инсертера, берущего с тайла from (соседнего) и кладущего напротив.
func InserterDir(p *knowledge.Prototypes, name string, at, from world.Tile) int {
	for _, d := range []int{N, E, S, W} {
		pick, _ := InserterEnds(p, Placement{Name: name, Pos: at.Center(), Dir: d})
		if pick == from {
			return d
		}
	}
	return N
}

func (pl Placement) String() string {
	s := fmt.Sprintf("%s (%.1f, %.1f) %s", pl.Name, pl.Pos.X, pl.Pos.Y, DirName(pl.Dir))
	if pl.Recipe != "" {
		s += " [" + pl.Recipe + "]"
	}
	return s
}

// Conn — трубное соединение размещения: At — свой крайний тайл, To — тайл соседа.
type Conn struct {
	At, To world.Tile
	Dir    int
	Filter string
	Flow   string
}

// Connections — трубные соединения размещения с учётом поворота.
func Connections(p *knowledge.Prototypes, pl Placement) []Conn {
	var res []Conn
	for _, pc := range p.Entities[pl.Name].PipeConnections {
		x, y := Rotate(pc.X, pc.Y, pl.Dir)
		at := world.TileOf(bridge.Pos{X: pl.Pos.X + x, Y: pl.Pos.Y + y})
		d := (pc.Direction + pl.Dir) % 16
		res = append(res, Conn{At: at, To: Step(at, d), Dir: d, Filter: pc.Filter, Flow: pc.Flow})
	}
	return res
}

// Joined — соединения смотрят друг на друга.
func Joined(a, b Conn) bool { return a.To == b.At && b.To == a.At }

// Attach — размещения name (все направления), у которых есть соединение c жидкостью fluid
// (пусто — любой), стыкующееся с from.
func Attach(p *knowledge.Prototypes, from Conn, name, fluid, tag string) []Placement {
	var res []Placement
	for _, d := range []int{N, E, S, W} {
		for _, pc := range p.Entities[name].PipeConnections {
			if fluid != "" && pc.Filter != "" && pc.Filter != fluid {
				continue
			}
			x, y := Rotate(pc.X, pc.Y, d)
			c := from.To.Center()
			pl := Placement{Name: name, Pos: bridge.Pos{X: c.X - x, Y: c.Y - y}, Dir: d, Tag: tag}
			for _, cn := range Connections(p, pl) {
				if Joined(from, cn) {
					res = append(res, pl)
					break
				}
			}
		}
	}
	return res
}
