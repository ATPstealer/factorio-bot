package cluster

import (
	"fmt"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// LongPipes — подземные пары труб l, чьи концы дальше дальности прототипа: игра их не соединяет
// (pipeAlong ставил вход на прошлый тайл — пара длиннее на тайл; кислота дальше не шла: стадия 59
// на 1114315936). Пары — тайлы концов.
func LongPipes(l *layout.Layout) [][2]world.Tile {
	const ptg = "pipe-to-ground"
	limit := l.P.Entities[ptg].MaxUndergroundDistance
	seen := map[world.Tile]bool{}
	var res [][2]world.Tile
	for _, p := range l.List {
		t := world.TileOf(p.Pos)
		if p.Name != ptg || seen[t] {
			continue
		}
		u := (p.Dir + 8) % 16 // подземная сторона — против наземной
		cur := t
		for k := 1; k <= limit+1; k++ {
			cur = layout.Step(cur, u)
			q := l.At(cur)
			if q == nil || q.Name != ptg {
				continue
			}
			if q.Dir == u && q.Tag == p.Tag {
				seen[t], seen[cur] = true, true
				if k > limit {
					res = append(res, [2]world.Tile{t, cur})
				}
			}
			break // первая подземная на оси — её пара или помеха
		}
	}
	return res
}

// FixLongPipes — каждую пару LongPipes — обходом трубой (layout.RoutePipe) между соседями её
// концов, тем же тегом. Возвращает снятые концы и проложенное.
func FixLongPipes(l *layout.Layout) (removed, added []layout.Placement, err error) {
	for _, pr := range LongPipes(l) {
		ia, _ := l.Index(pr[0])
		ib, _ := l.Index(pr[1])
		a, b := l.List[ia], l.List[ib]
		na, nb := layout.Step(pr[0], a.Dir), layout.Step(pr[1], b.Dir) // к кому концы подключены
		l.Remove(max(ia, ib))
		l.Remove(min(ia, ib))
		route, rerr := l.RoutePipe(layout.Conn{At: na, To: pr[0], Dir: (a.Dir + 8) % 16},
			layout.Conn{At: nb, To: pr[1], Dir: (b.Dir + 8) % 16}, a.Tag)
		if rerr != nil {
			l.Add(a)
			l.Add(b)
			return removed, added, fmt.Errorf("cluster: труба %s: пара (%d, %d)–(%d, %d) длиннее дальности, обхода нет: %w",
				a.Tag, pr[0].X, pr[0].Y, pr[1].X, pr[1].Y, rerr)
		}
		removed = append(removed, a, b)
		added = append(added, route...) // RoutePipe уже добавил их в l
	}
	return removed, added, nil
}
