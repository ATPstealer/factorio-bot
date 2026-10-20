package layout

import (
	"fmt"
	"sort"

	"factorio-bot/internal/world"
)

// MineResult — шахта: сколько буров, сколько руды в секунду, куда выходит лента.
type MineResult struct {
	Drills int
	Rate   float64    // руды/с
	Out    world.Tile // первый тайл за сборной лентой — отсюда маршрут дальше
	OutDir int
}

// Mine раскладывает шахту на месторождении ore (тайлы patch): вертикальные колонны лент
// (↓), по обе стороны — буры выходом на ленту; колонны сливаются в сборную ленту (→) ниже
// месторождения. Бур — только если в его зоне добычи хотя бы половина тайлов — руда.
// maxDrills ≤ 0 — сколько поместится.
func (l *Layout) Mine(patch []world.Tile, ore, drill string, maxDrills int, tag string) (MineResult, error) {
	// сетка колонн может упереться в уже разложенное (вторая шахта на том же
	// месторождении) — перебрать сдвиги сетки, неудачные попытки откатить
	s, _ := Size(l.P, drill, N)
	var err error
	for off := 0; off <= 2*s+2; off++ {
		mark := l.Mark()
		var mr MineResult
		mr, err = l.mineAt(patch, ore, drill, maxDrills, tag, off)
		if err == nil {
			return mr, nil
		}
		l.Rollback(mark)
	}
	return MineResult{}, err
}

func (l *Layout) mineAt(patch []world.Tile, ore, drill string, maxDrills int, tag string, off int) (MineResult, error) {
	if len(patch) == 0 {
		return MineResult{}, fmt.Errorf("layout: пустое месторождение")
	}
	isOre := map[world.Tile]bool{}
	minX, maxX, minY, maxY := patch[0].X, patch[0].X, patch[0].Y, patch[0].Y
	for _, t := range patch {
		isOre[t] = true
		minX, maxX = min(minX, t.X), max(maxX, t.X)
		minY, maxY = min(minY, t.Y), max(maxY, t.Y)
	}
	s, _ := Size(l.P, drill, N)
	type col struct {
		x      int
		drills []Placement
		outs   []world.Tile
	}
	var cols []*col
	total := 0
	half := 0
	for _, t := range MiningArea(l.P, Placement{Name: drill, Pos: CenterAt(l.P, drill, N, world.Tile{})}) {
		_ = t
		half++
	}
	half = (half + 1) / 2
	step := 2*s + 1
	if l.P.Entities[drill].Electric {
		step++ // проход между парами колонн — под столбы
	}
	for bx := minX + s + off; bx <= maxX+s && (maxDrills <= 0 || total < maxDrills); bx += step {
		c := &col{x: bx}
		for y := minY - s + 1; y <= maxY && (maxDrills <= 0 || total < maxDrills); y += s {
			for _, side := range []struct {
				lt  world.Tile
				dir int
			}{{world.Tile{X: bx - s, Y: y}, E}, {world.Tile{X: bx + 1, Y: y}, W}} {
				if maxDrills > 0 && total >= maxDrills {
					break
				}
				pl := Placement{Name: drill, Pos: CenterAt(l.P, drill, side.dir, side.lt), Dir: side.dir, Tag: tag}
				out := DrillOutput(l.P, pl)
				if out.X != bx {
					continue
				}
				n := 0
				for _, t := range MiningArea(l.P, pl) {
					if isOre[t] {
						n++
					}
				}
				if _, mixed := l.OreUnder(pl, ore); mixed || n < half || !l.CanPlace(pl, AnyLand) {
					continue
				}
				l.Add(pl)
				c.drills = append(c.drills, pl)
				c.outs = append(c.outs, out)
				total++
			}
		}
		if len(c.drills) > 0 {
			cols = append(cols, c)
		}
	}
	if total == 0 {
		return MineResult{}, fmt.Errorf("layout: на месторождении %s негде поставить %s", ore, drill)
	}
	// сборная лента ниже самого нижнего бура
	bottom := minY
	for _, c := range cols {
		for _, d := range c.drills {
			for _, t := range Tiles(l.P, d) {
				bottom = max(bottom, t.Y)
			}
		}
	}
	first, last := cols[0].x, cols[len(cols)-1].x
	for _, c := range cols {
		sort.Slice(c.outs, func(i, j int) bool { return c.outs[i].Y < c.outs[j].Y })
	}
	// сборная лента — ниже самого нижнего бура; ряд занят (другая шахта, лента) — ниже
	yc, found := 0, false
	for y := bottom + 1; y <= bottom+20 && !found; y++ {
		ok := l.Free(world.Tile{X: last + 1, Y: y}, AnyLand)
		for x := first; x <= last && ok; x++ {
			ok = l.Free(world.Tile{X: x, Y: y}, AnyLand)
		}
		for _, c := range cols {
			for yy := c.outs[0].Y; yy < y && ok; yy++ {
				t := world.Tile{X: c.x, Y: yy}
				if p := l.At(t); p != nil && p.Tag != tag {
					ok = false
				} else if p == nil && !l.Free(t, AnyLand) {
					ok = false
				}
			}
		}
		if ok {
			yc, found = y, true
		}
	}
	if !found {
		return MineResult{}, fmt.Errorf("layout: шахта %s: некуда вывести сборную ленту", ore)
	}
	for _, c := range cols {
		for y := c.outs[0].Y; y < yc; y++ {
			t := world.Tile{X: c.x, Y: y}
			if l.At(t) == nil {
				l.Add(Placement{Name: "transport-belt", Pos: t.Center(), Dir: S, Tag: tag})
			}
		}
	}
	for x := first; x <= last; x++ {
		l.Add(Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: yc}.Center(), Dir: E, Tag: tag})
	}
	out := world.Tile{X: last + 1, Y: yc}
	e := l.P.Entities[drill]
	rate := float64(total) * e.MiningSpeed
	if res, ok := l.P.Entities[ore]; ok && res.Mineable != nil && res.Mineable.MiningTime > 0 {
		rate /= res.Mineable.MiningTime
	}
	return MineResult{Drills: total, Rate: rate, Out: out, OutDir: E}, nil
}
