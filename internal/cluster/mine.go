package cluster

import (
	"fmt"
	"math"
	"slices"
	"sort"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Добыча блоком на пятно (cluster.md §2а «Добыча»): площадь — рамка пятна своего ресурса в своей
// зоне (+2 тайла — зона добычи бура), ряды — вдоль длинной стороны, поперёк — по 7; коридор — 2
// тайла; пятна рядом делят спорное место поровну.

// Пределы руды под бурами: первая итерация — ближние месторождения целиком или до OreFirst,
// со второй — по OreNext.
const (
	OreFirst = 500_000
	OreNext  = 2_000_000
)

// MineBorder — коридор вокруг блока добычи (между соседними блоками — вдвое).
const MineBorder = 2

// mineSpot — найденное место блока: зона и рамка.
type mineSpot struct {
	c    *Cluster
	z    Zone
	r    Rect
	head world.Tile // голова шины ресурсов: сюда смотрит голова блока
}

// mineRect — рамка блока добычи c на ближнем к head пятне его ресурса в зоне z (рамка пятна +2,
// в пределах зоны). false — пятна в зоне нет.
func (b *Base) mineRect(c *Cluster, z Zone, head world.Tile) (Rect, bool) {
	best, bd := Rect{}, math.Inf(1)
	for _, p := range b.W.Patches() {
		if p.Name != c.Product {
			continue
		}
		r := Rect{world.Tile{X: p.Min.X - 2, Y: p.Min.Y - 2}, world.Tile{X: p.Max.X + 3, Y: p.Max.Y + 3}}
		r = Rect{world.Tile{X: max(r.Min.X, z.Rect.Min.X), Y: max(r.Min.Y, z.Rect.Min.Y)},
			world.Tile{X: min(r.Max.X, z.Rect.Max.X), Y: min(r.Max.Y, z.Rect.Max.Y)}}
		if r.W() < 7 || r.H() < 7 {
			continue // вне зоны или узко: ни одного ряда (7 поперёк) не встанет
		}
		if p.Amount < minBlockOre || !blockHasDrill(b.W, r, c.Product) {
			continue // клочок в зоне у большого пятна: ни одного бура (17466282, стадия 3: железо 8×8)
		}
		if c.ToPower && float64(p.Amount)*MineCover < float64(c.Ore) {
			continue // уголь станции — одним блоком: пятно на всю цель
		}
		if d := math.Hypot(p.Center.X-float64(head.X), p.Center.Y-float64(head.Y)); d < bd {
			best, bd = r, d
		}
	}
	return best, !math.IsInf(bd, 1)
}

// shareMines — пятна рядом делят спорное место поровну: рамки с коридорами пересекаются —
// пересечение режется посередине поперёк его узкой стороны, каждому — своя половина.
func shareMines(spots []*mineSpot) {
	for i := range spots {
		for j := i + 1; j < len(spots); j++ {
			a, b := spots[i], spots[j]
			ga, gb := a.r.Grow(MineBorder), b.r.Grow(MineBorder)
			if !ga.Overlaps(gb) {
				continue
			}
			in := Rect{world.Tile{X: max(ga.Min.X, gb.Min.X), Y: max(ga.Min.Y, gb.Min.Y)},
				world.Tile{X: min(ga.Max.X, gb.Max.X), Y: min(ga.Max.Y, gb.Max.Y)}}
			ax, ay := a.r.Center()
			bx, by := b.r.Center()
			if in.W() <= in.H() { // режем по X: граница — середина пересечения
				m := (in.Min.X + in.Max.X) / 2
				left, right := a, b
				if ax > bx {
					left, right = b, a
				}
				left.r.Max.X = min(left.r.Max.X, m-MineBorder)
				right.r.Min.X = max(right.r.Min.X, m+MineBorder)
			} else { // по Y
				m := (in.Min.Y + in.Max.Y) / 2
				top, bottom := a, b
				if ay > by {
					top, bottom = b, a
				}
				top.r.Max.Y = min(top.r.Max.Y, m-MineBorder)
				bottom.r.Min.Y = max(bottom.r.Min.Y, m+MineBorder)
			}
		}
	}
}

// placeMine — блок добычи на рамке s.r: мимо уже размеченных кластеров (срезать полосу со стороны
// соседа, оставить большую часть), ряды — вдоль длинной стороны по 7. false — места не осталось.
func (b *Base) placeMine(s *mineSpot) bool {
	r, ok := b.fitMine(s)
	if !ok {
		return false
	}
	b.commitMine(s, r)
	return true
}

// fitMine — рамка блока s мимо уже размеченных кластеров, ряды по 7 (без разметки). false — места
// не осталось.
func (b *Base) fitMine(s *mineSpot) (Rect, bool) {
	c, r := s.c, s.r
	if c.Ore == 0 {
		c.Ore = OreFirst
	}
	for k := 0; k < 8; k++ {
		var hit *Rect
		for _, o := range b.Clusters {
			if occ := b.occupied(o); occ.Overlaps(r.Grow(MineBorder)) {
				hit = &occ
				break
			}
		}
		if hit == nil {
			break
		}
		g := hit.Grow(MineBorder)
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
			return Rect{}, false
		}
		r = bestR
	}
	vertical := r.H() > r.W()
	cross := r.H()
	if vertical {
		cross = r.W()
	}
	rows := cross / 7
	if rows == 0 || r.W() < 4 || r.H() < 4 {
		return Rect{}, false
	}
	if vertical {
		r.Max.X = r.Min.X + 7*rows
	} else {
		r.Max.Y = r.Min.Y + 7*rows
	}
	return r, true
}

// commitMine — блок s на рамке r (из fitMine): ряды, голова к шине, разметка.
func (b *Base) commitMine(s *mineSpot, r Rect) {
	c := s.c
	c.Vertical = r.H() > r.W()
	c.Cross = r.H() / 7 * 7
	if c.Vertical {
		c.Cross = r.W() / 7 * 7
	}
	c.Area, c.Rect = r, r
	// голова (лента сбора, оба выхода) — к шине ресурсов: ленты к ней — без обхода блока
	cx, cy := r.Center()
	if c.Vertical {
		c.Back = float64(s.head.Y) > cy
	} else {
		c.Back = float64(s.head.X) > cx
	}
	// но выходы — не в воду и утёсы (264771462: оба выхода блока угля смотрели в озеро, уголь на
	// станцию не провёлся, станцию переставляли 6 раз): с той стороны закрыто — голова к другому
	// концу блока
	exitsOK := func(back bool) bool {
		cc := *c
		cc.Back = back
		f := cc.frame()
		for _, p := range []struct{ v, d int }{{-1, layout.N}, {c.Cross, layout.S}} {
			t := f.tile(0, p.v)
			if ok, _ := b.W.Buildable(t); !ok {
				return false
			}
			if ok, _ := b.W.Buildable(layout.Step(t, f.dir(p.d))); !ok {
				return false
			}
		}
		return true
	}
	if !exitsOK(c.Back) && exitsOK(!c.Back) {
		c.Back = !c.Back
	}
	zr := s.z.Rect
	c.Zone = &zr
	if b.g != nil {
		b.g.mark(b.occupied(c))
	}
	b.Clusters = append(b.Clusters, c)
}

// MoreMines — блоки добычи ресурса it на месторождениях целиком (stages.md, стадия 5), пока руды
// под бурами меньше want: have — сколько уже под бурами; пятна в зонах добычи — ближние к голове
// шины ресурсов первыми, уже занятые блоками — остатком мимо них; остаток меньше minBlockOre —
// мимо. like — кластер добычи для образца (машина); Ore — без предела (пятно целиком).
func (b *Base) MoreMines(like *Cluster, zones []Zone, have, want int) ([]*Cluster, error) {
	b.grid()
	var head world.Tile
	for _, z := range zones {
		if z.Name == "bus-1" {
			head = z.Rect.Min
		}
	}
	type cand struct {
		z Zone
		r Rect
		d float64
	}
	var cs []cand
	for _, z := range zones {
		if z.Kind != ZoneMine || z.Resources[like.Product] == 0 {
			continue
		}
		for _, p := range b.W.Patches() {
			if p.Name != like.Product {
				continue
			}
			r := Rect{world.Tile{X: max(p.Min.X-2, z.Rect.Min.X), Y: max(p.Min.Y-2, z.Rect.Min.Y)},
				world.Tile{X: min(p.Max.X+3, z.Rect.Max.X), Y: min(p.Max.Y+3, z.Rect.Max.Y)}}
			if r.W() < 7 || r.H() < 7 {
				continue
			}
			cs = append(cs, cand{z, r, math.Hypot(p.Center.X-float64(head.X), p.Center.Y-float64(head.Y))})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	var res []*Cluster
	for _, k := range cs {
		for have < want { // пятно может дать и несколько блоков (остатки мимо соседей)
			// буры — до остатка цели под бурами (stages.md, стадия 30: «ещё по 2M»), не на всё пятно
			c := &Cluster{Kind: Mine, Product: like.Product, Reserve: like.Reserve, Ore: want - have,
				Units: []Unit{{Recipe: like.Units[0].Recipe, Machine: like.Units[0].Machine, Machines: 1}}}
			s := &mineSpot{c, k.z, k.r, head}
			r, ok := b.fitMine(s)
			if !ok || b.avoids(r) {
				break
			}
			ore := int(float64(b.oreIn(r, c.Product)) * MineCover)
			if ore < minBlockOre || !blockHasDrill(b.W, r, c.Product) {
				break // мало руды или ни одного бура в блоке не встанет (158846952: уголь 68k в 19×7)
			}
			b.commitMine(s, r)
			c.Name = b.name(c.Product, res)
			res = append(res, c)
			have += min(ore, c.Ore)
			b.logf("добыча: %s на %v — руды %d, всего %d из %d", c.Name, r, ore, have, want)
		}
		if have >= want {
			break
		}
	}
	if have < want {
		return res, fmt.Errorf("cluster: в зонах добычи %s под бурами %d из %d", like.Product, have, want)
	}
	return res, nil
}

// blockHasDrill — в блоке добычи r хоть один бур встанет так, как ставит mineBlock: ряды пар
// поперёк короткой стороны (буры на 0 и 4 от начала ряда, ряды через 7), под зоной 5×5 больше
// половины — свой ресурс, чужого нет, бур 3×3 — на суше.
func blockHasDrill(w *world.World, r Rect, product string) bool {
	vertical := r.W() < r.H() // ряд вдоль длинной стороны
	long, short := r.W(), r.H()
	if vertical {
		long, short = short, long
	}
	back := false // голова блока — у Min или у Max (к шине ресурсов, Back): проверяются оба
	at := func(u, v int) world.Tile {
		if back {
			u = long - 1 - u
		}
		if vertical {
			return world.Tile{X: r.Min.X + v, Y: r.Min.Y + u}
		}
		return world.Tile{X: r.Min.X + u, Y: r.Min.Y + v}
	}
	fits := func(c world.Tile) bool {
		n := 0
		for y := c.Y - 2; y <= c.Y+2; y++ {
			for x := c.X - 2; x <= c.X+2; x++ {
				if res, ok := w.ResourceAt(world.Tile{X: x, Y: y}); ok {
					if res.Name != product {
						return false
					}
					n++
				}
			}
		}
		for y := c.Y - 1; y <= c.Y+1; y++ {
			for x := c.X - 1; x <= c.X+1; x++ {
				if ok, _ := w.Buildable(world.Tile{X: x, Y: y}); !ok {
					return false
				}
			}
		}
		return n > 12
	}
	land := func(u, v int) bool { ok, _ := w.Buildable(at(u, v)); return ok }
	for _, back = range []bool{false, true} {
		for v0 := 0; v0+7 <= short; v0 += 7 {
			// лента сбора (u = 0) поперёк ряда; лента ряда (v0+3) — от головы до бура
			if !land(0, v0) || !land(0, v0+3) {
				continue
			}
			for k := 0; 1+7*k+3 <= long; k++ {
				for _, u := range []int{1 + 7*k, 5 + 7*k} { // левый край бура — как mineBlock
					if u+3 > long {
						continue
					}
					belt := true
					for x := 1; x <= u+2 && belt; x++ {
						belt = land(x, v0+3)
					}
					if !belt {
						break
					}
					for _, dv := range []int{1, 5} {
						if fits(at(u+1, v0+dv)) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// MineCover — доля руды пятна, что окажется под бурами блока (края, тайлы с рудой меньше половины
// под буром — без буров): оценка до раскладки (954176679: угля 1.79M вместо 2M).
const MineCover = 0.85

// minBlockOre — блок добычи меньше — не ставится (обрезок пятна у соседа).
const minBlockOre = 30_000

// oreIn — руды ресурса it в рамке r.
func (b *Base) oreIn(r Rect, it string) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if res, ok := b.W.ResourceAt(world.Tile{X: x, Y: y}); ok && res.Name == it {
				n += res.Amount
			}
		}
	}
	return n
}

// placeMines — блоки добычи cs: у каждого — ближнее к head пятно его ресурса в зонах добычи
// (zones — по близости к head), пятна рядом делят место поровну, потом разметка.
func (b *Base) placeMines(cs []*Cluster, zones []Zone, head world.Tile) error {
	var spots []*mineSpot
	for _, c := range cs {
		found := false
		at, zs := head, zones // уголь станции — ближе к станции (Near — зона энергии)
		if c.Near != nil {
			at, zs = *c.Near, slices.Clone(zones)
			sort.SliceStable(zs, func(i, j int) bool {
				xi, yi := zs[i].Rect.Center()
				xj, yj := zs[j].Rect.Center()
				return math.Hypot(xi-float64(at.X), yi-float64(at.Y)) < math.Hypot(xj-float64(at.X), yj-float64(at.Y))
			})
		}
		for _, z := range zs {
			if z.Resources[c.Product] == 0 {
				continue
			}
			if r, ok := b.mineRect(c, z, at); ok {
				// пятно уже под другим блоком добычи — к следующему (новый блок
				// железа шёл на стартовое пятно, где стоит iron-ore-1 — 238178543)
				// или под запретом (подвод отсюда до bus-1 не провёлся — stages.md, стадия 2)
				if slices.ContainsFunc(b.Clusters, func(o *Cluster) bool { return o != c && o.Kind == Mine && o.Area.Overlaps(r) }) || b.avoids(r) {
					continue
				}
				if c.Ore == 0 {
					c.Ore = OreFirst
				}
				spots = append(spots, &mineSpot{c, z, r, at})
				found = true
				break
			}
		}
		if !found {
			return mineError(c)
		}
	}
	b.logf("добыча: рамки %v", spotsString(spots))
	shareMines(spots)
	b.logf("добыча: после дележа %v", spotsString(spots))
	for _, s := range spots {
		if !b.placeMine(s) {
			b.logf("добыча: %s на %v не встал", s.c.Product, s.r)
			return mineError(s.c)
		}
	}
	return nil
}

func spotsString(spots []*mineSpot) string {
	s := ""
	for _, sp := range spots {
		s += fmt.Sprintf("%s %v; ", sp.c.Product, sp.r)
	}
	return s
}

// logf — в журнал базы (нет — молча).
func (b *Base) logf(format string, args ...any) {
	if b.Log != nil {
		b.Log(format, args...)
	}
}

// mineError — блоку добычи не нашлось пятна.
func mineError(c *Cluster) error {
	return &NoSiteError{c, fmt.Errorf("cluster: в зонах добычи %s нет пятна под %s", c.Product, c.Name)}
}
