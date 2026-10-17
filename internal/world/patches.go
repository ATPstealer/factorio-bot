package world

import (
	"math"
	"sort"

	"factorio-bot/internal/bridge"
)

// Patch — месторождение: связная группа тайлов одного ресурса.
type Patch struct {
	Name     string
	Tiles    []Tile
	Amount   int // сумма по тайлам (для нефти — суммарный «выход»)
	Min, Max Tile
	Center   bridge.Pos // центр масс тайлов
}

// linkRadius — какие тайлы считаются соседними: руда лежит сплошь (8-связность),
// нефтяные скважины — россыпью на расстоянии нескольких тайлов.
func (w *World) linkRadius(name string) int {
	if w.Protos != nil {
		if e, ok := w.Protos.Entities[name]; ok && e.ResourceCategory == "basic-fluid" {
			return 12
		}
	} else if name == "crude-oil" {
		return 12
	}
	return 1
}

// Patches — все месторождения открытой карты, крупные первыми.
// Месторождение у края открытой зоны может продолжаться в неоткрытую — это видно позже.
func (w *World) Patches() []Patch {
	all := map[Tile]Resource{}
	for _, c := range w.Chunks {
		for t, r := range c.Resources {
			all[t] = r
		}
	}
	seen := map[Tile]bool{}
	var res []Patch
	// обход в детерминированном порядке
	starts := make([]Tile, 0, len(all))
	for t := range all {
		starts = append(starts, t)
	}
	sort.Slice(starts, func(i, j int) bool {
		if starts[i].Y != starts[j].Y {
			return starts[i].Y < starts[j].Y
		}
		return starts[i].X < starts[j].X
	})
	for _, s := range starts {
		if seen[s] {
			continue
		}
		name := all[s].Name
		r := w.linkRadius(name)
		p := Patch{Name: name, Min: s, Max: s}
		var sx, sy float64
		stack := []Tile{s}
		seen[s] = true
		for len(stack) > 0 {
			t := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			p.Tiles = append(p.Tiles, t)
			p.Amount += all[t].Amount
			sx += float64(t.X) + 0.5
			sy += float64(t.Y) + 0.5
			p.Min = Tile{min(p.Min.X, t.X), min(p.Min.Y, t.Y)}
			p.Max = Tile{max(p.Max.X, t.X), max(p.Max.Y, t.Y)}
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					n := Tile{t.X + dx, t.Y + dy}
					if seen[n] {
						continue
					}
					if nr, ok := all[n]; ok && nr.Name == name {
						seen[n] = true
						stack = append(stack, n)
					}
				}
			}
		}
		p.Center = bridge.Pos{X: sx / float64(len(p.Tiles)), Y: sy / float64(len(p.Tiles))}
		res = append(res, p)
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].Amount > res[j].Amount })
	return res
}

// Distance — от точки до ближайшего тайла месторождения.
func (p Patch) Distance(from bridge.Pos) float64 {
	best := math.Inf(1)
	for _, t := range p.Tiles {
		c := t.Center()
		best = math.Min(best, math.Hypot(c.X-from.X, c.Y-from.Y))
	}
	return best
}
