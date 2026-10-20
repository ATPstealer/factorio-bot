package cluster

import (
	"fmt"
	"math"
	"sort"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// ConnectError — ленту к кластеру Consumer не провести (без подземок ленты не пересекаются:
// кластер встал по ту сторону чужой ленты) — переставить его.
type ConnectError struct {
	Consumer *Cluster
	Err      error
}

func (e *ConnectError) Error() string { return e.Err.Error() }
func (e *ConnectError) Unwrap() error { return e.Err }

// LogisticsTag — тег столбов между кластерами; ленты и трубы к кластеру X — LogisticsTag+"/X".
const LogisticsTag = "c/logistics"

// Connect прокладывает логистику между кластерами в раскладке Layout (внутренности закрыты —
// всё идёт по коридорам): от выхода поставщика — лента к самому большому потребителю этого
// предмета; остальным — отвод инсертером с этой ленты (разделителей до logistics нет; потоки
// малые) своей лентой; вода станции — помпа и труба; столбы связывают сети кластеров между
// собой и с anchors (уже стоящая сеть).
//
// existing — уже стоящая (спроектированная прошлыми этапами) логистика: выход поставщика,
// от которого лента уже идёт, — отвод с этой ленты; fresh — чьи входы подводить (нет — всех).
func (b *Base) Connect(l *layout.Layout, anchors, existing []layout.Placement, fresh ...*Cluster) error {
	if len(fresh) == 0 {
		fresh = b.Clusters
	}
	lines := map[world.Tile]layout.Placement{}
	for _, p := range existing {
		if p.Name == "transport-belt" || p.Name == "underground-belt" {
			lines[world.TileOf(p.Pos)] = p
		}
	}
	type cons struct {
		c    *Cluster
		port Port
		rate float64
	}
	byOut := map[*Cluster][]cons{}
	outPort := map[*Cluster]map[string]Port{}
	for _, c := range b.Clusters {
		for _, p := range c.Ports {
			if !p.In {
				if outPort[c] == nil {
					outPort[c] = map[string]Port{}
				}
				outPort[c][p.Item] = p
			}
		}
	}
	for _, c := range fresh {
		for _, p := range c.Ports {
			if !p.In {
				continue
			}
			src := b.supplier(c, p.Item, outPort)
			if src == nil {
				return fmt.Errorf("cluster: %s ← %s: нет поставщика", c.Name, p.Item)
			}
			byOut[src] = append(byOut[src], cons{c, p, rateOf(c.In, p.Item)})
		}
	}
	srcs := make([]*Cluster, 0, len(byOut))
	for s := range byOut {
		srcs = append(srcs, s)
	}
	// вода станции — первой: потом ленты подойдут к её трубе (не наоборот — лента угля
	// замурует вход воды)
	for _, c := range fresh {
		if c.Water == nil {
			continue
		}
		near := c.Water.To
		_, err := l.WaterPump(near, LogisticsTag+"/"+c.Name, func(pump layout.Placement) bool {
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
	// сначала короткие связи (меньше перегораживают); не вышло — откатить всё и начать с
	// неудавшейся (без подземок ленты не пересекаются — важен порядок)
	dist := func(src *Cluster) int {
		cs := byOut[src]
		op := outPort[src][cs[0].port.Item]
		return abs(op.T.X-cs[0].port.T.X) + abs(op.T.Y-cs[0].port.T.Y)
	}
	sort.Slice(srcs, func(i, j int) bool {
		if di, dj := dist(srcs[i]), dist(srcs[j]); di != dj {
			return di < dj
		}
		return srcs[i].Name < srcs[j].Name
	})
	// порты и тайл подхода к ним — бронь: чужая лента не займёт (без подземок не обойти)
	approach := func(p Port) []world.Tile {
		if p.In {
			return []world.Tile{p.T, layout.Step(p.T, (p.Dir+8)%16)}
		}
		return []world.Tile{p.T, layout.Step(p.T, p.Dir)}
	}
	reserve := func(p Port, on bool) {
		for _, t := range approach(p) {
			if on && l.At(t) == nil {
				l.Reserved[t] = true
			} else if !on {
				delete(l.Reserved, t)
			}
		}
	}
	route := func(src *Cluster) error {
		cs := byOut[src]
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].rate > cs[j].rate })
		item := cs[0].port.Item
		op := outPort[src][item]
		reserve(op, false)
		// лента от этого выхода уже есть (прошлый этап) — всем отводы с неё
		line := follow(lines, op.T)
		rest := cs
		if len(line) == 0 {
			mark := len(l.List)
			reserve(cs[0].port, false)
			if _, err := l.RouteBelt(op.T, op.Dir, cs[0].port.T, cs[0].port.Dir, LogisticsTag+"/"+cs[0].c.Name); err != nil {
				return &ConnectError{cs[0].c, fmt.Errorf("лента %s → %s (%s): %w", src.Name, cs[0].c.Name, item, err)}
			}
			line = append([]layout.Placement(nil), l.List[mark:]...)
			lineBuffer(l, line, cs[0].c, item, cs[0].rate, LogisticsTag+"/"+cs[0].c.Name)
			rest = cs[1:]
		}
		for _, k := range rest {
			reserve(k.port, false)
			mark := len(l.List)
			if err := tap(l, line, k.port, LogisticsTag+"/"+k.c.Name); err != nil {
				return &ConnectError{k.c, fmt.Errorf("отвод %s → %s (%s): %w", src.Name, k.c.Name, item, err)}
			}
			var branch []layout.Placement
			for _, pl := range l.List[mark:] {
				if pl.Name == "transport-belt" {
					branch = append(branch, pl)
				}
			}
			lineBuffer(l, branch, k.c, item, k.rate, LogisticsTag+"/"+k.c.Name)
		}
		return nil
	}
	reserved := map[world.Tile]bool{}
	for t := range l.Reserved {
		reserved[t] = true
	}
	mark := l.Mark()
	var err error
	for try := 0; try < 2*len(srcs)+2; try++ {
		for _, c := range b.Clusters {
			for _, p := range c.Ports {
				reserve(p, true)
			}
		}
		err = nil
		t0 := time.Now()
		for k, src := range srcs {
			if err = route(src); err != nil {
				if b.Log != nil {
					b.Log("логистика: попытка %d — %v (%.1f с)", try+1, err, time.Since(t0).Seconds())
				}
				// откат и заново — с неудавшейся
				l.Rollback(mark)
				l.Reserved = map[world.Tile]bool{}
				for t := range reserved {
					l.Reserved[t] = true
				}
				srcs = append([]*Cluster{src}, append(srcs[:k:k], srcs[k+1:]...)...)
				break
			}
		}
		if err == nil {
			break
		}
	}
	if err != nil {
		return err
	}
	l.Anchors = append(l.Anchors, anchors...)
	t0 := time.Now()
	l.Poles("small-electric-pole", LogisticsTag)
	if b.Log != nil {
		b.Log("логистика: столбы %.1f с", time.Since(t0).Seconds())
	}
	return nil
}

// follow — лента от тайла t по направлениям (до конца или петли), сквозь подземки: со входа —
// на его выход (ближайший по ходу); в списке — только обычные ленты (на них — отводы).
func follow(lines map[world.Tile]layout.Placement, t world.Tile) []layout.Placement {
	var res []layout.Placement
	seen := map[world.Tile]bool{}
	for {
		p, ok := lines[t]
		if !ok || seen[t] {
			return res
		}
		seen[t] = true
		if p.Name == "underground-belt" && p.Recipe == "input" {
			out := t
			found := false
			for k := 1; k <= 10 && !found; k++ {
				out = layout.Step(out, p.Dir)
				if q, ok := lines[out]; ok && q.Name == "underground-belt" && q.Recipe == "output" && q.Dir == p.Dir {
					found = true
				}
			}
			if !found {
				return res
			}
			seen[out] = true
			t = layout.Step(out, p.Dir)
			continue
		}
		if p.Name == "transport-belt" {
			res = append(res, p)
		}
		t = layout.Step(t, p.Dir)
	}
}

// supplier — кластер с выходом item (ближайший к c).
func (b *Base) supplier(c *Cluster, item string, outs map[*Cluster]map[string]Port) *Cluster {
	var best *Cluster
	bd := math.Inf(1)
	cx, cy := c.Area.Center()
	for _, o := range b.Clusters {
		if _, ok := outs[o][item]; !ok || o == c {
			continue
		}
		x, y := o.Area.Center()
		if d := math.Hypot(x-cx, y-cy); d < bd {
			best, bd = o, d
		}
	}
	return best
}

func rateOf(fs []Flow, item string) float64 {
	for _, f := range fs {
		if f.Item == item {
			return f.Rate
		}
	}
	return 0
}

// tap — отвод с ленты line к порту p: делитель (splitter) на прямом участке — второй его
// выход начинает новую ленту к p (решение пользователя: инсертер с ленты на ленту не тянет
// поток). Делитель недоступен — инсертер сбоку. Пробуются места ближе к p.
func tap(l *layout.Layout, line []layout.Placement, p Port, tag string) error {
	if l.Usable == nil || l.Usable("splitter") {
		if err := splitTap(l, line, p, tag); err == nil {
			return nil
		}
	}
	type cand struct {
		at, ins, start world.Tile
		side           int
		d              int
	}
	var cs []cand
	for _, b := range line {
		if b.Name != "transport-belt" {
			continue
		}
		t := world.TileOf(b.Pos)
		for _, side := range []int{(b.Dir + 4) % 16, (b.Dir + 12) % 16} {
			s := layout.Step(t, side)
			q := layout.Step(s, side)
			if !l.Free(s, layout.AnyLand) || !l.Free(q, layout.AnyLand) {
				continue
			}
			cs = append(cs, cand{t, s, q, side, abs(q.X-p.T.X) + abs(q.Y-p.T.Y)})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	var err error = layout.ErrNoRoute
	for i, c := range cs {
		if i >= 80 {
			break
		}
		mark := l.Mark()
		l.Add(layout.Placement{Name: "inserter", Pos: c.ins.Center(), Dir: layout.InserterDir(l.P, "inserter", c.ins, c.at), Recipe: p.Item, Tag: tag})
		if _, err = l.RouteBelt(c.start, c.side, p.T, p.Dir, tag); err == nil {
			return nil
		}
		l.Rollback(mark)
	}
	return err
}

// splitTap — делитель вместо ленты t прямого участка line (лента до и после — того же
// направления), рядом с ней — тайл s; с выхода s — новая лента к порту p. Лента t из этой
// раскладки заменяется на месте; уже стоящая (прошлых этапов) — делитель ставится поверх
// (игра заменяет ленту), раскладка прошлого этапа снимает её (Runner.supersede).
func splitTap(l *layout.Layout, line []layout.Placement, p Port, tag string) error {
	return SplitTap(l, line, p, tag, 60)
}

// SplitTap — splitTap для уже стоящей линии (переделка старых отводов): ленты line могут
// быть чужими (в Reserved), тогда и второй вход делителя не должен быть занят.
// tries — сколько мест делителя пробовать (ближайшие к p).
func SplitTap(l *layout.Layout, line []layout.Placement, p Port, tag string, tries int) error {
	dir := map[world.Tile]int{}
	for _, b := range line {
		if b.Name == "transport-belt" {
			dir[world.TileOf(b.Pos)] = b.Dir
		}
	}
	type cand struct {
		t, s  world.Tile
		d, at int
	}
	var cs []cand
	for _, b := range line {
		if b.Name != "transport-belt" {
			continue
		}
		t, d := world.TileOf(b.Pos), b.Dir
		if q := l.At(t); q != nil && q.Name != "transport-belt" {
			continue // уже делитель (прошлый отвод)
		}
		if pd, ok := dir[layout.Step(t, (d+8)%16)]; !ok || pd != d {
			continue
		}
		if nd, ok := dir[layout.Step(t, d)]; !ok || nd != d {
			continue
		}
		for _, side := range []int{(d + 4) % 16, (d + 12) % 16} {
			s := layout.Step(t, side)
			start, behind := layout.Step(s, d), layout.Step(s, (d+8)%16)
			if !l.Free(s, layout.AnyLand) || !l.Free(start, layout.AnyLand) {
				continue
			}
			if q := l.At(behind); q != nil && (q.Name == "transport-belt" || q.Name == "underground-belt" || q.Name == "splitter") || q == nil && l.Reserved[behind] {
				continue // во второй вход делителя что-то въедет
			}
			cs = append(cs, cand{t, s, d, abs(start.X-p.T.X) + abs(start.Y-p.T.Y)})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].at < cs[j].at })
	var err error = layout.ErrNoRoute
	for i, c := range cs {
		if i >= tries {
			break
		}
		tc, sc := c.t.Center(), c.s.Center()
		sp := layout.Placement{Name: "splitter", Pos: bridge.Pos{X: (tc.X + sc.X) / 2, Y: (tc.Y + sc.Y) / 2}, Dir: c.d, Tag: tag}
		mark := l.Mark()
		idx, own := l.Index(c.t)
		var old layout.Placement
		if own {
			old = l.List[idx]
			l.Replace(idx, sp)
		} else {
			l.Add(sp)
		}
		if _, err = l.RouteBelt(layout.Step(c.s, c.d), c.d, p.T, p.Dir, tag); err == nil {
			return nil
		}
		if own {
			l.Replace(idx, old)
		}
		l.Rollback(mark)
	}
	return err
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// lineBuffer — буфер входа кластера c с двумя входами — на его входящей ленте предмета item
// (там только этот предмет: руда и уголь не смешаются): вдоль прямого участка у порта —
// ряд инсертеров и ряд ящиков сбоку; инсертер с ленты в первый ящик (выше по ходу), по ящикам,
// из последнего — обратно на ленту ниже по ходу. Ёмкость — на BufferSeconds; нет места —
// без буфера. line — ленты маршрута (с конца: у порта — первые).
func lineBuffer(l *layout.Layout, line []layout.Placement, c *Cluster, item string, rate float64, tag string) {
	if len(c.In) < 2 || rate <= 0 {
		return
	}
	stack := l.P.Items[item].StackSize
	if stack <= 0 {
		stack = 50
	}
	n := max(2, int(math.Ceil(rate*BufferSeconds/float64(16*stack))))
	span := 2*n - 1 // ящики через тайл, между ними — перекладывающие инсертеры
	for k := 0; k+span <= len(line) && k < 30; k++ {
		run := line[k : k+span]
		straight := true
		for i, b := range run {
			straight = straight && b.Name == "transport-belt" && b.Dir == run[0].Dir
			// подряд на карте (между ними может быть подземка — тогда не участок)
			if i > 0 && straight {
				straight = layout.Step(world.TileOf(b.Pos), b.Dir) == world.TileOf(run[i-1].Pos)
			}
		}
		if !straight {
			continue
		}
		for _, side := range []int{(run[0].Dir + 4) % 16, (run[0].Dir + 12) % 16} {
			ok := true
			var ins, box []world.Tile
			for _, b := range run {
				t := world.TileOf(b.Pos)
				a, q := layout.Step(t, side), layout.Step(layout.Step(t, side), side)
				ok = ok && l.Free(a, layout.AnyLand) && l.Free(q, layout.AnyLand)
				ins, box = append(ins, a), append(box, q)
			}
			if !ok {
				continue
			}
			// run[0] — ниже по ходу (ближе к порту), run[span-1] — выше
			up, down := span-1, 0
			for j := up; j >= down; j -= 2 {
				l.Add(layout.Placement{Name: "wooden-chest", Pos: box[j].Center(), Tag: tag})
				if j-2 >= down {
					m := box[j-1]
					l.Add(layout.Placement{Name: "inserter", Pos: m.Center(), Dir: layout.InserterDir(l.P, "inserter", m, box[j]), Recipe: item, Tag: tag})
				}
			}
			bt := func(j int) world.Tile { return world.TileOf(run[j].Pos) }
			l.Add(layout.Placement{Name: "inserter", Pos: ins[up].Center(), Dir: layout.InserterDir(l.P, "inserter", ins[up], bt(up)), Recipe: item, Tag: tag})
			l.Add(layout.Placement{Name: "inserter", Pos: ins[down].Center(), Dir: layout.InserterDir(l.P, "inserter", ins[down], box[down]), Recipe: item, Tag: tag})
			return
		}
	}
}
