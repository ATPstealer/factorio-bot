package layout

import (
	"fmt"
	"math"

	"factorio-bot/internal/world"
)

func isBelt(name string) bool {
	return name == "transport-belt" || name == "fast-transport-belt" || name == "express-transport-belt"
}

func isUnderground(name string) bool {
	return name == "underground-belt" || name == "fast-underground-belt" || name == "express-underground-belt"
}

func isPole(name string) bool {
	return name == "small-electric-pole" || name == "medium-electric-pole" || name == "big-electric-pole" || name == "substation"
}

// End — разрешённый конец ленты: туда лента выходит из раскладки (вход модуля, который
// раскладывается отдельно, или точка выхода шахты).
func (l *Layout) AllowEnd(t world.Tile) {
	if l.ends == nil {
		l.ends = map[world.Tile]bool{}
	}
	l.ends[t] = true
}

// Validate — все проверки; список проблем (пусто — раскладка годится).
func (l *Layout) Validate() []string {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	seen := map[world.Tile]int{}
	for i, pl := range l.List {
		for _, t := range Tiles(l.P, pl) {
			if j, ok := seen[t]; ok {
				add("пересечение: %s [%s] и %s [%s] в %v", l.List[j], l.List[j].Tag, pl, pl.Tag, t)
			}
			seen[t] = i
			if l.W != nil {
				if ok, _ := l.W.Buildable(t); !ok {
					add("%s: тайл %v не для стройки (вода/утёс/постройка/неоткрыто)", pl, t)
				}
			}
		}
	}
	for _, pl := range l.List {
		e := l.P.Entities[pl.Name]
		switch {
		case e.Type == "mining-drill":
			if l.W != nil {
				kinds := map[string]bool{}
				for _, t := range MiningArea(l.P, pl) {
					if r, ok := l.W.ResourceAt(t); ok {
						kinds[r.Name] = true
					}
				}
				switch {
				case len(kinds) == 0:
					add("%s: в зоне добычи нет руды", pl)
				case len(kinds) > 1:
					add("%s: в зоне добычи несколько ресурсов %v", pl, kinds)
				}
			}
			if out := DrillOutput(l.P, pl); l.At(out) == nil && !l.ends[out] {
				add("%s: выход %v никуда (нет ленты/постройки)", pl, out)
			}
		case isBelt(pl.Name):
			t := world.TileOf(pl.Pos)
			next := Step(t, pl.Dir)
			n := l.At(next)
			switch {
			case n == nil:
				if !l.ends[next] {
					add("лента %v %s обрывается", t, DirName(pl.Dir))
				}
			case isBelt(n.Name) || isUnderground(n.Name) || l.P.Entities[n.Name].Type == "splitter":
				if (n.Dir+8)%16 == pl.Dir || l.P.Entities[n.Name].Type == "splitter" && n.Dir != pl.Dir {
					add("лента %v %s упирается во встречную", t, DirName(pl.Dir))
				}
			default:
				if !l.ends[next] {
					add("лента %v %s упирается в %s", t, DirName(pl.Dir), n.Name)
				}
			}
		case isUnderground(pl.Name) && pl.Recipe == "input":
			t := world.TileOf(pl.Pos)
			max := l.P.Entities[pl.Name].MaxUndergroundDistance
			ok := false
			cur := t
			for k := 1; k <= max; k++ {
				cur = Step(cur, pl.Dir)
				if n := l.At(cur); n != nil && isUnderground(n.Name) && n.Recipe == "output" && n.Dir == pl.Dir {
					ok = true
					break
				}
			}
			if !ok {
				add("подземная лента %v %s: нет выхода в пределах %d", t, DirName(pl.Dir), max)
			}
		case e.Type == "pipe-to-ground":
			// подземная часть — в сторону, противоположную наземному соединению; пара смотрит навстречу
			t := world.TileOf(pl.Pos)
			d := (pl.Dir + 8) % 16
			ok := false
			cur := t
			for k := 1; k <= e.MaxUndergroundDistance+1; k++ {
				cur = Step(cur, d)
				if n := l.At(cur); n != nil && n.Name == pl.Name && n.Dir == d {
					ok = true
					break
				}
			}
			if !ok {
				add("подземная труба %v %s: нет пары", t, DirName(pl.Dir))
			}
		case e.Type == "inserter":
			pick, drop := InserterEnds(l.P, pl)
			if l.At(pick) == nil {
				add("%s: берёт с пустого %v", pl, pick)
			}
			if l.At(drop) == nil {
				add("%s: кладёт в пустой %v", pl, drop)
			}
		}
	}
	errs = append(errs, l.validatePower()...)
	return errs
}

// validatePower — электрические машины в зоне какого-нибудь столба, столбы связаны.
func (l *Layout) validatePower() []string {
	var errs []string
	poles := l.poleList()
	for _, pl := range l.List {
		e := l.P.Entities[pl.Name]
		if !e.Electric || isPole(pl.Name) {
			continue
		}
		if !l.powered(pl, poles) {
			errs = append(errs, fmt.Sprintf("%s: без электричества (нет столба рядом)", pl))
		}
	}
	if len(poles) > 1 {
		// связность по длине провода
		comp := make([]int, len(poles))
		for i := range comp {
			comp[i] = -1
		}
		var dfs func(i, c int)
		dfs = func(i, c int) {
			comp[i] = c
			for j := range poles {
				if comp[j] < 0 && wireOK(l, poles[i], poles[j]) {
					dfs(j, c)
				}
			}
		}
		n := 0
		for i := range poles {
			if comp[i] < 0 {
				dfs(i, n)
				n++
			}
		}
		if n > 1 {
			errs = append(errs, fmt.Sprintf("столбы не связаны: %d отдельных сетей", n))
		}
	}
	return errs
}

func wireOK(l *Layout, a, b Placement) bool {
	r := math.Min(l.P.Entities[a.Name].MaxWireDistance, l.P.Entities[b.Name].MaxWireDistance)
	return math.Hypot(a.Pos.X-b.Pos.X, a.Pos.Y-b.Pos.Y) <= r
}

// powered — постройка задевает зону питания хоть одного столба.
func (l *Layout) powered(pl Placement, poles []Placement) bool {
	for _, pole := range poles {
		r := l.P.Entities[pole.Name].SupplyAreaDistance
		for _, t := range Tiles(l.P, pl) {
			c := t.Center()
			if math.Abs(c.X-pole.Pos.X) < r+0.5 && math.Abs(c.Y-pole.Pos.Y) < r+0.5 {
				return true
			}
		}
	}
	return false
}
