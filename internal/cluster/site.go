package cluster

import (
	"fmt"
	"math"
	"sort"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Corridor — логистический коридор вокруг Area кластера: ленты, трубы, столбы, проход.
const Corridor = 6

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
	Origin   world.Tile // центр базы (старт, станция)
	Clusters []*Cluster
	MaxR     int // радиус поиска места от якоря, тайлов
	// Avoid — занятые тайлы (раскладки прошлых задач): ни кластеров, ни логистики
	Avoid map[world.Tile]bool
	// Usable — доступна ли постройка (изучено); nil — всё (подземки — после logistics)
	Usable func(entity string) bool
	// Log — журнал прокладки (попытки, время); nil — молча
	Log func(format string, args ...any)

	g        *grid
	oreShare int // добыча: доля руды под Area, %
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
	solid        []int32            // не построить (вода, неубираемое, свои постройки) — без зон руды
}

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
	g.solid = prefix(bad, g.w, g.h)
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
		g.mark(c.Area.Grow(Corridor))
	}
	return g
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

// Place — место кластеру: добыча — на своём месторождении, станция — у воды, прочие — на
// свободной суше вне зон месторождений; ближе к якорю (центр входов; лаборатории — у науки).
// Добыча: длинный ряд с запасом может не влезть в ближнее месторождение — тогда короче (рост
// добычи — и новым кластером), если это ближе: каждое укорочение вдвое «стоит» shrinkCost тайлов.
func (b *Base) Place(c *Cluster) error {
	if b.grid().w == 0 {
		return fmt.Errorf("cluster: карта пуста")
	}
	rooms := []int{c.Room}
	if c.Kind == Mine {
		for r := c.Room / 2; r > c.Count; r /= 2 {
			rooms = append(rooms, r)
		}
		if c.Room > c.Count {
			rooms = append(rooms, c.Count)
		}
	}
	room := c.Room
	var best *cand
	bestRoom := 0
	for k, r := range rooms {
		c.Room = r
		if f := b.find(c); f != nil && (best == nil || f.d+float64(k)*shrinkCost < best.d) {
			f.d += float64(k) * shrinkCost
			best, bestRoom = f, r
		}
	}
	if best == nil {
		c.Room = room
		return fmt.Errorf("cluster: нет места под %s (%d×%d в %d тайлах)", c.Name, c.Cross, c.Len(), b.MaxR)
	}
	c.Room = bestRoom
	c.Area, c.Rect, c.Vertical = best.r, best.r, best.v
	// занято сейчас — начало ряда
	l := c.Head + c.Count*c.Pitch
	if !c.Vertical {
		c.Rect.Max.X = c.Rect.Min.X + l
	} else {
		c.Rect.Max.Y = c.Rect.Min.Y + l
	}
	b.g.mark(c.Area.Grow(Corridor))
	b.Clusters = append(b.Clusters, c)
	return nil
}

// shrinkCost — «цена» укорочения ряда добычи вдвое, тайлов расстояния.
const shrinkCost = 15

type cand struct {
	r Rect
	d float64
	v bool
}

// find — ближайшее к якорю место под c (как сейчас: Room) или nil.
func (b *Base) find(c *Cluster) *cand {
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
	b.oreShare = 95
	for rr := 0; rr <= b.MaxR; rr++ {
		if c.Kind == Mine && best == nil && rr == b.MaxR && b.oreShare > 70 {
			// целиком на руде не нашлось — хоть на 70%
			b.oreShare, rr = 70, 0
		}
		if best != nil && float64(rr) > best.d+1 {
			break
		}
		for _, horizontal := range []bool{true, false} {
			w, h := size(c, horizontal)
			cx, cy := int(math.Round(ax))-w/2, int(math.Round(ay))-h/2
			for _, o := range ringOffsets(rr) {
				try(Rect{world.Tile{X: cx + o.X, Y: cy + o.Y}, world.Tile{X: cx + o.X + w, Y: cy + o.Y + h}}, !horizontal)
			}
		}
	}
	return best
}

// fits — r годится кластеру c.
func (b *Base) fits(c *Cluster, r Rect, vertical bool) bool {
	g := b.g
	ring := r.Grow(Corridor)
	if n, out := g.count(g.ring, ring); n > 0 || out > 0 {
		return false
	}
	if n, _ := g.count(g.unknown, ring); n > 0 {
		return false
	}
	area := r.W() * r.H()
	switch c.Kind {
	case Mine:
		// на руде своего ресурса: не меньше 70% площади (буры ставятся, где есть руда)
		ore := g.ore[c.Product]
		if ore == nil {
			return false
		}
		// постройки на месторождении (точки нулевого этапа) — буры встанут рядом; свободной
		// должна быть лента ряда (v = 3) и хоть две трети площади
		n, _ := g.count(ore, r)
		a, _ := g.count(g.solid, r)
		// лента ряда и выход из него в коридор (2 тайла перед началом)
		belt := Rect{world.Tile{X: r.Min.X - 2, Y: r.Min.Y + 3}, world.Tile{X: r.Max.X, Y: r.Min.Y + 4}}
		if vertical {
			belt = Rect{world.Tile{X: r.Min.X + 3, Y: r.Min.Y - 2}, world.Tile{X: r.Min.X + 4, Y: r.Max.Y}}
		}
		bs, _ := g.count(g.solid, belt)
		return bs == 0 && a*3 <= area && n*100 >= area*b.oreShare
	case Power:
		// станция у воды: вода в коридоре
		if n, _ := g.count(g.water, ring); n == 0 {
			return false
		}
	}
	n, _ := g.count(g.bad, r)
	return n == 0
}

// anchor — куда тянуть кластер: добыча — к центру базы; лаборатории — к кластерам науки;
// прочие (и станция — к углю: лента дороже столбов) — к центру своих поставщиков (по
// потоку), без них — к центру базы.
func (b *Base) anchor(c *Cluster) (float64, float64) {
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
	if c.Kind == Mine || sw == 0 {
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

// PlaceAll — имена (<продукт>-N) и места всем кластерам по порядку.
func (b *Base) PlaceAll(cs []*Cluster) error {
	// по зависимостям: добыча → плавка → сборка → лаборатории → станция (к потребителям)
	order := map[Kind]int{Mine: 0, Smelt: 1, Make: 2, Lab: 3, Supply: 4, Power: 5, Storage: 6}
	cs = append([]*Cluster(nil), cs...)
	sort.SliceStable(cs, func(i, j int) bool { return order[cs[i].Kind] < order[cs[j].Kind] })
	// клиенты шины — на шину (после плавки: шина — у неё)
	var clients []*Cluster
	for _, c := range cs {
		if c.Name == "" {
			c.Name = b.name(c.Product)
		}
		if OnBus(c) {
			clients = append(clients, c)
			continue
		}
		if err := b.Place(c); err != nil {
			return err
		}
	}
	if len(clients) > 0 {
		return b.placeOnBus(clients)
	}
	return nil
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
func (b *Base) name(product string) string {
	if s, ok := short[product]; ok {
		product = s
	}
	used := map[string]bool{}
	for _, c := range b.Clusters {
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

// Reset — пересчитать разметку (после правки Avoid или списка кластеров).
func (b *Base) Reset() { b.g = nil }

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
	for _, c := range b.Clusters {
		if c.Kind == Bus {
			b.reserveBus(l, c) // свободны голова, зазоры, полоса — для подводов
			continue
		}
		area(c, true)
	}
	for _, c := range fresh {
		if c.Kind == Bus {
			continue // ленты шины — в Connect (по потребителям)
		}
		area(c, false)
		if err := Interior(l, c, Tag(c.Name)); err != nil {
			return l, err
		}
		area(c, true)
	}
	return l, nil
}
