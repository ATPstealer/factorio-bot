package cluster

import (
	"sort"

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

// SplitTap — отвод с ленты line к порту p: делитель вместо ленты t прямого участка (лента до и
// после — того же направления), рядом с ней — тайл s; с выхода s — новая лента к p. Лента t
// из этой раскладки заменяется на месте; ленты line могут быть чужими (в Reserved), тогда и
// второй вход делителя не должен быть занят. tries — сколько мест делителя пробовать
// (ближайшие к p).
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
		fed := false // сюда сбоку въезжает подвод: делитель его оборвёт
		for _, side := range []int{(d + 4) % 16, (d + 12) % 16} {
			if q := l.At(layout.Step(t, side)); q != nil && (q.Name == "transport-belt" || q.Name == "underground-belt" && q.Recipe == "output") && q.Dir == (side+8)%16 {
				fed = true
			}
		}
		if fed {
			continue
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

// SplitJoin — подвод от from в ленту line делителем, а не боком (сбоку всё ложится на одну
// полосу; делитель полосы сохраняет — продукт с обеих полос кластера идёт по обеим полосам
// ленты): лента t прямого участка line — делитель, его второй вход — тайл g рядом (зазор),
// подвод — прямо в него с тайла перед g; второй выход (за g) — пустой и закрыт (Reserved):
// всё — в ленту. tries — сколько мест пробовать (ближайшие к from). Возвращает тайл t.
func SplitJoin(l *layout.Layout, line []layout.Placement, from Port, tag string, tries int) (world.Tile, error) {
	dir := map[world.Tile]int{}
	for _, b := range line {
		if b.Name == "transport-belt" {
			dir[world.TileOf(b.Pos)] = b.Dir
		}
	}
	type cand struct {
		t, g, in, out world.Tile
		d, at         int
	}
	var cs []cand
	for _, b := range line {
		if b.Name != "transport-belt" {
			continue
		}
		t, d := world.TileOf(b.Pos), b.Dir
		if q := l.At(t); q != nil && q.Name != "transport-belt" {
			continue // уже делитель
		}
		if pd, ok := dir[layout.Step(t, (d+8)%16)]; !ok || pd != d {
			continue
		}
		if nd, ok := dir[layout.Step(t, d)]; !ok || nd != d {
			continue
		}
		for _, side := range []int{(d + 4) % 16, (d + 12) % 16} {
			g := layout.Step(t, side)
			in, out := layout.Step(g, (d+8)%16), layout.Step(g, d)
			if !l.Free(g, layout.AnyLand) || !l.Free(in, layout.AnyLand) || !l.Free(out, layout.AnyLand) {
				continue
			}
			cs = append(cs, cand{t, g, in, out, d, abs(in.X-from.T.X) + abs(in.Y-from.T.Y)})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].at < cs[j].at })
	var err error = layout.ErrNoRoute
	for i, c := range cs {
		if i >= tries {
			break
		}
		tc, gc := c.t.Center(), c.g.Center()
		sp := layout.Placement{Name: "splitter", Pos: bridge.Pos{X: (tc.X + gc.X) / 2, Y: (tc.Y + gc.Y) / 2}, Dir: c.d, Tag: tag}
		mark := l.Mark()
		idx, own := l.Index(c.t)
		var old layout.Placement
		if own {
			old = l.List[idx]
			l.Replace(idx, sp)
		} else {
			l.Add(sp)
		}
		l.Reserved[c.out] = true // второй выход — пустой: всё в ленту
		if _, err = l.RouteBelt(from.T, from.Dir, c.in, c.d, tag); err == nil {
			return c.t, nil
		}
		delete(l.Reserved, c.out)
		if own {
			l.Replace(idx, old)
		}
		l.Rollback(mark)
	}
	return world.Tile{}, err
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
