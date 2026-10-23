package cluster

import (
	"fmt"
	"math"

	"factorio-bot/internal/world"
)

// Добыча блоком на пятно (cluster.md §2а «Добыча»): площадь — рамка пятна своего ресурса в своей
// зоне (+2 тайла — зона добычи бура), ряды — вдоль длинной стороны, поперёк — по 7.

// Пределы руды под бурами: первая итерация — ближние месторождения целиком или до OreFirst,
// со второй — по OreNext.
const (
	OreFirst = 500_000
	OreNext  = 2_000_000
)

// placeMineBlock — площадь блока добычи c на ближнем к head пятне его ресурса в зоне z; мимо
// уже размеченных кластеров (рамка обрезается). false — пятна нет или не осталось места.
func (b *Base) placeMineBlock(c *Cluster, z Zone, head world.Tile) bool {
	var best *world.Patch
	bd := math.Inf(1)
	for _, p := range b.W.Patches() {
		if p.Name != c.Product {
			continue
		}
		pr := Rect{p.Min, world.Tile{X: p.Max.X + 1, Y: p.Max.Y + 1}}
		if !pr.Overlaps(z.Rect) {
			continue
		}
		if d := math.Hypot(p.Center.X-float64(head.X), p.Center.Y-float64(head.Y)); d < bd {
			pp := p
			best, bd = &pp, d
		}
	}
	if best == nil {
		return false
	}
	r := Rect{world.Tile{X: best.Min.X - 2, Y: best.Min.Y - 2}, world.Tile{X: best.Max.X + 3, Y: best.Max.Y + 3}}
	// в своей зоне
	r = Rect{world.Tile{X: max(r.Min.X, z.Rect.Min.X), Y: max(r.Min.Y, z.Rect.Min.Y)},
		world.Tile{X: min(r.Max.X, z.Rect.Max.X), Y: min(r.Max.Y, z.Rect.Max.Y)}}
	// мимо соседей: срезать полосу со стороны соседа (оставить большую часть)
	for k := 0; k < 8; k++ {
		var hit *Rect
		for _, o := range b.Clusters {
			if occ := b.occupied(o); occ.Overlaps(r.Grow(ZoneBorder)) {
				hit = &occ
				break
			}
		}
		if hit == nil {
			break
		}
		g := hit.Grow(ZoneBorder)
		cands := []Rect{
			{r.Min, world.Tile{X: min(r.Max.X, g.Min.X), Y: r.Max.Y}}, // левее соседа
			{world.Tile{X: max(r.Min.X, g.Max.X), Y: r.Min.Y}, r.Max}, // правее
			{r.Min, world.Tile{X: r.Max.X, Y: min(r.Max.Y, g.Min.Y)}}, // выше
			{world.Tile{X: r.Min.X, Y: max(r.Min.Y, g.Max.Y)}, r.Max}, // ниже
		}
		bestR, ba := Rect{}, 0
		for _, cr := range cands {
			if a := cr.W() * cr.H(); cr.W() > 0 && cr.H() > 0 && a > ba {
				bestR, ba = cr, a
			}
		}
		if ba == 0 {
			return false
		}
		r = bestR
	}
	// ряды — вдоль длинной стороны; поперёк — по 7 (неполный ряд не нужен)
	c.Vertical = r.H() > r.W()
	cross := r.H()
	if c.Vertical {
		cross = r.W()
	}
	rows := cross / 7
	if rows == 0 || r.W() < 4 || r.H() < 4 {
		return false
	}
	if c.Vertical {
		r.Max.X = r.Min.X + 7*rows
	} else {
		r.Max.Y = r.Min.Y + 7*rows
	}
	c.Cross = 7 * rows
	c.Area, c.Rect = r, r
	zr := z.Rect
	c.Zone = &zr
	if c.Ore == 0 {
		c.Ore = OreFirst
	}
	if b.g != nil {
		b.g.mark(b.occupied(c))
	}
	b.Clusters = append(b.Clusters, c)
	return true
}

// mineError — блоку добычи не нашлось пятна.
func mineError(c *Cluster) error {
	return &NoSiteError{c, fmt.Errorf("cluster: в зонах добычи %s нет пятна под %s", c.Product, c.Name)}
}
