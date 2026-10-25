package cluster

import (
	"fmt"
	"math"
	"slices"
	"sort"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Corridor — логистический коридор вокруг Area кластера: ленты, трубы, столбы, проход.
// Внутри зон (zones.md) у кластера бордюр ZoneBorder.
const Corridor = 6

// ZoneBorder — бордюр кластера внутри зоны: у соседей 3+3 = коридор 6.
const ZoneBorder = 3

// Rect — прямоугольник тайлов: Min включительно, Max не включая.
type Rect struct {
	Min, Max world.Tile
}

func (r Rect) W() int { return r.Max.X - r.Min.X }
func (r Rect) H() int { return r.Max.Y - r.Min.Y }

// Grow — шире на d со всех сторон.
func (r Rect) Grow(d int) Rect {
	return Rect{world.Tile{X: r.Min.X - d, Y: r.Min.Y - d}, world.Tile{X: r.Max.X + d, Y: r.Max.Y + d}}
}

func (r Rect) Overlaps(o Rect) bool {
	return r.Min.X < o.Max.X && o.Min.X < r.Max.X && r.Min.Y < o.Max.Y && o.Min.Y < r.Max.Y
}

// Center — центр в координатах тайлов.
func (r Rect) Center() (float64, float64) {
	return float64(r.Min.X+r.Max.X) / 2, float64(r.Min.Y+r.Max.Y) / 2
}

func (r Rect) String() string {
	return fmt.Sprintf("(%d,%d) %d×%d", r.Min.X, r.Min.Y, r.W(), r.H())
}

// Base — размеченная карта: кластеры с местами. Месторождения + Corridor — только добыче;
// Area + Corridor разных кластеров не пересекаются (у каждого свой коридор).
type Base struct {
	P        *knowledge.Prototypes
	W        *world.World
	Origin   world.Tile // центр базы (прикидка — место у крупных пятен угля, железа и меди)
	Clusters []*Cluster
	MaxR     int // радиус поиска места от якоря, тайлов
	// Avoid — занятые тайлы (раскладки прошлых задач): ни кластеров, ни логистики
	Avoid map[world.Tile]bool
	// Usable — доступна ли постройка (изучено); nil — всё (подземки — после logistics)
	Usable func(entity string) bool
	// Log — журнал прокладки (попытки, время); nil — молча
	Log func(format string, args ...any)
	// Rough — прикидка базы целиком (шина — с запасом +10%)
	Rough bool
	// CliffZones — база с утёсами (zones.md): в зонах производства утёсы любой толщины можно
	// (вода — нет), зоны шире на половину
	CliffZones bool

	// StorageNear — куда тянуть хранение (стартовые буры); nil — к середине шин
	StorageNear *world.Tile
	// feedLane — выход блока добычи → Y его ленты bus-1 (orderedMineLanes)
	feedLane map[world.Tile]int
	// Keep — что кластерам без зоны не задевать (стартовые точки до разборки)
	Keep []Rect

	mineZones []Zone // прикидка: зоны добычи (MineZones)
	zoneRects []Rect // зоны (PlaceZoned): кластеры без зоны — вне их

	g *grid
}

func NewBase(p *knowledge.Prototypes, w *world.World, origin world.Tile) *Base {
	return &Base{P: p, W: w, Origin: origin, MaxR: 400}
}

// grid — открытая карта в массивах с префиксными суммами: проверка прямоугольника за O(1).
type grid struct {
	x0, y0, w, h int
	bad          []int32 // нельзя строить: не суша, неубираемое, зона месторождения
	ring         []int32 // занято Area+Corridor другого кластера
	water        []int32
	ore          map[string][]int32 // руда по ресурсу
	unknown      []int32            // неоткрыто
	avoid        []int32            // Base.Avoid
	zone         []int32            // зона месторождения (руда + Corridor)
	thick        []int32            // непроходимое (вода, утёсы…) подряд больше maxGap — подземкой не пройти
	thickWet     []int32            // то же, только вода и неоткрытое (база с утёсами)
	rowThick     []int32            // неровности длиннее busGap вдоль ряда (X): ряду ленты шины не подходит
}

// maxGap — сколько тайлов непроходимого подряд пройдёт подземка (этажная база, cluster.md 5а).
const maxGap = 3

// busGap — неровность длиннее стольких тайлов вдоль ряда ленты шины подземка не перепрыгнет (жёлтая —
// 4 тайла под землёй): такой ряд шине не подходит (решение пользователя)
const busGap = 4

func (b *Base) grid() *grid {
	if b.g != nil {
		return b.g
	}
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for cp := range b.W.Chunks {
		minX, minY = min(minX, cp.X), min(minY, cp.Y)
		maxX, maxY = max(maxX, cp.X), max(maxY, cp.Y)
	}
	g := &grid{ore: map[string][]int32{}}
	if minX > maxX {
		b.g = g
		return g
	}
	cs := world.ChunkSize
	g.x0, g.y0 = minX*cs, minY*cs
	g.w, g.h = (maxX-minX+1)*cs, (maxY-minY+1)*cs
	n := (g.w + 1) * (g.h + 1)
	bad, water, unknown := make([]bool, g.w*g.h), make([]bool, g.w*g.h), make([]bool, g.w*g.h)
	ores := map[string][]bool{}
	zone := make([]bool, g.w*g.h)
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			t := world.Tile{X: g.x0 + x, Y: g.y0 + y}
			i := y*g.w + x
			switch b.W.TileAt(t) {
			case world.Land:
				ok, _ := b.W.Buildable(t)
				bad[i] = !ok
			case world.Water, world.Shallow:
				bad[i], water[i] = true, true
			default:
				bad[i], unknown[i] = true, true
			}
			if r, ok := b.W.ResourceAt(t); ok {
				if ores[r.Name] == nil {
					ores[r.Name] = make([]bool, g.w*g.h)
				}
				ores[r.Name][i] = true
				// зона месторождения: руда + Corridor
				for dy := -Corridor; dy <= Corridor; dy++ {
					for dx := -Corridor; dx <= Corridor; dx++ {
						if xx, yy := x+dx, y+dy; xx >= 0 && yy >= 0 && xx < g.w && yy < g.h {
							zone[yy*g.w+xx] = true
						}
					}
				}
			}
		}
	}
	avoid := make([]bool, g.w*g.h)
	for t := range b.Avoid {
		if x, y := t.X-g.x0, t.Y-g.y0; x >= 0 && y >= 0 && x < g.w && y < g.h {
			bad[y*g.w+x], avoid[y*g.w+x] = true, true
		}
	}
	g.avoid = prefix(avoid, g.w, g.h)
	g.zone = prefix(zone, g.w, g.h)
	g.thick = prefix(thickRuns(bad, g.w, g.h), g.w, g.h)
	g.rowThick = prefix(rowRuns(bad, g.w, g.h, busGap), g.w, g.h)
	wet := make([]bool, len(bad))
	for i := range wet {
		wet[i] = water[i] || unknown[i]
	}
	g.thickWet = prefix(thickRuns(wet, g.w, g.h), g.w, g.h)
	for i := range bad {
		bad[i] = bad[i] || zone[i]
	}
	g.bad, g.water, g.unknown = prefix(bad, g.w, g.h), prefix(water, g.w, g.h), prefix(unknown, g.w, g.h)
	for name, o := range ores {
		g.ore[name] = prefix(o, g.w, g.h)
	}
	g.ring = make([]int32, n)
	b.g = g
	for _, c := range b.Clusters { // уже размеченные кластеры
		g.mark(b.occupied(c))
	}
	return g
}

// occupied — что кластер c занимает в разметке: Area с бордюром; в зоне производства — весь
// столбец зоны (рост вдоль ряда, друг над другом кластеры не ставятся).
func (b *Base) occupied(c *Cluster) Rect {
	occ := c.Area.Grow(b.border(c))
	if c.Upright && c.Zone != nil {
		occ.Min.Y, occ.Max.Y = min(occ.Min.Y, c.Zone.Min.Y), max(occ.Max.Y, c.Zone.Max.Y)
	}
	return occ
}

func prefix(v []bool, w, h int) []int32 {
	s := make([]int32, (w+1)*(h+1))
	for y := 0; y < h; y++ {
		row := int32(0)
		for x := 0; x < w; x++ {
			if v[y*w+x] {
				row++
			}
			s[(y+1)*(w+1)+x+1] = s[y*(w+1)+x+1] + row
		}
	}
	return s
}

// count — сколько отмеченных в r; out — тайлов r вне открытой карты.
func (g *grid) count(s []int32, r Rect) (n, out int) {
	x0, y0 := max(r.Min.X-g.x0, 0), max(r.Min.Y-g.y0, 0)
	x1, y1 := min(r.Max.X-g.x0, g.w), min(r.Max.Y-g.y0, g.h)
	if x1 <= x0 || y1 <= y0 {
		return 0, r.W() * r.H()
	}
	W := g.w + 1
	n = int(s[y1*W+x1] - s[y0*W+x1] - s[y1*W+x0] + s[y0*W+x0])
	return n, r.W()*r.H() - (x1-x0)*(y1-y0)
}

// thickRuns — тайлы в отрезках непроходимого длиннее maxGap по горизонтали или вертикали.
// rowRuns — тайлы, входящие в отрезки bad вдоль X длиннее n.
func rowRuns(bad []bool, w, h, n int) []bool {
	res := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for k := 0; k < w; {
			if !bad[y*w+k] {
				k++
				continue
			}
			e := k
			for e < w && bad[y*w+e] {
				e++
			}
			if e-k > n {
				for j := k; j < e; j++ {
					res[y*w+j] = true
				}
			}
			k = e
		}
	}
	return res
}

func thickRuns(bad []bool, w, h int) []bool {
	res := make([]bool, w*h)
	run := func(at func(k int) int, n int) {
		for k := 0; k < n; {
			if !bad[at(k)] {
				k++
				continue
			}
			e := k
			for e < n && bad[at(e)] {
				e++
			}
			if e-k > maxGap {
				for j := k; j < e; j++ {
					res[at(j)] = true
				}
			}
			k = e
		}
	}
	for y := 0; y < h; y++ {
		run(func(k int) int { return y*w + k }, w)
	}
	for x := 0; x < w; x++ {
		run(func(k int) int { return k*w + x }, h)
	}
	return res
}

// mark — отметить r в ring (пересчёт префиксных сумм).
func (g *grid) mark(r Rect) {
	W := g.w + 1
	v := make([]bool, g.w*g.h)
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			c := g.ring[(y+1)*W+x+1] - g.ring[y*W+x+1] - g.ring[(y+1)*W+x] + g.ring[y*W+x]
			t := world.Tile{X: g.x0 + x, Y: g.y0 + y}
			v[y*g.w+x] = c > 0 || t.X >= r.Min.X && t.X < r.Max.X && t.Y >= r.Min.Y && t.Y < r.Max.Y
		}
	}
	g.ring = prefix(v, g.w, g.h)
}

// size — Area кластера: вдоль — Len (Room мест по Pitch), поперёк — Cross; horizontal — ряд вдоль X.
func size(c *Cluster, horizontal bool) (int, int) {
	l := c.Len()
	if horizontal {
		return l, c.Cross
	}
	return c.Cross, l
}

// Place — место кластеру (не добыче — она блоками на пятна, placeMines): в зоне — по
// столбцам (pack), станция — у воды, прочие — на свободной суше вне зон; ближе к якорю (Near
// или центр поставщиков).
func (b *Base) Place(c *Cluster) error {
	if b.grid().w == 0 {
		return fmt.Errorf("cluster: карта пуста")
	}
	best := b.find(c)
	if best == nil {
		return &NoSiteError{c, fmt.Errorf("cluster: нет места под %s (%d×%d в %d тайлах)", c.Name, c.Cross, c.Len(), b.MaxR)}
	}
	c.Area, c.Rect, c.Vertical = best.r, best.r, best.v
	// занято сейчас — начало ряда
	l := c.Head + c.Count*c.Pitch
	if !c.Vertical {
		c.Rect.Max.X = c.Rect.Min.X + l
	} else if c.Upright { // головой вниз
		c.Rect.Min.Y = c.Rect.Max.Y - l
	} else {
		c.Rect.Max.Y = c.Rect.Min.Y + l
	}
	b.g.mark(b.occupied(c))
	b.Clusters = append(b.Clusters, c)
	return nil
}

// border — коридор вокруг кластера: внутри зоны — бордюр ZoneBorder, иначе Corridor.
func (b *Base) border(c *Cluster) int {
	if c.Kind == Mine {
		return MineBorder // блок добычи (cluster.md §2а «Добыча»)
	}
	if c.Zone != nil {
		return ZoneBorder
	}
	return Corridor
}

// NoSiteError — кластеру нет места на открытой карте (добыче — нет свободного
// месторождения: разведать новое).
type NoSiteError struct {
	Cluster *Cluster
	Err     error
}

func (e *NoSiteError) Error() string { return e.Err.Error() }

type cand struct {
	r Rect
	d float64
	v bool
}

// find — ближайшее к якорю место под c (как сейчас: Room) или nil.
func (b *Base) find(c *Cluster) *cand {
	if c.Upright && c.Zone != nil {
		return b.pack(c)
	}
	ax, ay := b.anchor(c)
	var best *cand
	try := func(r Rect, vertical bool) {
		x, y := r.Center()
		d := math.Hypot(x-ax, y-ay)
		if best != nil && d >= best.d {
			return
		}
		if !b.fits(c, r, vertical) {
			return
		}
		best = &cand{r, d, vertical}
	}
	for rr := 0; rr <= b.MaxR; rr++ {
		if best != nil && float64(rr) > best.d+1 {
			break
		}
		for _, horizontal := range []bool{true, false} {
			if horizontal && c.Upright {
				continue
			}
			w, h := size(c, horizontal)
			cx, cy := int(math.Round(ax))-w/2, int(math.Round(ay))-h/2
			for _, o := range ringOffsets(rr) {
				try(Rect{world.Tile{X: cx + o.X, Y: cy + o.Y}, world.Tile{X: cx + o.X + w, Y: cy + o.Y + h}}, !horizontal)
			}
		}
	}
	return best
}

// pack — место в зоне производства (zones.md): плотно слева направо — первое, что влезает, по
// столбцам от левого края, в столбце — от низа зоны. Столбец кластера целиком (на всю глубину
// зоны) — его (Place): друг над другом кластеры не ставятся.
func (b *Base) pack(c *Cluster) *cand {
	z := *c.Zone
	w, h := size(c, false)
	if c.Kind == Lab { // лаборатории — в правый край этажа: вся наука (и будущая) — левее, выше по течению
		for x := z.Max.X - w; x >= z.Min.X; x-- {
			for y := z.Max.Y - h; y >= z.Min.Y; y-- {
				r := Rect{world.Tile{X: x, Y: y}, world.Tile{X: x + w, Y: y + h}}
				if b.fits(c, r, true) {
					return &cand{r, 0, true}
				}
			}
		}
		return nil
	}
	for x := z.Min.X; x+w <= z.Max.X; x++ {
		for y := z.Max.Y - h; y >= z.Min.Y; y-- {
			r := Rect{world.Tile{X: x, Y: y}, world.Tile{X: x + w, Y: y + h}}
			if b.fits(c, r, true) {
				return &cand{r, 0, true}
			}
		}
	}
	return nil
}

// fits — r годится кластеру c.
func (b *Base) fits(c *Cluster, r Rect, vertical bool) bool {
	g := b.g
	ring := r.Grow(b.border(c))
	if z := c.Zone; z != nil && (r.Min.X < z.Min.X || r.Min.Y < z.Min.Y || r.Max.X > z.Max.X || r.Max.Y > z.Max.Y) {
		return false // только внутри своей зоны
	}
	if c.MinX != nil && r.Min.X < *c.MinX {
		return false // ниже по течению шины, чем поставщики входов
	}
	if c.Zone == nil {
		for _, z := range append(b.zoneRects, b.Keep...) {
			if ring.Overlaps(z) {
				return false // вне зон и стартовых точек
			}
		}
	}
	if n, out := g.count(g.ring, ring); n > 0 || out > 0 {
		return false
	}
	if n, _ := g.count(g.unknown, ring); n > 0 {
		return false
	}
	// станция у воды: вода в коридоре
	if n, _ := g.count(g.water, ring); c.Kind == Power && n == 0 {
		return false
	}
	n, _ := g.count(g.bad, r)
	return n == 0
}

// anchor — куда тянуть кластер: Near; лаборатории — к кластерам науки;
// прочие (и станция — к углю: лента дороже столбов) — к центру своих поставщиков (по
// потоку), без них — к центру базы.
func (b *Base) anchor(c *Cluster) (float64, float64) {
	if c.Near != nil {
		return float64(c.Near.X), float64(c.Near.Y)
	}
	ox, oy := float64(b.Origin.X), float64(b.Origin.Y)
	var sx, sy, sw float64
	for _, o := range b.Clusters {
		w := 0.0
		for _, in := range c.In {
			for _, out := range o.Out {
				if in.Item == out.Item {
					w += in.Rate
				}
			}
		}
		if w > 0 {
			x, y := o.Area.Center()
			sx, sy, sw = sx+x*w, sy+y*w, sw+w
		}
	}
	if sw == 0 {
		return ox, oy
	}
	return sx / sw, sy / sw
}

// ringOffsets — смещения на квадратном кольце радиуса r (r = 0 — центр).
func ringOffsets(r int) []world.Tile {
	if r == 0 {
		return []world.Tile{{}}
	}
	var res []world.Tile
	for i := -r; i <= r; i++ {
		res = append(res, world.Tile{X: i, Y: -r}, world.Tile{X: i, Y: r})
		if i != -r && i != r {
			res = append(res, world.Tile{X: -r, Y: i}, world.Tile{X: r, Y: i})
		}
	}
	return res
}

// scaleCluster — копия кластера с машинами, потоками и мощностью ×k.
func scaleCluster(c *Cluster, k float64) *Cluster {
	p := *c
	scale := func(fs []Flow) []Flow {
		var out []Flow
		for _, f := range fs {
			out = append(out, Flow{f.Item, f.Rate * k})
		}
		return out
	}
	p.In, p.Out, p.Internal, p.PowerW = scale(c.In), scale(c.Out), scale(c.Internal), c.PowerW*k
	p.Units = nil
	for _, u := range c.Units {
		u.Machines *= k
		p.Units = append(p.Units, u)
	}
	return &p
}

// short — короткие имена продуктов для кластеров.
var short = map[string]string{
	"automation-science-pack": "red-science",
	"logistic-science-pack":   "green-science",
	"chemical-science-pack":   "blue-science",
	"production-science-pack": "purple-science",
	"utility-science-pack":    "yellow-science",
	"crude-oil":               "oil",
	"electronic-circuit":      "circuit",
	"advanced-circuit":        "adv-circuit",
	"iron-gear-wheel":         "gear",
}

// name — следующее свободное имя продукта.
// name — свободное имя кластера product: не занятое ни в базе, ни среди also (кластеры, которым
// имена дают сейчас — два хранения в одном плане оба были storage-1).
// Name — свободное имя кластера продукта product (с also — ещё не в базе).
func (b *Base) Name(product string, also []*Cluster) string { return b.name(product, also) }

func (b *Base) name(product string, also []*Cluster) string {
	if s, ok := short[product]; ok {
		product = s
	}
	used := map[string]bool{}
	for _, c := range append(slices.Clone(b.Clusters), also...) {
		used[c.Name] = true
	}
	for i := 1; ; i++ {
		if n := fmt.Sprintf("%s-%d", product, i); !used[n] {
			return n
		}
	}
}

// Sorted — кластеры по имени (для вывода).
func (b *Base) Sorted() []*Cluster {
	res := append([]*Cluster(nil), b.Clusters...)
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res
}

// Tag — тег размещений кластера в раскладке (стройка — plan.Built с этим тегом).
func Tag(name string) string { return "c/" + name }

// Layout — раскладка кластеров fresh (нет — всех): внутренности (Interior); Area всех
// кластеров базы закрыты (маршруты потом идут только по коридорам).
func (b *Base) Layout(fresh ...*Cluster) (*layout.Layout, error) {
	if len(fresh) == 0 {
		fresh = b.Clusters
	}
	l := layout.New(b.P, b.W)
	l.Usable = b.Usable
	for t := range b.Avoid {
		l.Reserved[t] = true
	}
	area := func(c *Cluster, on bool) {
		for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
			for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
				t := world.Tile{X: x, Y: y}
				if on && l.At(t) == nil {
					l.Reserved[t] = true
				} else if !on {
					delete(l.Reserved, t)
				}
			}
		}
	}
	// добыча — сплошная: чужие ленты и трубы не идут через неё и подземкой (правило пользователя)
	l.Solid = map[world.Tile]bool{}
	solid := func(c *Cluster, on bool) {
		if c.Kind != Mine {
			return
		}
		for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
			for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
				if t := (world.Tile{X: x, Y: y}); on {
					l.Solid[t] = true
				} else {
					delete(l.Solid, t)
				}
			}
		}
	}
	for _, c := range b.Clusters {
		if c.Kind == Bus {
			continue // ленты шины — в ConnectZoned
		}
		area(c, true)
		solid(c, true)
	}
	for _, c := range fresh {
		if c.Kind == Bus {
			continue // ленты шины — в ConnectZoned (по потребителям)
		}
		area(c, false)
		solid(c, false) // своя — внутри можно
		if err := Interior(l, c, Tag(c.Name)); err != nil {
			return l, err
		}
		area(c, true)
		solid(c, true)
	}
	return l, nil
}
