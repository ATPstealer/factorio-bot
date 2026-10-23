package cluster

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

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
	case z.Kind == ZoneFinal && strings.HasSuffix(z.Name, "-2"):
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
			c.Name = b.name(c.Product)
		}
	}
	// добыча: зона своего ресурса, ближняя к голове шины ресурсов
	for _, c := range cs {
		if c.Kind != Mine {
			continue
		}
		placed := false
		sort.SliceStable(mines, func(i, j int) bool {
			xi, yi := mines[i].Rect.Center()
			xj, yj := mines[j].Rect.Center()
			return math.Hypot(xi-float64(head.X), yi-float64(head.Y)) < math.Hypot(xj-float64(head.X), yj-float64(head.Y))
		})
		for _, z := range mines {
			if z.Resources[c.Product] == 0 {
				continue
			}
			r := z.Rect
			c.Zone, c.Near = &r, &head
			if err := b.Place(c); err == nil {
				placed = true
				break
			}
		}
		if !placed {
			return nil, &NoSiteError{c, fmt.Errorf("cluster: в зонах добычи %s нет места под %s", c.Product, c.Name)}
		}
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
		case f > 0:
			// не левее поставщиков своих входов с этажей ниже (и своего — сборка берёт и с шины
			// над собой): продукт кладётся в ленту шины у столбца поставщика, отвод — только ниже
			// по течению (zones.md)
			for _, in := range c.In {
				first := math.MaxInt // самый левый поставщик предмета
				for _, p := range b.Clusters {
					if f2 := floorOf(p); f2 > 0 && (f2 < f || f2 == f && c.Kind == Make) && slices.ContainsFunc(p.Out, func(o Flow) bool { return o.Item == in.Item }) {
						first = min(first, p.Area.Min.X)
					}
				}
				if first != math.MaxInt && (c.MinX == nil || first > *c.MinX) {
					c.MinX = &first
				}
			}
			// части этажа слева направо — первая, где есть место; ряд — поперёк шин
			var err error = fmt.Errorf("cluster: нет зоны этажа %d под %s", f, c.Name)
			for _, r := range floor[f] {
				c.Zone, c.Upright = &r, true
				if err = b.Place(c); err == nil {
					break
				}
			}
			if err != nil {
				return nil, err
			}
			continue
		default: // хранение — вне зон, у стартовых буров
			cx, cy := base.Center()
			near := world.Tile{X: int(cx), Y: int(cy)}
			if b.StorageNear != nil {
				near = *b.StorageNear
			}
			c.Near = &near
		}
		if err := b.Place(c); err != nil {
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
			if src == f && c.Kind == Make && f < nb {
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
			use = use || in.Item == it && (f == k+1 || f == k && c.Kind == Make)
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

// fixGaps — неровности шины (вода, утёсы — до busThick площади, zones.md): ленты ряда — в
// обход, когда все ряды уже лежат (обход одной не займёт ряд другой).
func fixGaps(l *layout.Layout, buses []*ZBus, gaps []laneGap) error {
	for _, gp := range gaps {
		// обход — до первой ленты после препятствия, а не вышел — на 1–3 дальше (выход подземки
		// не может стоять на самой цели: узкую помеху вплотную к ней не перепрыгнуть)
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
			return fmt.Errorf("cluster: %s: лента %s не обходит неровность (%d, %d)–(%d, %d): %w", gp.tag, gp.item, gp.a.X, gp.a.Y, gp.b.X, gp.b.Y, err)
		}
		for i := mark; i < len(l.List); i++ {
			l.List[i].Recipe = gp.item
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
	to := zSidePorts(l, bus, ln, c.Area.Min.X-20, max(c.Area.Max.X+20, ln.Start+40)) // лента начинается за неровностью — ближайшие за ней
	sort.SliceStable(to, func(i, j int) bool {
		return math.Abs(float64(to[i].T.X-op.T.X)) < math.Abs(float64(to[j].T.X-op.T.X))
	})
	if len(to) > 40 {
		to = to[:40]
	}
	g.open(op)
	p, err := zRoute(l, op, to, LogisticsTag+"/"+bus.Name+"/"+c.Name)
	if err != nil {
		return &ConnectError{c, fmt.Errorf("%s → %s (%s): %w", c.Name, bus.Name, op.Item, err)}
	}
	ln.feed(p.T.X)
	return nil
}

// feedThrough — сквозные ленты: голова ленты шины k без поставщика — отводом с той же ленты
// шины k-1.
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
			to := append([]Port{{T: world.Tile{X: ln.Start - 1, Y: ln.Y}, Dir: layout.E, Item: ln.Item, In: true}},
				zSidePorts(l, buses[k], ln, buses[k].Rect.Min.X, low.From+41)...)
			err := layout.ErrNoRoute
			for _, p := range to {
				if err = SplitTap(l, line, p, LogisticsTag+"/"+buses[k].Name+"/"+buses[k-1].Name, 40); err == nil {
					ln.feed(p.T.X)
					break
				}
			}
			if err != nil {
				return fmt.Errorf("сквозная %s → %s (%s): %w", buses[k-1].Name, buses[k].Name, ln.Item, err)
			}
		}
	}
	return nil
}

// feedIn — вход p кластера c: делителем с ленты шины снизу (сборка — и сверху), у своего
// столбца, только ниже подвода в ленту.
func feedIn(l *layout.Layout, buses []*ZBus, c *Cluster, p Port, g *portGuard) error {
	f := floorOf(c)
	var line []layout.Placement
	for _, k := range []int{f - 1, f} {
		if k >= len(buses) || (k == f && c.Kind != Make) {
			continue
		}
		if ln := buses[k].lane(p.Item); ln != nil && len(line) == 0 {
			// только ниже подвода: выше по течению предмета на ленте нет
			x0 := max(c.Area.Min.X-40, ln.From+1)
			line = zLaneBelts(l, buses[k], ln, x0, max(c.Area.Max.X+40, x0+60))
		}
	}
	if len(line) == 0 {
		return fmt.Errorf("cluster: %s ← %s: на шинах рядом нет ленты", c.Name, p.Item)
	}
	g.open(p)
	if err := SplitTap(l, line, p, LogisticsTag+"/"+c.Name, 40); err != nil {
		return &ConnectError{c, fmt.Errorf("шина → %s (%s): %w", c.Name, p.Item, err)}
	}
	return nil
}

// feedMine — выход добычи c — в голову своей ленты шины ресурсов, есть поставщик — боком у
// головы.
func feedMine(l *layout.Layout, buses []*ZBus, c *Cluster) error {
	op, ok := outPort(c)
	if !ok {
		return nil
	}
	ln := buses[0].lane(c.Product)
	if ln == nil {
		return nil
	}
	bus := buses[0]
	var to []Port
	if !ln.Fed {
		to = append(to, Port{T: world.Tile{X: ln.Start - 1, Y: ln.Y}, Dir: layout.E, Item: ln.Item, In: true})
	}
	to = append(to, zSidePorts(l, bus, ln, bus.Rect.Min.X, bus.Rect.Min.X+12)...)
	p, err := zRoute(l, op, to, LogisticsTag+"/"+bus.Name+"/"+c.Name)
	if err != nil {
		return fmt.Errorf("добыча %s → %s (%s): %w", c.Name, bus.Name, c.Product, err)
	}
	ln.feed(p.T.X)
	return nil
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
	var gaps []laneGap
	for k, bus := range buses {
		for _, it := range sortedItems(need[k], on, k) {
			j := len(bus.Lanes)
			v := laneV(j)
			if v >= bus.Rect.H() {
				return fmt.Errorf("cluster: %s: нет ряда под ленту %s", bus.Name, it)
			}
			bus.Lanes = append(bus.Lanes, ZoneLane{Item: it, Y: bus.Rect.Min.Y + v, Start: bus.Rect.Min.X})
			b.layLane(l, buses, k, j, bus.Rect.Min.X, b.laneEnd(buses, k, it), -1, false, &gaps)
		}
	}
	if err := fixGaps(l, buses, gaps); err != nil {
		return err
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
			return fmt.Errorf("вода к %s: %w", c.Name, err)
		}
	}
	for _, t := range keep {
		delete(l.Reserved, t)
	}
	// добыча → головы лент шины ресурсов (второй поставщик — боком у головы); сначала уголь и
	// сразу отвод угля на станцию — иначе ленты другой добычи замуруют вход станции
	feedMine := func(c *Cluster) error { return feedMine(l, buses, c) }
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
		mark := l.Mark()
		if _, err := zRoute(l, op, []Port{pp}, LogisticsTag+"/"+pw.Name); err != nil {
			return &ConnectError{pw, fmt.Errorf("уголь %s → %s: %w", m.Name, pw.Name, err)}
		}
		powered = true
		line := append([]layout.Placement(nil), l.List[mark:]...)
		bus := buses[0]
		ln := bus.lane(m.Product)
		if ln == nil {
			continue
		}
		var to []Port
		if !ln.Fed {
			to = append(to, Port{T: world.Tile{X: ln.Start - 1, Y: ln.Y}, Dir: layout.E, Item: ln.Item, In: true})
		}
		to = append(to, zSidePorts(l, bus, ln, bus.Rect.Min.X, bus.Rect.Min.X+12)...)
		err := layout.ErrNoRoute
		for _, p := range to {
			if err = SplitTap(l, line, p, LogisticsTag+"/"+bus.Name+"/"+m.Name, 60); err == nil {
				ln.feed(p.T.X)
				break
			}
		}
		if err != nil {
			return fmt.Errorf("уголь %s → %s (отвод со станции): %w", m.Name, bus.Name, err)
		}
	}
	for _, c := range b.Clusters {
		if c.Kind == Mine && c.Product != "coal" {
			if err := feedMine(c); err != nil {
				return err
			}
		}
	}
	// выходы этажей — боком в ленты шины над собой; отводы — ниже по течению
	for _, c := range b.Clusters {
		if err := b.feedOut(l, buses, c, g); err != nil {
			return err
		}
	}
	if err := feedThrough(l, buses); err != nil {
		return err
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
