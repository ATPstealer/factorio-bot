package cluster

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Кластеры внутри зон (zones.md, стадия 5): место кластера — в зоне его вида, шины — ленты в
// зонах шин (bus-1 ресурсы … bus-4 конечные), всё течёт вправо (+X): головы лент — у левого
// края шины. Зона берёт с шины под собой (снабжение — и над собой), отдаёт в шину над собой;
// что нужно выше, чем лежит, — сквозной лентой на следующие шины; уголь на станцию — отводом
// прямо с ленты добычи угля, мимо шин.

// ZBus — шина в зоне: прямоугольник и ленты (предмет, ряд Y).
type ZBus struct {
	Name  string     `json:"name"`
	Rect  Rect       `json:"rect"`
	Lanes []ZoneLane `json:"lanes"`
}

// ZoneLane — лента шины: предмет, ряд (Y), есть ли поставщик в голове.
type ZoneLane struct {
	Item  string `json:"item"`
	Y     int    `json:"y"`
	Fed   bool   `json:"fed,omitempty"`
	From  int    `json:"from,omitempty"` // X самого раннего подвода: отводы — только ниже по течению
	Start int    `json:"start"`          // X начала ленты (голова; у неровности у головы — за ней)
	Taps  int    `json:"taps,omitempty"` // отводов с ленты: из двух лент предмета — с менее занятой
}

// feed — подвод в ленту у x: запомнить самый ранний.
func (ln *ZoneLane) feed(x int) {
	if !ln.Fed || x < ln.From {
		ln.From = x
	}
	ln.Fed = true
}

func (zb *ZBus) lane(item string) *ZoneLane {
	for i := range zb.Lanes {
		if zb.Lanes[i].Item == item {
			return &zb.Lanes[i]
		}
	}
	return nil
}

// lanes — все ленты предмета (добытое в bus-1 — двумя лентами, cluster.md §2а «Добыча»).
func (zb *ZBus) lanes(item string) []*ZoneLane {
	var res []*ZoneLane
	for i := range zb.Lanes {
		if zb.Lanes[i].Item == item {
			res = append(res, &zb.Lanes[i])
		}
	}
	return res
}

// head — вход в голову ленты: только слева (там вход в шину).
func (ln *ZoneLane) head() Port {
	return Port{T: world.Tile{X: ln.Start - 1, Y: ln.Y}, Dir: layout.E, Item: ln.Item, In: true}
}

// laneCount — лент предмета it на шине k: добытое в bus-1 — по ленте на выход блока добычи (их
// два), остальное — одна.
func (b *Base) laneCount(k int, it string) int {
	n := 1
	if k == 0 {
		for _, c := range b.Clusters {
			if c.Kind == Mine && c.Product == it {
				n = max(n, min(len(outPorts(c)), 2))
			}
		}
	}
	return n
}

// floorOfZone — номер этажа (1–4) зоны производства: Floor; в старых файлах — по виду и
// порядку (final-1 → 3, final-2 → 4).
func floorOfZone(z Zone) int {
	switch {
	case z.Floor > 0:
		return z.Floor
	case z.Kind == ZoneProcessing:
		return 1
	case z.Kind == ZoneSupply:
		return 2
	case z.Kind == ZoneLab, z.Kind == ZoneFinal && strings.HasSuffix(z.Name, "-2"):
		return 4
	case z.Kind == ZoneFinal:
		return 3
	}
	return 0
}

// PlaceZoned — места кластерам cs внутри зон zones: добыча — в зонах добычи своего ресурса
// (ближе к шине ресурсов), производство — в зоне своего этажа, станция — в зоне энергии,
// хранение — вне зон, у стартовых буров (StorageNear). Уже размеченные кластеры базы
// (b.Clusters) — препятствие. Возвращает шины (ленты — потом, ConnectZoned).
func (b *Base) PlaceZoned(cs []*Cluster, zones []Zone) ([]*ZBus, error) {
	b.grid()
	var buses []*ZBus
	floor := map[int][]Rect{} // части этажа слева направо
	var mines []Zone
	var power *Rect
	base := Rect{world.Tile{X: math.MaxInt, Y: math.MaxInt}, world.Tile{X: math.MinInt, Y: math.MinInt}}
	for _, z := range zones {
		switch z.Kind {
		case ZoneBus:
			buses = append(buses, &ZBus{Name: z.Name, Rect: z.Rect})
			base = union(base, z.Rect)
			b.busRects = append(b.busRects, z.Rect)
		case ZoneMine:
			mines = append(mines, z)
		case ZonePower:
			r := z.Rect
			power = &r
		default:
			f := floorOfZone(z)
			floor[f] = append(floor[f], z.Rect)
		}
		b.zoneRects = append(b.zoneRects, z.Rect)
	}
	sort.Slice(buses, func(i, j int) bool { return buses[i].Name < buses[j].Name })
	for _, ps := range floor {
		sort.Slice(ps, func(i, j int) bool { return ps[i].Min.X < ps[j].Min.X })
	}
	if len(buses) == 0 {
		return nil, fmt.Errorf("cluster: в зонах нет шин")
	}
	head := world.Tile{X: buses[0].Rect.Min.X, Y: buses[0].Rect.Min.Y}
	for _, c := range cs {
		if c.Name == "" {
			c.Name = b.name(c.Product, cs)
		}
	}
	// добыча: зона своего ресурса, ближняя к голове шины ресурсов
	sort.SliceStable(mines, func(i, j int) bool {
		xi, yi := mines[i].Rect.Center()
		xj, yj := mines[j].Rect.Center()
		return math.Hypot(xi-float64(head.X), yi-float64(head.Y)) < math.Hypot(xj-float64(head.X), yj-float64(head.Y))
	})
	var mineCs []*Cluster
	for _, c := range cs {
		if c.Kind == Mine {
			mineCs = append(mineCs, c)
		}
	}
	// блоки на пятна (cluster.md §2а «Добыча»): по объёму руды, соседи делят место поровну
	if err := b.placeMines(mineCs, mines, head); err != nil {
		return nil, err
	}
	// этажи снизу вверх: поставщики входов стоят раньше потребителей
	order := slices.Clone(cs)
	sort.SliceStable(order, func(i, j int) bool { return floorOf(order[i]) < floorOf(order[j]) })
	for _, c := range order {
		f := floorOf(c)
		switch {
		case c.Kind == Mine:
			continue
		case c.Kind == Power:
			if power == nil {
				return nil, fmt.Errorf("cluster: нет зоны энергии")
			}
			r := power.Grow(4)
			c.Zone = &r
			// ряд — на всю зону станции (место под все бойлеры), строятся Count (cluster.md §2а
			// «Станция»)
			c.Room = max(c.Count, (max(power.W(), power.H())-c.Head)/c.Pitch)
		case f > 0:
			// не левее поставщиков своих входов с этажей ниже (и своего — сборка берёт и с шины
			// над собой): продукт кладётся в ленту шины у столбца поставщика, отвод — только ниже
			// по течению (zones.md)
			for _, in := range c.In {
				first := math.MaxInt // самый левый поставщик предмета
				for _, p := range b.Clusters {
					if f2 := floorOf(p); f2 > 0 && (f2 < f || f2 == f && c.TakesAbove()) && slices.ContainsFunc(p.Out, func(o Flow) bool { return o.Item == in.Item }) {
						first = min(first, p.Area.Min.X)
					}
				}
				if first != math.MaxInt && (c.MinX == nil || first > *c.MinX) {
					c.MinX = &first
				}
			}
			// части этажа слева направо — первая, где есть место; ряд — поперёк шин
			var err error = fmt.Errorf("cluster: нет зоны этажа %d под %s", f, c.Name)
			parts := slices.Clone(floor[f])
			if c.Kind == Lab { // лаборатории — с правой части этажа
				slices.Reverse(parts)
			}
			for _, r := range parts {
				c.Zone, c.Upright = &r, true
				if err = b.Place(c); err == nil {
					break
				}
			}
			if err != nil {
				return nil, err
			}
			if c.Kind == Lab { // пакеты — с шины над этажом: голова блока (входы, буферы) — сверху,
				// ленты A и B идут вниз вдоль левого столбца (решение пользователя)
				c.Upright, c.Rect = false, c.Area
			}
			continue
		default: // хранение — вне зон, у стартовых буров (или где задано: у головы bus-1)
			if c.Near == nil {
				cx, cy := base.Center()
				near := world.Tile{X: int(cx), Y: int(cy)}
				if b.StorageNear != nil {
					near = *b.StorageNear
				}
				c.Near = &near
			}
		}
		err := b.Place(c)
		// станция: ряд на всю зону не влез (зона у воды вплотную к соседям, 264771462) —
		// короче, вплоть до бойлеров, нужных сейчас (cluster.md §2а «Станция»: строится Count)
		for c.Kind == Power && err != nil && c.Room > c.Count {
			c.Room = max(c.Count, c.Room*2/3)
			err = b.Place(c)
		}
		if c.Kind == Power && err != nil && power != nil {
			// и так не встала (в зону залез коридор соседней добычи) — другое место у воды
			// рядом с зоной, вне зон (cluster.md §2а «Станция»)
			x, y := power.Center()
			near := world.Tile{X: int(x), Y: int(y)}
			c.Zone, c.Near = nil, &near
			err = b.Place(c)
		}
		if err != nil {
			return nil, err
		}
	}
	return buses, nil
}

// laneHead — лента шины не короче (от начала зоны шины): подводы добычи, сквозные отводы;
// laneTail — за последним пользователем ленты (отвод делителем, подвод боком).
const (
	laneHead = 41
	laneTail = 20
)

// zoneSources — где что лежит: шина 0 — добыча; шина f — выходы этажа f.
func (b *Base) zoneSources(nb int) map[string]int {
	on := map[string]int{}
	for _, c := range b.Clusters {
		if c.Kind == Mine {
			on[c.Product] = 0
		}
	}
	for _, c := range b.Clusters {
		if f := floorOf(c); f > 0 && f < nb {
			for _, o := range c.Out {
				if _, set := on[o.Item]; !set {
					on[o.Item] = f
				}
			}
		}
	}
	return on
}

// zoneNeed — ленты шин: предмет — на своей шине и сквозной на всех до шины под потребителем;
// сборке, которая берёт с шины над собой предмет своего этажа, — на шине над этажом.
func (b *Base) zoneNeed(on map[string]int, nb int) []map[string]bool {
	need := make([]map[string]bool, nb)
	for i := range need {
		need[i] = map[string]bool{}
	}
	for _, c := range b.Clusters {
		f := floorOf(c)
		if f == 0 {
			continue
		}
		for _, in := range c.In {
			src, ok := on[in.Item]
			if !ok {
				continue
			}
			if _, fluid := b.P.Fluids[in.Item]; fluid {
				continue
			}
			for k := src; k <= f-1 && k < nb; k++ {
				need[k][in.Item] = true
			}
			if src == f && c.TakesAbove() && f < nb {
				need[f][in.Item] = true
			}
		}
	}
	return need
}

// laneEnd — конец ленты it шины k — по необходимости (zones.md): за последним, кто с неё берёт
// (этаж над шиной; снабжение — и этаж под ней) или в неё кладёт (этаж под шиной), с запасом под
// отвод; не короче головы (подводы добычи, сквозные отводы).
func (b *Base) laneEnd(buses []*ZBus, k int, it string) int {
	bus := buses[k]
	end := bus.Rect.Min.X + laneHead
	for _, c := range b.Clusters {
		f, use := floorOf(c), false
		for _, in := range c.In {
			use = use || in.Item == it && (f == k+1 || f == k && c.TakesAbove())
		}
		for _, o := range c.Out {
			use = use || o.Item == it && f == k
		}
		if use {
			end = max(end, c.Area.Max.X+laneTail)
		}
	}
	return min(end, bus.Rect.Max.X)
}

// laneGap — разрыв ленты шины: от a (последняя лента до препятствия) до b (первая после).
type laneGap struct {
	a, b      world.Tile
	item, tag string
	bus, lane int
}

// layLane — ленты ряда j шины k на x0..end-1 (вода, утёс — разрыв, обход потом: fixGaps);
// started — лента уже есть до x0 (конец — в last), иначе голова — с первого тайла суши.
func (b *Base) layLane(l *layout.Layout, buses []*ZBus, k, j, x0, end, last int, started bool, gaps *[]laneGap) {
	bus := buses[k]
	ln := &bus.Lanes[j]
	for x := x0; x < end; x++ {
		t := world.Tile{X: x, Y: ln.Y}
		if ok, _ := b.W.Buildable(t); !ok || l.At(t) != nil {
			continue // вода, утёс, чужая лента поперёк — обход потом (gaps)
		}
		if !started {
			ln.Start, started = x, true // голова — в неровности: лента — с первого тайла
		} else if x > last+1 {
			*gaps = append(*gaps, laneGap{world.Tile{X: last, Y: t.Y}, t, ln.Item, Tag(bus.Name), k, j})
		}
		l.Add(layout.Placement{Name: "transport-belt", Pos: t.Center(), Dir: layout.E, Recipe: ln.Item, Tag: Tag(bus.Name)})
		last = x
	}
	l.AllowEnd(world.Tile{X: last + 1, Y: ln.Y})
}

// fixGaps — неровности шины (вода, утёсы — вдоль ряда не длиннее busGap, zones.md): ленты ряда — в
// обход, когда все ряды уже лежат (обход одной не займёт ряд другой).
func fixGaps(l *layout.Layout, buses []*ZBus, gaps []laneGap) error {
	for _, gp := range gaps {
		// обход — до первой ленты после препятствия, а не вышел — на 1–3 дальше (выход подземки
		// не может стоять на самой цели: узкую помеху вплотную к ней не перепрыгнуть)
		// неровность не длиннее подземки — пара подземок прямо в ряду (обход маршрутом в тесноте
		// у отводов не находился — 238178543, стадия 7)
		if ia, oka := l.Index(gp.a); oka && gp.b.X-gp.a.X <= l.P.Entities["underground-belt"].MaxUndergroundDistance {
			if ib, okb := l.Index(gp.b); okb && l.List[ia].Name == "transport-belt" && l.List[ib].Name == "transport-belt" &&
				l.List[ia].Dir == layout.E && l.List[ib].Dir == layout.E && !ugBetween(l, gp.a, gp.b) {
				l.Replace(ia, layout.Placement{Name: "underground-belt", Pos: gp.a.Center(), Dir: layout.E, Recipe: "input", Tag: gp.tag})
				l.Replace(ib, layout.Placement{Name: "underground-belt", Pos: gp.b.Center(), Dir: layout.E, Recipe: "output", Tag: gp.tag})
				continue
			}
		}
		var err error
		mark := l.Mark()
		for extra := 0; extra <= 3; extra++ {
			b := world.Tile{X: gp.b.X + extra, Y: gp.b.Y}
			if p := l.At(b); extra > 0 && (p == nil || p.Tag != gp.tag || p.Recipe != gp.item) {
				break // дальше не наша лента ряда
			}
			l.List = slices.DeleteFunc(l.List, func(p layout.Placement) bool {
				t := world.TileOf(p.Pos)
				return p.Tag == gp.tag && (t == gp.a || t.Y == b.Y && t.X >= gp.b.X && t.X <= b.X && p.Recipe == gp.item)
			})
			l.Rollback(-1) // пересчитать занятость
			mark = l.Mark()
			// въезд в a — откуда на самом деле (прошлый обход кончился въездом снизу: подземка в
			// начале нового легла боком к ленте — пакеты вставали, стадия 23 на 851731946)
			if _, err = l.RouteBelt(gp.a, feedDir(l, gp.a), b, layout.E, gp.tag); err == nil {
				break
			}
			// вернуть ленты ряда и попробовать дальше
			l.Add(layout.Placement{Name: "transport-belt", Pos: gp.a.Center(), Dir: layout.E, Recipe: gp.item, Tag: gp.tag})
			for x := gp.b.X; x <= b.X; x++ {
				l.Add(layout.Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: b.Y}.Center(), Dir: layout.E, Recipe: gp.item, Tag: gp.tag})
			}
		}
		ln := &buses[gp.bus].Lanes[gp.lane]
		if err != nil && gp.a.X < ln.Start+laneHead {
			l.List = slices.DeleteFunc(l.List, func(p layout.Placement) bool {
				t := world.TileOf(p.Pos)
				return p.Tag == gp.tag && (t == gp.a || t == gp.b)
			})
			l.Rollback(-1)
			// у головы обход не провёлся — лента начинается за неровностью (подводы в голову — туда)
			l.List = slices.DeleteFunc(l.List, func(p layout.Placement) bool {
				t := world.TileOf(p.Pos)
				return p.Tag == gp.tag && t.Y == gp.a.Y && t.X <= gp.a.X && p.Recipe == gp.item
			})
			l.Rollback(-1)
			l.Add(layout.Placement{Name: "transport-belt", Pos: gp.b.Center(), Dir: layout.E, Recipe: gp.item, Tag: gp.tag})
			ln.Start = gp.b.X
			continue
		} else if err != nil {
			in := map[string]bool{} // что в разрыве (разбор)
			for x := gp.a.X + 1; x < gp.b.X; x++ {
				t := world.Tile{X: x, Y: gp.a.Y}
				if p := l.At(t); p != nil {
					in[p.Name+" "+p.Tag] = true
				} else if ok, _ := l.W.Buildable(t); !ok {
					in["неровность"] = true
				}
			}
			return fmt.Errorf("cluster: %s: лента %s не обходит неровность (%d, %d)–(%d, %d) %v: %w", gp.tag, gp.item, gp.a.X, gp.a.Y, gp.b.X, gp.b.Y, slices.Collect(maps.Keys(in)), err)
		}
		// предмет ленты — обычным лентам; у подземки в Recipe — вход/выход: затёртый — следующий
		// обход той же ленты не видел, откуда в него въезжают (feedDir), и ставил вход подземки
		// боком к потоку — лента стояла (лента инсертеров bus-3, стадия 26 на 859088572)
		for i := mark; i < len(l.List); i++ {
			if l.List[i].Name == "transport-belt" {
				l.List[i].Recipe = gp.item
			}
		}
	}
	return nil
}

// feedDir — куда едет лента, въезжающая в t (лента или выход подземки, смотрящие в t); нет —
// восток (вдоль шины).
func feedDir(l *layout.Layout, t world.Tile) int {
	for _, d := range []int{layout.E, layout.N, layout.S, layout.W} {
		p := l.At(layout.Step(t, (d+8)%16))
		if p != nil && p.Dir == d && (p.Name == "transport-belt" || p.Name == "underground-belt" && p.Recipe == "output") {
			return d
		}
	}
	return layout.E
}

// zLaneBelts — ленты ряда ln шины bus на x0..x1-1.
func zLaneBelts(l *layout.Layout, bus *ZBus, ln *ZoneLane, x0, x1 int) []layout.Placement {
	var res []layout.Placement
	for x := max(x0, bus.Rect.Min.X); x < min(x1, bus.Rect.Max.X); x++ {
		if p := l.At(world.Tile{X: x, Y: ln.Y}); p != nil && p.Name == "transport-belt" {
			res = append(res, *p)
		}
	}
	return res
}

// zSidePorts — подвод боком в ленту ln шины bus у столбцов x0..x1-1 (зазор над или под лентой).
func zSidePorts(l *layout.Layout, bus *ZBus, ln *ZoneLane, x0, x1 int) []Port {
	var res []Port
	for x := max(x0, bus.Rect.Min.X+1, ln.Start+1); x < min(x1, bus.Rect.Max.X-1); x++ {
		for _, s := range []struct{ dy, d int }{{-1, layout.S}, {1, layout.N}} {
			t := world.Tile{X: x, Y: ln.Y + s.dy}
			if l.Free(t, layout.AnyLand) {
				res = append(res, Port{T: t, Dir: s.d, Item: ln.Item, In: true})
			}
		}
	}
	return res
}

// zRoute — лента из from в первый из to, куда проводится; возвращает его.
func zRoute(l *layout.Layout, from Port, to []Port, tag string) (Port, error) {
	var err error = layout.ErrNoRoute
	for _, p := range to {
		if _, err = l.RouteBelt(from.T, from.Dir, p.T, p.Dir, tag); err == nil {
			return p, nil
		}
	}
	return Port{}, err
}

func outPort(c *Cluster) (Port, bool) {
	for _, p := range c.Ports {
		if !p.In {
			return p, true
		}
	}
	return Port{}, false
}

// portGuard — порты кластеров этажей закрыты для чужих лент до подключения своего (сквозная
// лента меди легла через вход шестерён); open — открыть порт перед его подключением.
type portGuard struct {
	l     *layout.Layout
	ports map[world.Tile]bool
}

func guardPorts(l *layout.Layout, cs []*Cluster) *portGuard {
	g := &portGuard{l, map[world.Tile]bool{}}
	for _, c := range cs {
		if floorOf(c) == 0 {
			continue
		}
		for _, p := range c.Ports {
			g.close(p)
		}
	}
	return g
}

func (g *portGuard) close(p Port) {
	if !g.l.Reserved[p.T] && g.l.At(p.T) == nil {
		g.l.Reserved[p.T], g.ports[p.T] = true, true
	}
}

func (g *portGuard) open(p Port) {
	if g.ports[p.T] {
		delete(g.l.Reserved, p.T)
		delete(g.ports, p.T)
	}
}

// feedOut — выход кластера этажа — боком в ленту шины над собой, у своего столбца (к голове —
// нельзя: лента ляжет вдоль зазора между лентами шины и закроет его другим).
func (b *Base) feedOut(l *layout.Layout, buses []*ZBus, c *Cluster, g *portGuard) error {
	f := floorOf(c)
	op, ok := outPort(c)
	if f == 0 || f >= len(buses) || !ok {
		return nil
	}
	bus := buses[f]
	ln := bus.lane(op.Item)
	if ln == nil {
		return nil // продукт выше не нужен (буфер выхода)
	}
	x0, x1 := c.Area.Min.X-20, max(c.Area.Max.X+20, ln.Start+40) // лента начинается за неровностью — ближайшие за ней
	g.open(op)
	tag := LogisticsTag + "/" + bus.Name + "/" + c.Name
	// в ленту — делителем (cluster.md §5: боком всё ложилось на одну полосу, делитель полосы
	// сохраняет); не встал — боком
	if t, err := SplitJoin(l, zLaneBelts(l, bus, ln, x0, x1), op, tag, 40); err == nil {
		ln.feed(t.X)
		return nil
	}
	to := zSidePorts(l, bus, ln, x0, x1)
	sort.SliceStable(to, func(i, j int) bool {
		return math.Abs(float64(to[i].T.X-op.T.X)) < math.Abs(float64(to[j].T.X-op.T.X))
	})
	if len(to) > 40 {
		to = to[:40]
	}
	p, err := zRoute(l, op, to, tag)
	if err != nil {
		return &ConnectError{c, fmt.Errorf("%s → %s (%s): %w", c.Name, bus.Name, op.Item, err)}
	}
	ln.feed(p.T.X)
	return nil
}

// feedThrough — сквозные ленты: голова ленты шины k без поставщика — отводом с той же ленты
// шины k-1.
// ThroughError — сквозную ленту item на шину Bus не провести.
type ThroughError struct {
	Item string
	Bus  int
	Err  error
}

func (e *ThroughError) Error() string { return e.Err.Error() }
func (e *ThroughError) Unwrap() error { return e.Err }

func feedThrough(l *layout.Layout, buses []*ZBus) error {
	for k := 1; k < len(buses); k++ {
		for i := range buses[k].Lanes {
			ln := &buses[k].Lanes[i]
			if ln.Fed {
				continue
			}
			low := buses[k-1].lane(ln.Item)
			if low == nil {
				continue
			}
			// отвод — ниже подвода в нижнюю ленту; в ленту шины k — в голову или боком до места
			// отвода (подвод в нижнюю бывает не у головы)
			line := zLaneBelts(l, buses[k-1], low, low.From+1, low.From+81)
			// боком — в любом месте ленты (у головы шины тесно от других сквозных и отводов: сера на
			// bus-3, 859088572), ближние к подводу внизу первыми
			side := zSidePorts(l, buses[k], ln, buses[k].Rect.Min.X, buses[k].Rect.Max.X)
			sort.SliceStable(side, func(i, j int) bool { return abs(side[i].T.X-low.From) < abs(side[j].T.X-low.From) })
			if len(side) > 30 {
				side = side[:30]
			}
			to := append([]Port{{T: world.Tile{X: ln.Start - 1, Y: ln.Y}, Dir: layout.E, Item: ln.Item, In: true}}, side...)
			err := layout.ErrNoRoute
			for _, p := range to {
				if err = SplitTap(l, line, p, LogisticsTag+"/"+buses[k].Name+"/"+buses[k-1].Name, 40); err == nil {
					ln.feed(p.T.X)
					break
				}
			}
			if err != nil {
				return &ThroughError{Item: ln.Item, Bus: k, Err: fmt.Errorf("сквозная %s → %s (%s): %w", buses[k-1].Name, buses[k].Name, ln.Item, err)}
			}
		}
	}
	return nil
}

// feedIn — вход p кластера c: делителем с ленты шины снизу (сборка — и сверху), у своего
// столбца, только ниже подвода в ленту.
func feedIn(l *layout.Layout, buses []*ZBus, c *Cluster, p Port, g *portGuard) error {
	f := floorOf(c)
	var lines [][]layout.Placement
	var used []*ZoneLane
	for _, k := range []int{f - 1, f} {
		if k >= len(buses) || (k == f && !c.TakesAbove()) || len(lines) > 0 {
			continue
		}
		// лент предмета две (добытое) — сначала с менее занятой; только с тех, куда есть
		// подвод (второй подвод не дошёл до головы и влился сбоку в первый — вторая лента
		// пустая: стадия 17 на 859088572, печи железа без угля)
		lns := slices.DeleteFunc(buses[k].lanes(p.Item), func(ln *ZoneLane) bool { return !ln.Fed })
		sort.SliceStable(lns, func(i, j int) bool { return lns[i].Taps < lns[j].Taps })
		for _, ln := range lns {
			// только ниже подвода: выше по течению предмета на ленте нет
			x0 := max(c.Area.Min.X-40, ln.From+1)
			if line := zLaneBelts(l, buses[k], ln, x0, max(c.Area.Max.X+40, x0+60)); len(line) > 0 {
				lines, used = append(lines, line), append(used, ln)
			}
		}
	}
	if len(lines) == 0 {
		return fmt.Errorf("cluster: %s ← %s: на шинах рядом нет ленты", c.Name, p.Item)
	}
	g.open(p)
	var err error
	for i, line := range lines {
		if err = SplitTap(l, line, p, LogisticsTag+"/"+c.Name, 40); err == nil {
			used[i].Taps++
			pairBefore(l, buses, used[i], LogisticsTag+"/"+c.Name)
			return nil
		}
	}
	return &ConnectError{c, fmt.Errorf("шина → %s (%s): %w", c.Name, p.Item, err)}
}

// pairBefore — отвод с ленты ln, у которой есть пара (соседний ряд с тем же предметом): делитель
// между лентами пары сразу перед отводом (cluster.md §5) — в отвод идёт с обеих. Делитель отвода
// — делитель с тегом tag на ряду ln (SplitTap ставит его на место ленты).
func pairBefore(l *layout.Layout, buses []*ZBus, ln *ZoneLane, tag string) {
	var bus *ZBus
	for _, b := range buses {
		for i := range b.Lanes {
			if &b.Lanes[i] == ln {
				bus = b
			}
		}
	}
	if bus == nil {
		return
	}
	other := -1
	for _, o := range bus.Lanes {
		if o.Item == ln.Item && (o.Y == ln.Y-1 || o.Y == ln.Y+1) {
			other = o.Y
		}
	}
	if other < 0 {
		return
	}
	for i := len(l.List) - 1; i >= 0; i-- {
		sp := l.List[i]
		if sp.Name != "splitter" || sp.Tag != tag {
			continue
		}
		var t world.Tile // тайл делителя на ряду ленты
		found := false
		for _, u := range layout.Tiles(l.P, sp) {
			if u.Y == ln.Y {
				t, found = u, true
			}
		}
		if !found {
			continue
		}
		step := -1 // против хода: перед отводом
		if sp.Dir == layout.W {
			step = 1
		}
		for k := 2; k <= 12; k++ {
			if balanceAt(l, ln.Item, min(ln.Y, other), max(ln.Y, other), t.X+k*step, sp.Dir) {
				return
			}
		}
		return
	}
}

// feedMine — выходы добычи c (у блока их два — две ленты в bus-1), кроме skip, — в свободные
// головы лент своего ресурса, только слева; голов нет — боком в подвод до головы (cluster.md §2а
// «Добыча»). Подводы — только по пустому месту (fence).
func (b *Base) feedMine(l *layout.Layout, buses []*ZBus, c *Cluster, skip *Port) error {
	bus := buses[0]
	lns := bus.lanes(c.Product)
	if len(lns) == 0 {
		return nil
	}
	defer unfence(l, b.fence(l, bus, c))
	defer unsolid(l, c)()
	for i, op := range outPorts(c) {
		if skip != nil && op.T == skip.T {
			continue
		}
		var p Port
		err := layout.ErrNoRoute
		for _, q := range b.feedTargets(l, bus, lns, op) {
			g := guardHeads(l, bus, q.T)
			_, err = l.RouteBelt(op.T, op.Dir, q.T, q.Dir, LogisticsTag+"/"+bus.Name+"/"+c.Name)
			unfence(l, g)
			if err == nil {
				p = q
				break
			}
		}
		if err != nil {
			var tried []string
			for _, q := range b.feedTargets(l, bus, lns, op) {
				tried = append(tried, fmt.Sprintf("(%d,%d)", q.T.X, q.T.Y))
			}
			b.logf("подвод %s выход %d из (%d,%d): не дошёл ни до одной из %v", c.Name, i+1, op.T.X, op.T.Y, tried)
			// разбор: где закрыто между выходом и головой
			if tg := b.feedTargets(l, bus, lns, op); len(tg) > 0 {
				h := tg[0].T
				var row []string
				for x := min(op.T.X, h.X) - 3; x <= max(op.T.X, h.X)+3; x++ {
					s := ""
					for y := min(op.T.Y, h.Y) - 3; y <= max(op.T.Y, h.Y)+3; y++ {
						t := world.Tile{X: x, Y: y}
						switch {
						case t == op.T:
							s += "O"
						case t == h:
							s += "H"
						case l.At(t) != nil:
							s += "#"
						case l.Solid[t]:
							s += "S"
						case l.Reserved[t]:
							s += "r"
						case !l.Free(t, layout.AnyLand):
							s += "~"
						default:
							s += "."
						}
					}
					row = append(row, fmt.Sprintf("x=%d %s", x, s))
				}
				b.logf("  карта (столбцы — X, по Y вниз):\n%s", strings.Join(row, "\n"))
			}
			return fmt.Errorf("добыча %s → %s (%s, выход %d): %w", c.Name, bus.Name, c.Product, i+1, err)
		}
		fedHead(lns, p)
	}
	return nil
}

// mineOut — выход блока добычи и его лента bus-1.
type mineOut struct {
	c    *Cluster
	p    Port
	lane int // Y ленты
}

// orderedMineLanes — ленты bus-1 под каждый выход каждого блока добычи (не нефть), ряды сверху
// вниз — по углу выхода вокруг головы шины (от востока против часовой: сверху-справа — верхний
// ряд, слева — середина, снизу — нижние): подводы не пересекаются и не закрывают головы. Выход →
// лента — в b.feedLane.
func (b *Base) orderedMineLanes(l *layout.Layout, buses []*ZBus) error {
	bus := buses[0]
	hx, hy := float64(bus.Rect.Min.X), float64(bus.Rect.Min.Y+bus.Rect.Max.Y)/2
	var outs []mineOut
	for _, c := range b.Clusters {
		if c.Kind != Mine || c.IsOil() {
			continue
		}
		for _, p := range outPorts(c) {
			outs = append(outs, mineOut{c: c, p: p})
		}
	}
	key := func(t world.Tile) float64 {
		a := math.Atan2(-(float64(t.Y) - hy), float64(t.X)-hx)
		if a < 0 {
			a += 2 * math.Pi
		}
		return a
	}
	sort.SliceStable(outs, func(i, j int) bool { return key(outs[i].p.T) < key(outs[j].p.T) })
	// ряд — ближе к высоте выхода (пришедший снизу — в нижний), порядок сверху вниз — как у углов,
	// и место под остальных
	rows := 0
	for laneV(rows) < bus.Rect.H() {
		rows++
	}
	if len(outs) > rows {
		return fmt.Errorf("cluster: %s: выходов добычи %d, рядов %d", bus.Name, len(outs), rows)
	}
	want := make([]int, len(outs))
	for i, o := range outs {
		v, best := 0, math.MaxInt
		for r := range rows {
			if d := abs(bus.Rect.Min.Y + laneV(r) - o.p.T.Y); d < best {
				v, best = r, d
			}
		}
		want[i] = v
		if i > 0 {
			want[i] = max(want[i], want[i-1]+1)
		}
	}
	for i := len(outs) - 1; i >= 0; i-- { // хвост не за последним рядом
		want[i] = min(want[i], rows-(len(outs)-i))
		if i < len(outs)-1 {
			want[i] = min(want[i], want[i+1]-1)
		}
	}
	b.feedLane = map[world.Tile]int{}
	for i, o := range outs {
		// до последнего потребителя (не до конца шины — решение пользователя)
		y, _, err := b.newLaneFrom(l, buses, 0, o.c.Product, b.laneEnd(buses, 0, o.c.Product), want[i])
		if err != nil { // ниже по порядку рядов не осталось — любой свободный
			y, _, err = b.newLaneFrom(l, buses, 0, o.c.Product, b.laneEnd(buses, 0, o.c.Product), 0)
		}
		if err != nil {
			return err
		}
		b.feedLane[o.p.T] = y
	}
	return nil
}

// LaterTag — тег построек, дописанных в стоящий кластер на потом (буры до 2M в блоке первой
// итерации): строятся вместе с будущей добычей.
func LaterTag(name string) string { return Tag(name) + "/later" }

// transfers — руда будущих блоков (Later) питает и старые цепочки (решение пользователя): на
// новой ленте — делитель, его второй выход — подземкой поперёк шины к ближней старой ленте того
// же предмета, там — делитель (вливает в неё).
func (b *Base) transfers(l *layout.Layout, bus *ZBus) {
	byY := map[int]*ZoneLane{}
	for i := range bus.Lanes {
		byY[bus.Lanes[i].Y] = &bus.Lanes[i]
	}
	later := map[int]*Cluster{} // лента → будущий блок
	old := map[string][]int{}   // предмет → ленты старых блоков
	for _, c := range b.Clusters {
		for _, p := range outPorts(c) {
			y, ok := b.feedLane[p.T]
			if !ok {
				continue
			}
			if c.Later {
				later[y] = c
			} else {
				old[c.Product] = append(old[c.Product], y)
			}
		}
	}
	for _, y := range slices.Sorted(maps.Keys(later)) {
		c, nl := later[y], byY[y]
		if nl == nil || len(old[c.Product]) == 0 {
			continue
		}
		oy := old[c.Product][0]
		for _, o := range old[c.Product] {
			if abs(o-y) < abs(oy-y) {
				oy = o
			}
		}
		ol := byY[oy]
		if ol == nil {
			continue
		}
		tag := LogisticsTag + "/" + bus.Name + "/" + c.Name
		x0 := max(nl.From, ol.From, nl.Start, ol.Start) + 3
		if err := transferAt(l, nl, ol, x0, tag); err != nil {
			b.logf("перекидка %s → лента %d: %v", c.Name, oy, err)
		}
	}
}

// transferAt — перекидка с ленты from на ленту to (лента шины течёт на восток): делитель на from
// у столбца x (с ряда соседнего в сторону to), его второй выход — лентой (поперёк — подземками)
// во второй вход делителя на to. Ищет столбец от x0 вправо.
func transferAt(l *layout.Layout, from, to *ZoneLane, x0 int, tag string) error {
	s := 1
	if to.Y < from.Y {
		s = -1
	}
	straight := func(x, y int) bool { // прямая лента предмета, без подач сбоку
		for _, dx := range []int{-1, 0, 1} {
			q := l.At(world.Tile{X: x + dx, Y: y})
			if q == nil || q.Name != "transport-belt" || q.Dir != layout.E {
				return false
			}
		}
		for _, dy := range []int{-1, 1} {
			if q := l.At(world.Tile{X: x, Y: y + dy}); q != nil && (q.Name == "transport-belt" || q.Name == "underground-belt") && (q.Dir == layout.N || q.Dir == layout.S) {
				return false
			}
		}
		return true
	}
	splitter := func(x, y, side int) layout.Placement {
		a, b := world.Tile{X: x, Y: y}.Center(), world.Tile{X: x, Y: y + side}.Center()
		return layout.Placement{Name: "splitter", Pos: bridge.Pos{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}, Dir: layout.E, Tag: tag}
	}
	// делители — со стороны, где рядом не лента пары (сначала к другой ленте, потом наружу)
	for _, sa := range []int{s, -s} {
		for _, sb := range []int{-s, s} {
			for x := x0; x < x0+80; x++ {
				if !straight(x, from.Y) || !l.Free(world.Tile{X: x, Y: from.Y + sa}, layout.AnyLand) ||
					!l.Free(world.Tile{X: x + 1, Y: from.Y + sa}, layout.AnyLand) {
					continue
				}
				for xb := x + 3; xb < x+16; xb++ {
					if !straight(xb, to.Y) || !l.Free(world.Tile{X: xb, Y: to.Y + sb}, layout.AnyLand) ||
						!l.Free(world.Tile{X: xb - 1, Y: to.Y + sb}, layout.AnyLand) {
						continue
					}
					mark := l.Mark()
					ia, _ := l.Index(world.Tile{X: x, Y: from.Y})
					savedA := l.List[ia]
					l.Replace(ia, splitter(x, from.Y, sa))
					ib, _ := l.Index(world.Tile{X: xb, Y: to.Y})
					saved := l.List[ib]
					l.Replace(ib, splitter(xb, to.Y, sb))
					_, err := l.RouteBelt(world.Tile{X: x + 1, Y: from.Y + sa}, layout.E, world.Tile{X: xb - 1, Y: to.Y + sb}, layout.E, tag)
					if err == nil {
						return nil
					}
					// назад: делители — снова лентами
					l.List = l.List[:mark]
					l.Replace(ib, saved)
					l.Replace(ia, savedA)
				}
			}
		}
	}
	return fmt.Errorf("нет места под перекидку")
}

// feedTargets — куда вести выход op: сначала голова его ленты (orderedMineLanes), потом —
// как раньше (свободные головы, боком в подводы).
func (b *Base) feedTargets(l *layout.Layout, bus *ZBus, lns []*ZoneLane, op Port) []Port {
	res := feedPorts(l, bus, lns, op.T)
	y, ok := b.feedLane[op.T]
	if !ok {
		return res
	}
	for _, ln := range lns {
		if ln.Y == y && !ln.Fed {
			h := ln.head()
			return append([]Port{h}, slices.DeleteFunc(res, func(p Port) bool { return p.T == h.T })...)
		}
	}
	return res
}

// headRun — подход к голове ленты шины ресурсов: столько тайлов слева от неё — по ряду ленты.
const headRun = 4

// guardHeads — подходы к головам лент шины (headRun тайлов слева от головы) закрыты, кроме
// головы at: подвод в дальнюю ленту не ляжет вдоль столбца голов и не закроет соседние.
// Возвращает закрытое — открыть (unfence).
func guardHeads(l *layout.Layout, bus *ZBus, at world.Tile) []world.Tile {
	var res []world.Tile
	for _, ln := range bus.Lanes {
		h := ln.head().T
		if h == at {
			continue
		}
		for j := range headRun {
			if t := (world.Tile{X: h.X - j, Y: h.Y}); l.At(t) == nil && !l.Reserved[t] {
				l.Reserved[t] = true
				res = append(res, t)
			}
		}
	}
	return res
}

// fedHead — подвод пришёл в p: голова ленты — запомнить подвод.
func fedHead(lns []*ZoneLane, p Port) {
	for _, ln := range lns {
		if ln.head().T == p.T {
			ln.feed(p.T.X)
		}
	}
}

// feedPorts — куда подвести ресурс в шину ресурсов: свободные головы лент lns (ближние к from
// первыми), потом — боком в подводы до головы (вне шины).
func feedPorts(l *layout.Layout, bus *ZBus, lns []*ZoneLane, from world.Tile) []Port {
	var to []Port
	for _, ln := range lns {
		if !ln.Fed {
			to = append(to, ln.head())
		}
	}
	sort.SliceStable(to, func(i, j int) bool { return dist(to[i].T, from) < dist(to[j].T, from) })
	return append(to, feederSides(l, bus, lns, from)...)
}

// unsolid — своя добыча c на время её подводов — не сплошная (под своим блоком подземкой можно,
// под чужим — нет); возвращает, как было.
func unsolid(l *layout.Layout, c *Cluster) func() {
	var opened []world.Tile
	for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
		for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
			if t := (world.Tile{X: x, Y: y}); l.Solid[t] {
				delete(l.Solid, t)
				opened = append(opened, t)
			}
		}
	}
	return func() {
		for _, t := range opened {
			l.Solid[t] = true
		}
	}
}

// feederSides — входы боком в подводы лент lns: прямые ленты логистики шины вне неё, которые
// ведут в голову; ближние к from первыми, не больше 40.
func feederSides(l *layout.Layout, bus *ZBus, lns []*ZoneLane, from world.Tile) []Port {
	prefix := LogisticsTag + "/" + bus.Name + "/"
	lines := map[world.Tile]layout.Placement{}
	for _, p := range l.List {
		if strings.HasPrefix(p.Tag, prefix) && (p.Name == "transport-belt" || p.Name == "underground-belt") {
			lines[world.TileOf(p.Pos)] = p
		}
	}
	heads := map[world.Tile]bool{}
	for _, ln := range lns {
		if ln.Fed {
			heads[ln.head().T] = true
		}
	}
	in := func(t world.Tile) bool {
		r := bus.Rect
		return t.X >= r.Min.X && t.X < r.Max.X && t.Y >= r.Min.Y && t.Y < r.Max.Y
	}
	var res []Port
	for t, p := range lines {
		if p.Name != "transport-belt" || in(t) {
			continue
		}
		if q, ok := lines[layout.Step(t, (p.Dir+8)%16)]; !ok || q.Dir != p.Dir {
			continue // поворот: сбоку в него не войти
		}
		path := follow(lines, t)
		if len(path) == 0 || !heads[world.TileOf(path[len(path)-1].Pos)] {
			continue
		}
		for _, side := range []int{(p.Dir + 4) % 16, (p.Dir + 12) % 16} {
			if s := layout.Step(t, side); l.Free(s, layout.AnyLand) {
				res = append(res, Port{T: s, Dir: (side + 8) % 16, Item: lns[0].Item, In: true})
			}
		}
	}
	sort.Slice(res, func(i, j int) bool {
		if di, dj := dist(res[i].T, from), dist(res[j].T, from); di != dj {
			return di < dj
		}
		return res[i].T.X < res[j].T.X || res[i].T.X == res[j].T.X && res[i].T.Y < res[j].T.Y
	})
	return res[:min(len(res), 40)]
}

// fence — подводы с добычи c — только по пустому месту (cluster.md §2а «Добыча»): зоны и
// площади других кластеров закрыты (своя зона добычи открыта, кроме чужих блоков в ней), шина
// ресурсов bus — тоже, кроме рядов лент слева от голов. Возвращает закрытое — открыть (unfence).
func (b *Base) fence(l *layout.Layout, bus *ZBus, c *Cluster) []world.Tile {
	rs := []Rect{bus.Rect}
	// и другие шины: подвод вдоль ряда bus-2 перегородил ей ленту на стадии 6 (954176679)
	for _, r := range b.busRects {
		if r != bus.Rect {
			rs = append(rs, r)
		}
	}
	for _, o := range b.Clusters {
		if o == c {
			continue
		}
		r := o.Area
		if o.Zone != nil && (c.Zone == nil || *o.Zone != *c.Zone) {
			r = *o.Zone
		}
		rs = append(rs, r)
	}
	open := map[world.Tile]bool{}
	for _, ln := range bus.Lanes {
		for x := bus.Rect.Min.X - 1; x < ln.Start; x++ {
			open[world.Tile{X: x, Y: ln.Y}] = true
		}
	}
	var res []world.Tile
	for _, r := range rs {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if t := (world.Tile{X: x, Y: y}); !open[t] && !l.Reserved[t] {
					l.Reserved[t] = true
					res = append(res, t)
				}
			}
		}
	}
	return res
}

func unfence(l *layout.Layout, ts []world.Tile) {
	for _, t := range ts {
		delete(l.Reserved, t)
	}
}

// dist — расстояние между тайлами (манхэттен).
func dist(a, b world.Tile) int { return abs(a.X-b.X) + abs(a.Y-b.Y) }

// outPorts — все выходы кластера.
func outPorts(c *Cluster) []Port {
	var res []Port
	for _, p := range c.Ports {
		if !p.In {
			res = append(res, p)
		}
	}
	return res
}

// sortedItems — предметы лент шины k: сначала свои (этажа под шиной), потом сквозные.
func sortedItems(need map[string]bool, on map[string]int, k int) []string {
	var items []string
	for it := range need {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool {
		si, sj := on[items[i]] == k, on[items[j]] == k
		if si != sj {
			return si
		}
		return items[i] < items[j]
	})
	return items
}

// ConnectZoned — логистика кластеров в зонах: ленты шин, подводы с добычи в головы лент шины
// ресурсов, выходы этажей — боком в ленты шины над собой, сквозные ленты (что нужно выше), входы
// — делителем с ленты шины снизу (снабжение — и сверху), уголь на станцию — отводом с ленты
// добычи угля, вода станции — помпой.
func (b *Base) ConnectZoned(l *layout.Layout, buses []*ZBus) error {
	on := b.zoneSources(len(buses))
	need := b.zoneNeed(on, len(buses))
	// bus-1: ленты добытого — по выходу каждого блока (и будущих), ряды — по порядку подводов
	// вокруг головы (zones.md: сверху-справа — верхний ряд, дальше по кругу против часовой)
	if err := b.orderedMineLanes(l, buses); err != nil {
		return err
	}
	for k := range buses {
		for _, it := range sortedItems(need[k], on, k) {
			if k == 0 && len(buses[0].lanes(it)) > 0 {
				continue // добытое — уже (orderedMineLanes)
			}
			for range b.laneCount(k, it) {
				if err := b.newLane(l, buses, k, it, b.laneEnd(buses, k, it)); err != nil {
					return err
				}
			}
		}
	}
	g := guardPorts(l, b.Clusters)
	// вода станции — первой (потом ленты не замуруют вход); подход ко входу угля (4 тайла) —
	// свободным, иначе его займут трубы
	var keep []world.Tile
	for _, c := range b.Clusters {
		for _, p := range c.Ports {
			if c.Kind != Power || !p.In {
				continue
			}
			for k, t := 0, p.T; k < 4; k, t = k+1, layout.Step(t, (p.Dir+8)%16) {
				if !l.Reserved[t] {
					l.Reserved[t] = true
					keep = append(keep, t)
				}
			}
		}
	}
	for _, c := range b.Clusters {
		if c.Water == nil {
			continue
		}
		_, err := l.WaterPump(c.Water.To, LogisticsTag+"/"+c.Name, func(pump layout.Placement) bool {
			if !l.CanPlace(pump, layout.AnyLand) {
				return false
			}
			l.Add(pump)
			_, err := l.RoutePipe(layout.Connections(l.P, pump)[0], *c.Water, LogisticsTag+"/"+c.Name)
			return err == nil
		})
		if err != nil {
			// помпа не дотянулась — станцию в другое место у воды (план перекладывается)
			return &ConnectError{c, fmt.Errorf("вода к %s: %w", c.Name, err)}
		}
	}
	// добыча → головы лент шины ресурсов, слева: сначала вся, кроме угля (лента угля на станцию,
	// проложенная первой, замуровывала выходы соседних блоков — 859088572); подход ко входу угля
	// станции всё это время закрыт (keep) — потом уголь на станцию и его отвод в шину
	feedMine := func(c *Cluster) error { return b.feedMine(l, buses, c, nil) }
	for _, c := range b.Clusters {
		if c.Kind == Mine && c.Product != "coal" && !c.IsOil() {
			if err := feedMine(c); err != nil {
				return err
			}
		}
	}
	for _, t := range keep {
		delete(l.Reserved, t)
	}
	// уголь: лента с добычи прямо на станцию (мимо шин), с неё делителем — в голову ленты шины
	// ресурсов; станции нет или уже питается — добыча угля сразу на шину
	powered := false
	for _, m := range b.Clusters {
		if m.Kind != Mine || m.Product != "coal" {
			continue
		}
		op, ok := outPort(m)
		var pw *Cluster
		var pp Port
		for _, c := range b.Clusters {
			for _, p := range c.Ports {
				if c.Kind == Power && p.In && p.Item == "coal" {
					pw, pp = c, p
				}
			}
		}
		if !ok || pw == nil || powered {
			if err := feedMine(m); err != nil {
				return err
			}
			continue
		}
		// на станцию — выход, ближний к её входу (дальний уводил ленту в обход всего блока)
		for _, p := range outPorts(m) {
			if dist(p.T, pp.T) < dist(op.T, pp.T) {
				op = p
			}
		}
		mark := l.Mark()
		if _, err := zRoute(l, op, []Port{pp}, LogisticsTag+"/"+pw.Name); err != nil {
			return &ConnectError{pw, fmt.Errorf("уголь %s → %s: %w", m.Name, pw.Name, err)}
		}
		powered = true
		line := append([]layout.Placement(nil), l.List[mark:]...)
		bus := buses[0]
		lns := bus.lanes(m.Product)
		if len(lns) == 0 {
			continue
		}
		opened := b.fence(l, bus, m)
		err := layout.ErrNoRoute
		for _, p := range b.feedTargets(l, bus, lns, op) {
			g := guardHeads(l, bus, p.T)
			err = SplitTap(l, line, p, LogisticsTag+"/"+bus.Name+"/"+m.Name, 60)
			unfence(l, g)
			if err == nil {
				fedHead(lns, p)
				break
			}
		}
		unfence(l, opened)
		if err != nil {
			return fmt.Errorf("уголь %s → %s (отвод со станции): %w", m.Name, bus.Name, err)
		}
		// второй выход блока — сразу на шину
		if err := b.feedMine(l, buses, m, &op); err != nil {
			return err
		}
	}
	// будущая добыча питает и старые цепочки — перекидки на шине (zones.md)
	b.transfers(l, buses[0])
	// выходы этажей — боком в ленты шины над собой; отводы — ниже по течению
	for _, c := range b.Clusters {
		if err := b.feedOut(l, buses, c, g); err != nil {
			return err
		}
	}
	if err := feedThrough(l, buses); err != nil {
		return err
	}
	// соседние ленты одного предмета — делят друг с другом, до первого отвода (cluster.md §5)
	for _, bus := range buses {
		balanceLanes(l, bus)
	}
	// входы: делителем с ленты шины снизу (снабжение — и сверху), у своих столбцов
	for _, c := range b.Clusters {
		if floorOf(c) == 0 {
			continue
		}
		for _, p := range c.Ports {
			if p.In {
				if err := feedIn(l, buses, c, p, g); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// balanceLanes — соседние (ряд к ряду) ленты одного предмета шины bus делят друг с другом
// делителями (cluster.md §5): цепочка лент — пары с конца (последняя со предпоследней раньше
// по ходу, дальше — следующая пара), сразу за последним подводом, на прямом участке без боковых
// подач.
func balanceLanes(l *layout.Layout, bus *ZBus) {
	byItem := map[string][]ZoneLane{}
	for _, ln := range bus.Lanes {
		if ln.Fed {
			byItem[ln.Item] = append(byItem[ln.Item], ln)
		}
	}
	for item, lns := range byItem {
		sort.Slice(lns, func(i, j int) bool { return lns[i].Y < lns[j].Y })
		// цепочки соседних рядов
		var chains [][]ZoneLane
		for i, ln := range lns {
			if i > 0 && ln.Y == lns[i-1].Y+1 {
				chains[len(chains)-1] = append(chains[len(chains)-1], ln)
			} else {
				chains = append(chains, []ZoneLane{ln})
			}
		}
		for _, ch := range chains {
			if len(ch) < 2 {
				continue
			}
			from := math.MinInt
			for _, ln := range ch {
				from = max(from, ln.From, ln.Start)
			}
			// направление — по ленте у начала
			d := -1
			for x := from; x < from+40 && d < 0; x++ {
				if q := l.At(world.Tile{X: x, Y: ch[0].Y}); q != nil && q.Name == "transport-belt" && q.Recipe == item {
					d = q.Dir
				}
			}
			if d < 0 {
				continue
			}
			step := 1
			if d == layout.W {
				step = -1
			}
			x := from + 2*step
			for k := len(ch) - 1; k >= 1; k-- {
				a, b := ch[k-1], ch[k]
				for n := 0; n < 80; n++ {
					if balanceAt(l, item, a.Y, b.Y, x, d) {
						x += 2 * step
						break
					}
					x += step
				}
			}
		}
	}
}

// balanceAt — делитель поперёк рядов ya, yb (соседних) в столбце x, если там прямые ленты item
// направления d без боковых подач; true — поставлен.
func balanceAt(l *layout.Layout, item string, ya, yb, x, d int) bool {
	ta, tb := world.Tile{X: x, Y: ya}, world.Tile{X: x, Y: yb}
	for _, t := range []world.Tile{ta, tb} {
		for _, u := range []world.Tile{layout.Step(t, (d+8)%16), t, layout.Step(t, d)} {
			q := l.At(u)
			if q == nil || q.Name != "transport-belt" || q.Recipe != item || q.Dir != d {
				return false
			}
		}
	}
	// боком в ta/tb ничего не въезжает (снаружи пары)
	for _, side := range []struct {
		t   world.Tile
		dir int
	}{{ta, (d + 12) % 16}, {ta, (d + 4) % 16}, {tb, (d + 12) % 16}, {tb, (d + 4) % 16}} {
		n := layout.Step(side.t, side.dir)
		if n == ta || n == tb {
			continue
		}
		if q := l.At(n); q != nil && (q.Name == "transport-belt" || q.Name == "underground-belt") && q.Dir == (side.dir+8)%16 {
			return false
		}
	}
	ia, _ := l.Index(ta)
	ib, _ := l.Index(tb)
	tag := l.List[ia].Tag
	ca, cb := ta.Center(), tb.Center()
	sp := layout.Placement{Name: "splitter", Pos: bridge.Pos{X: (ca.X + cb.X) / 2, Y: (ca.Y + cb.Y) / 2}, Dir: d, Tag: tag}
	l.Replace(ia, sp) // делитель на месте ленты ta, лента tb — убрать
	l.Remove(ib)
	return true
}

// BalancePlan — правило «ленты одного предмета делят друг с другом» (cluster.md §5) для уже
// сохранённого плана l (обслуживание, stages.md, стадия 28): ленты шин восстанавливаются по
// раскладке (ряды лент с предметом в Recipe у тега шины), делители дописываются. Сколько
// делителей добавлено.
func BalancePlan(l *layout.Layout, buses []string) int {
	before := l.Count()["splitter"]
	for _, name := range buses {
		tag := Tag(name)
		type key struct {
			item string
			y    int
		}
		lanes := map[key]*ZoneLane{}
		for _, pl := range l.List {
			if pl.Tag != tag || pl.Name != "transport-belt" || pl.Recipe == "" {
				continue
			}
			t := world.TileOf(pl.Pos)
			k := key{pl.Recipe, t.Y}
			ln := lanes[k]
			if ln == nil {
				ln = &ZoneLane{Item: pl.Recipe, Y: t.Y, Fed: true, Start: t.X}
				lanes[k] = ln
			}
			// голова — начало по ходу (на восток — левый край, на запад — правый)
			if pl.Dir == layout.W && t.X > ln.Start || pl.Dir != layout.W && t.X < ln.Start {
				ln.Start = t.X
			}
			ln.From = ln.Start
		}
		bus := &ZBus{Name: name}
		for _, ln := range lanes {
			bus.Lanes = append(bus.Lanes, *ln)
		}
		balanceLanes(l, bus)
	}
	return l.Count()["splitter"] - before
}

// ugBetween — между a и b (ряд, по X) есть подземка вдоль ряда: своя пара в разрыве сцепилась бы
// не с той.
func ugBetween(l *layout.Layout, a, b world.Tile) bool {
	for x := a.X + 1; x < b.X; x++ {
		if p := l.At(world.Tile{X: x, Y: a.Y}); p != nil && p.Name == "underground-belt" && (p.Dir == layout.E || p.Dir == layout.W) {
			return true
		}
	}
	return false
}
