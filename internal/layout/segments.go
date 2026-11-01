package layout

import (
	"fmt"
	"slices"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

// Участки трубы (cluster.md §5): трубы, подземки и цистерны, связанные соединениями и
// подземными парами; насос и машины участки делят. В игре участок шире 320 тайлов по оси
// (default_pipeline_extent) стоит — жидкость не течёт вовсе.

// PipeExtentGame — предел участка в игре; PipeExtent — с запасом, до него режет SplitWide.
const PipeExtentGame, PipeExtent = 320, 300

// segMember — постройка — часть участка трубы.
func (l *Layout) segMember(name string) bool {
	switch l.P.Entities[name].Type {
	case "pipe", "pipe-to-ground", "storage-tank":
		return true
	}
	return false
}

// pipeNet — участки труб раскладки: члены, соседи по соединениям, подземные пары, источники.
type pipeNet struct {
	l     *Layout
	mem   []int         // члены (номера в l.List)
	adj   map[int][]int // соседи по наземным соединениям
	links [][2]int      // подземные пары: вход → выход (по ходу от входа к выходу)
	src   map[int]int   // член → сколько источников жидкости в него выдают
}

func (l *Layout) pipeNet() *pipeNet {
	n := &pipeNet{l: l, adj: map[int][]int{}, src: map[int]int{}}
	type end struct {
		i int
		c Conn
	}
	byAt := map[world.Tile][]end{}
	at := map[world.Tile]int{}
	for i, pl := range l.List {
		cs := Connections(l.P, pl)
		for _, c := range cs {
			byAt[c.At] = append(byAt[c.At], end{i, c})
		}
		if l.segMember(pl.Name) {
			n.mem = append(n.mem, i)
			at[world.TileOf(pl.Pos)] = i
		}
	}
	// source — постройка j выдаёт жидкость в соединение c (помпа, качалка, насос; машина — если
	// у её рецепта есть жидкий продукт)
	source := func(j int, c Conn) bool {
		if c.Flow != "output" {
			return false
		}
		pl := l.List[j]
		switch l.P.Entities[pl.Name].Type {
		case "assembling-machine", "furnace":
			r, ok := l.P.Recipes[pl.Recipe]
			return ok && slices.ContainsFunc(r.Products, func(a knowledge.Amount) bool { _, fl := l.P.Fluids[a.Name]; return fl })
		}
		return true
	}
	for _, i := range n.mem {
		pl := l.List[i]
		for _, a := range Connections(l.P, pl) {
			for _, b := range byAt[a.To] {
				if b.i == i || !Joined(a, b.c) || a.Filter != "" && b.c.Filter != "" && a.Filter != b.c.Filter {
					continue
				}
				if l.segMember(l.List[b.i].Name) {
					n.adj[i] = append(n.adj[i], b.i)
				} else if source(b.i, b.c) {
					n.src[i]++
				}
			}
		}
		// подземная пара (как в проверке течения, flow.pipes): под землёй — против своего
		// направления, до подземки, смотрящей навстречу; каждая пара — один раз (i < j)
		e := l.P.Entities[pl.Name]
		if e.Type != "pipe-to-ground" {
			continue
		}
		d := (pl.Dir + 8) % 16
		cur := world.TileOf(pl.Pos)
		for k := 1; k <= e.MaxUndergroundDistance+1; k++ {
			cur = Step(cur, d)
			if j, ok := at[cur]; ok && l.List[j].Name == pl.Name && l.List[j].Dir == d {
				if i < j {
					n.links = append(n.links, [2]int{i, j})
				}
				break
			}
		}
	}
	return n
}

// parts — участки: член → корень; skip — номер подземной пары, которой нет (-1 — все есть).
func (n *pipeNet) parts(skip int) map[int]int {
	parent := map[int]int{}
	for _, i := range n.mem {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i, js := range n.adj {
		for _, j := range js {
			parent[find(i)] = find(j)
		}
	}
	for k, p := range n.links {
		if k != skip {
			parent[find(p[0])] = find(p[1])
		}
	}
	res := map[int]int{}
	for _, i := range n.mem {
		res[i] = find(i)
	}
	return res
}

// box — рамка тайлов участка root (и тайлов extra): мин и макс.
func (n *pipeNet) box(part map[int]int, root int, extra ...world.Tile) (lo, hi world.Tile) {
	first := true
	add := func(t world.Tile) {
		if first {
			lo, hi, first = t, t, false
			return
		}
		lo = world.Tile{X: min(lo.X, t.X), Y: min(lo.Y, t.Y)}
		hi = world.Tile{X: max(hi.X, t.X), Y: max(hi.Y, t.Y)}
	}
	for _, i := range n.mem {
		if part[i] == root {
			for _, t := range Tiles(n.l.P, n.l.List[i]) {
				add(t)
			}
		}
	}
	for _, t := range extra {
		add(t)
	}
	return lo, hi
}

func extent(lo, hi world.Tile) int { return max(hi.X-lo.X+1, hi.Y-lo.Y+1) }

// WideSegments — участки шире limit: «участок трубы (тег) W×H у (x, y)».
func (l *Layout) WideSegments(limit int) []string {
	n := l.pipeNet()
	part := n.parts(-1)
	seen := map[int]bool{}
	var res []string
	for _, i := range n.mem {
		r := part[i]
		if seen[r] {
			continue
		}
		seen[r] = true
		if lo, hi := n.box(part, r); extent(lo, hi) > limit {
			res = append(res, fmt.Sprintf("участок трубы (%s) %d×%d у %v–%v шире %d", l.List[r].Tag, hi.X-lo.X+1, hi.Y-lo.Y+1, lo, hi, limit))
		}
	}
	return res
}

// SplitWide — насосы на подземных пролётах, пока участки шире PipeExtent (cluster.md §5): над
// пролётом на 4 свободных тайлах подряд — выход подземки, насос, вход подземки; пролёт — тот,
// после которого большая из частей меньше всего; насос — от части с источниками (больше
// источников) к другой. ok — теги труб, где насос можно ставить (строит стадия этих труб).
// Не разрезать участок шире PipeExtentGame — ошибка.
func (l *Layout) SplitWide(ok func(tag string) bool) ([]Placement, error) {
	const ptg, pump, pole = "pipe-to-ground", "pump", "small-electric-pole"
	var added []Placement
	for range 64 {
		n := l.pipeNet()
		part := n.parts(-1)
		// самый широкий участок
		root, wide := -1, 0
		seen := map[int]bool{}
		for _, i := range n.mem {
			r := part[i]
			if seen[r] {
				continue
			}
			seen[r] = true
			if lo, hi := n.box(part, r); extent(lo, hi) > PipeExtent && extent(lo, hi) > wide {
				root, wide = r, extent(lo, hi)
			}
		}
		if root < 0 {
			return added, nil
		}
		type cut struct {
			score            int
			q1, p, q4, power Placement
			valid            bool
		}
		// столб насосу: свободный тайл, откуда малый столб его питает (сеть — столбами стадии)
		poleFor := func(p Placement, busy [4]world.Tile, tag string) (Placement, bool) {
			r := int(l.P.Entities[pole].SupplyAreaDistance)
			for _, pt := range Tiles(l.P, p) {
				for dy := -r; dy <= r; dy++ {
					for dx := -r; dx <= r; dx++ {
						t := world.Tile{X: pt.X + dx, Y: pt.Y + dy}
						pl := Placement{Name: pole, Pos: t.Center(), Tag: tag}
						if !slices.Contains(busy[:], t) && l.CanPlace(pl, AnyLand) && !l.Solid[t] {
							return pl, true
						}
					}
				}
			}
			return Placement{}, false
		}
		best := cut{score: wide}
		for k, lk := range n.links {
			a, b := l.List[lk[0]], l.List[lk[1]]
			if part[lk[0]] != root || !ok(a.Tag) {
				continue
			}
			ta, tb := world.TileOf(a.Pos), world.TileOf(b.Pos)
			d := (a.Dir + 8) % 16 // ход под землёй от a к b
			dist := abs(tb.X-ta.X) + abs(tb.Y-ta.Y)
			if dist < 5 {
				continue
			}
			sub := n.parts(k)
			ra, rb := sub[lk[0]], sub[lk[1]]
			if ra == rb { // пролёт — не единственная связь (кольцо): насос не делит
				continue
			}
			srcA, srcB := 0, 0
			for _, i := range n.mem {
				switch sub[i] {
				case ra:
					srcA += n.src[i]
				case rb:
					srcB += n.src[i]
				}
			}
			for s := 1; s+3 < dist; s++ {
				var ts [4]world.Tile
				free := true
				for m := range 4 {
					ts[m] = ta
					for range s + m {
						ts[m] = Step(ts[m], d)
					}
					free = free && l.Free(ts[m], AnyLand) && !l.Solid[ts[m]]
				}
				if !free {
					continue
				}
				loA, hiA := n.box(sub, ra, ts[0])
				loB, hiB := n.box(sub, rb, ts[3])
				score := max(extent(loA, hiA), extent(loB, hiB))
				if score >= best.score {
					continue
				}
				fd := d // поток — от части a к части b
				if srcB > srcA {
					fd = (d + 8) % 16
				}
				lt := world.Tile{X: min(ts[1].X, ts[2].X), Y: min(ts[1].Y, ts[2].Y)}
				p := Placement{Name: pump, Pos: CenterAt(l.P, pump, fd, lt), Dir: fd, Tag: a.Tag}
				power, ok := poleFor(p, ts, a.Tag)
				if !ok {
					continue
				}
				best = cut{
					score: score,
					q1:    Placement{Name: ptg, Pos: ts[0].Center(), Dir: d, Tag: a.Tag},
					p:     p,
					q4:    Placement{Name: ptg, Pos: ts[3].Center(), Dir: (d + 8) % 16, Tag: a.Tag},
					power: power,
					valid: true,
				}
			}
		}
		if !best.valid {
			if wide > PipeExtentGame {
				lo, hi := n.box(part, root)
				return added, fmt.Errorf("участок трубы (%s) %d×%d у %v–%v шире %d: насос не встал (нет пролёта подземки с 4 свободными тайлами)", l.List[root].Tag, hi.X-lo.X+1, hi.Y-lo.Y+1, lo, hi, PipeExtentGame)
			}
			return added, nil
		}
		for _, pl := range []Placement{best.q1, best.p, best.q4, best.power} {
			l.Add(pl)
			added = append(added, pl)
		}
	}
	return added, fmt.Errorf("участки труб: насосов больше 64")
}
