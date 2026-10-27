package layout

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

// Flow — проверка течения по плану без игры (просьба пользователя 27.10): откуда что берётся
// (буры — руда под ними, помпы — вода, качалки — нефть, печи и сборщики — продукты рецептов) и
// куда доходит по лентам (с подземками, делителями, боковой подачей), инсертерам (с фильтром),
// ящикам и трубам (подземные пары, насосы, цистерны, жидкостные ящики машин). Возвращает, у
// каких машин нет входа: печь без руды или топлива, сборщик без ингредиента, бойлер без воды
// или угля, двигатель без пара, лаборатория без пакетов, НПЗ и химзавод без жидкости. skip —
// постройки, которых ещё нет (будущие блоки добычи): не считаются ни источниками, ни путями.
// Так нашлось: печи плавки брали с ленты, которую кормила только будущая добыча (1028292478,
// стадия 17).
func (l *Layout) Flow(skip func(pl Placement) bool) []string {
	errs, _ := l.FlowTrace(skip)
	return errs
}

// FlowTrace — Flow и вдобавок, что куда дотекло: предметы по тайлам лент и ящиков и по машинам
// (центр машины) — для разбора (bin/bot flow).
func (l *Layout) FlowTrace(skip func(pl Placement) bool) ([]string, map[world.Tile][]string) {
	f := &flow{l: l, p: l.P, items: map[world.Tile]set{}, machine: map[int]set{}, net: map[int]int{}, netFluids: map[int]set{}}
	for i, pl := range l.List {
		if skip != nil && skip(pl) {
			continue
		}
		f.list = append(f.list, i)
	}
	f.index()
	f.pipes()
	for changed := true; changed; {
		changed = false
		for _, i := range f.list {
			if f.step(i) {
				changed = true
			}
		}
	}
	trace := map[world.Tile][]string{}
	for t, s := range f.items {
		trace[t] = slices.Sorted(mapsKeys(s))
	}
	for i, s := range f.machine {
		t := world.TileOf(f.l.List[i].Pos)
		trace[t] = append(trace[t], slices.Sorted(mapsKeys(s))...)
	}
	for i, r := range f.net { // жидкости — у всего трубного (и машин с жидкостными ящиками)
		t := world.TileOf(f.l.List[i].Pos)
		trace[t] = append(trace[t], fmt.Sprintf("сеть %d:", r))
		trace[t] = append(trace[t], slices.Sorted(mapsKeys(f.netFluids[r]))...)
	}
	return f.report(), trace
}

type set map[string]bool

func (s set) add(items ...string) bool {
	changed := false
	for _, it := range items {
		if it != "" && !s[it] {
			s[it] = true
			changed = true
		}
	}
	return changed
}

type flow struct {
	l    *Layout
	p    *knowledge.Prototypes
	list []int              // индексы учтённых размещений
	at   map[world.Tile]int // тайл → индекс (учтённые)
	// items — что лежит на тайле ленты / в ящике / выход бура (по тайлу)
	items map[world.Tile]set
	// machine — что пришло в машину (индекс → предметы и жидкости)
	machine map[int]set
	// трубы: сеть — по союзу соединений
	net       map[int]int // индекс трубного размещения → сеть
	netFluids map[int]set // сеть → жидкости
	ugOut     map[int]int // вход подземной ленты → индекс выхода
}

func (f *flow) index() {
	f.at = map[world.Tile]int{}
	for _, i := range f.list {
		for _, t := range Tiles(f.p, f.l.List[i]) {
			f.at[t] = i
		}
	}
	f.ugOut = map[int]int{}
	for _, i := range f.list {
		pl := f.l.List[i]
		if !isUnderground(pl.Name) || pl.Recipe != "input" {
			continue
		}
		cur := world.TileOf(pl.Pos)
		for k := 1; k <= f.p.Entities[pl.Name].MaxUndergroundDistance; k++ {
			cur = Step(cur, pl.Dir)
			if j, ok := f.at[cur]; ok {
				n := f.l.List[j]
				if isUnderground(n.Name) && n.Recipe == "output" && n.Dir == pl.Dir {
					f.ugOut[i] = j
					break
				}
			}
		}
	}
}

// pipes — трубные сети: союз по соединениям (Joined) и подземным парам; машины с жидкостными
// ящиками — в сети своих соединений.
func (f *flow) pipes() {
	parent := map[int]int{}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	conns := map[int][]Conn{}
	byAt := map[world.Tile][]int{}
	for _, i := range f.list {
		pl := f.l.List[i]
		cs := Connections(f.p, pl)
		if len(cs) == 0 {
			continue
		}
		parent[i] = i
		conns[i] = cs
		for _, c := range cs {
			byAt[c.At] = append(byAt[c.At], i)
		}
	}
	for i, cs := range conns {
		for _, a := range cs {
			for _, j := range byAt[a.To] {
				if j == i {
					continue
				}
				for _, b := range conns[j] {
					if Joined(a, b) && (a.Filter == "" || b.Filter == "" || a.Filter == b.Filter) {
						union(i, j)
					}
				}
			}
		}
		pl := f.l.List[i]
		if e := f.p.Entities[pl.Name]; e.Type == "pipe-to-ground" {
			d := (pl.Dir + 8) % 16
			cur := world.TileOf(pl.Pos)
			for k := 1; k <= e.MaxUndergroundDistance+1; k++ {
				cur = Step(cur, d)
				if j, ok := f.at[cur]; ok {
					if n := f.l.List[j]; n.Name == pl.Name && n.Dir == d {
						union(i, j)
						break
					}
					// поверху — что угодно (ленты поперёк): под ними и идёт труба
				}
			}
		}
	}
	for i := range conns {
		r := find(i)
		f.net[i] = r
		if f.netFluids[r] == nil {
			f.netFluids[r] = set{}
		}
	}
}

// fluidOut — жидкости, которые размещение i отдаёт в сеть; fluidIn — какие ждёт.
func (f *flow) fluidsOf(i int) (out, in []string) {
	pl := f.l.List[i]
	e := f.p.Entities[pl.Name]
	switch e.Type {
	case "offshore-pump":
		return []string{"water"}, nil
	case "mining-drill": // качалка (электрический бур — предметы, step)
		if !slices.Contains(e.ResourceCategories, "basic-fluid") {
			return nil, nil
		}
		if f.l.W != nil {
			if r, ok := f.l.W.ResourceAt(world.TileOf(pl.Pos)); ok {
				if _, fluid := f.p.Fluids[r.Name]; fluid {
					return []string{r.Name}, nil
				}
			}
		}
		return []string{"crude-oil"}, nil
	case "boiler":
		return []string{"steam"}, []string{"water"}
	case "generator":
		return nil, []string{"steam"}
	case "assembling-machine", "furnace":
		if r, ok := f.p.Recipes[pl.Recipe]; ok {
			for _, a := range r.Products {
				if _, fluid := f.p.Fluids[a.Name]; fluid {
					out = append(out, a.Name)
				}
			}
			for _, a := range r.Ingredients {
				if _, fluid := f.p.Fluids[a.Name]; fluid {
					in = append(in, a.Name)
				}
			}
		}
	}
	return out, in
}

// step — один проход по размещению i: передать дальше, что в нём есть. true — что-то изменилось.
func (f *flow) step(i int) bool {
	pl := f.l.List[i]
	e := f.p.Entities[pl.Name]
	t := world.TileOf(pl.Pos)
	changed := false
	switch {
	case isBelt(pl.Name):
		changed = f.pour(t, Step(t, pl.Dir), pl.Dir)
	case isUnderground(pl.Name):
		if pl.Recipe == "input" {
			if j, ok := f.ugOut[i]; ok {
				changed = f.give(world.TileOf(f.l.List[j].Pos), f.items[t])
			}
		} else {
			changed = f.pour(t, Step(t, pl.Dir), pl.Dir)
		}
	case e.Type == "splitter":
		tiles := Tiles(f.p, pl)
		all := set{}
		for _, u := range tiles {
			for it := range f.items[u] {
				all[it] = true
			}
		}
		for _, u := range tiles {
			if f.give(u, all) {
				changed = true
			}
			if f.pour(u, Step(u, pl.Dir), pl.Dir) {
				changed = true
			}
		}
	case e.Type == "inserter":
		pick, drop := InserterEnds(f.p, pl)
		have := f.itemsAt(pick)
		if pl.Recipe != "" {
			have = set{}
			if f.itemsAt(pick)[pl.Recipe] {
				have[pl.Recipe] = true
			}
		}
		changed = f.deliver(drop, have)
	case e.Type == "mining-drill":
		if slices.Contains(e.ResourceCategories, "basic-fluid") {
			break // качалка — жидкость (pipes); у электрического бура тоже есть жидкостный ящик (кислота)
		}
		ore := set{}
		if f.l.W != nil {
			for _, u := range MiningArea(f.p, pl) {
				if r, ok := f.l.W.ResourceAt(u); ok {
					ore[r.Name] = true
				}
			}
		}
		changed = f.deliver(DrillOutput(f.p, pl), ore)
	case e.Type == "furnace", e.Type == "assembling-machine", e.Type == "boiler":
		changed = f.produce(i)
	}
	// жидкости: что машина отдаёт (если у неё есть всё, что нужно) — в свою трубную сеть
	if out, _ := f.fluidsOf(i); len(out) > 0 && f.works(i) {
		if r, ok := f.net[i]; ok && f.netFluids[r].add(out...) {
			changed = true
		}
	}
	return changed
}

// works — машине i хватает входов, чтобы отдавать продукт.
func (f *flow) works(i int) bool {
	pl := f.l.List[i]
	have := f.machine[i]
	switch f.p.Entities[pl.Name].Type {
	case "boiler":
		return (have["coal"] || have["wood"] || have["solid-fuel"]) && f.fluidAt(i, "water")
	case "assembling-machine", "furnace":
		r, ok := f.p.Recipes[pl.Recipe]
		return ok && f.fed(i, r)
	}
	return true // помпа, качалка
}

func (f *flow) itemsAt(t world.Tile) set {
	if s := f.items[t]; s != nil {
		return s
	}
	if j, ok := f.at[t]; ok {
		if m := f.machine[j]; m != nil {
			return f.productsOf(j, m)
		}
	}
	return set{}
}

// productsOf — что из машины j можно взять (продукты рецепта, если ингредиенты пришли; ящик —
// всё содержимое).
func (f *flow) productsOf(j int, have set) set {
	pl := f.l.List[j]
	e := f.p.Entities[pl.Name]
	switch e.Type {
	case "container", "logistic-container":
		return have
	case "lab": // пакеты передаются из лаборатории в лабораторию инсертером (блок лабораторий)
		return have
	case "furnace":
		res := set{}
		for in, out := range map[string]string{"iron-ore": "iron-plate", "copper-ore": "copper-plate", "stone": "stone-brick", "iron-plate": "steel-plate"} {
			if have[in] && (pl.Recipe == "" && in != "iron-plate" || pl.Recipe != "" && f.productOf(pl.Recipe) == out) {
				res[out] = true
			}
		}
		return res
	case "assembling-machine":
		res := set{}
		if r, ok := f.p.Recipes[pl.Recipe]; ok && f.fed(j, r) {
			for _, a := range r.Products {
				if _, fluid := f.p.Fluids[a.Name]; !fluid {
					res[a.Name] = true
				}
			}
		}
		return res
	}
	return set{}
}

func (f *flow) productOf(recipe string) string {
	if r, ok := f.p.Recipes[recipe]; ok && len(r.Products) > 0 {
		return r.Products[0].Name
	}
	return ""
}

// fed — все ингредиенты рецепта пришли в машину j (предметы — инсертерами, жидкости — трубами).
func (f *flow) fed(j int, r knowledge.Recipe) bool {
	have := f.machine[j]
	for _, a := range r.Ingredients {
		if _, fluid := f.p.Fluids[a.Name]; fluid {
			if !f.fluidAt(j, a.Name) {
				return false
			}
		} else if !have[a.Name] {
			return false
		}
	}
	return true
}

// fluidAt — жидкость есть в сети у входного соединения машины j.
func (f *flow) fluidAt(j int, fluid string) bool {
	if r, ok := f.net[j]; ok && f.netFluids[r][fluid] {
		return true
	}
	return false
}

// produce — машина j: отметить, что пришло (ничего не двигает — инсертеры берут productsOf).
func (f *flow) produce(j int) bool { return false }

// pour — лента с t смотрит d на next: передать предметы, если там лента/подземка/делитель не
// навстречу (боковая подача — тоже).
func (f *flow) pour(t, next world.Tile, d int) bool {
	j, ok := f.at[next]
	if !ok {
		return false
	}
	n := f.l.List[j]
	e := f.p.Entities[n.Name]
	switch {
	case isBelt(n.Name), isUnderground(n.Name), e.Type == "splitter":
		if n.Dir == (d+8)%16 {
			return false
		}
		if isUnderground(n.Name) && n.Recipe == "output" && n.Dir != d {
			return false // в выход подземки сбоку не въехать
		}
		return f.give(next, f.items[t])
	}
	return false
}

// give — предметы на тайл ленты.
func (f *flow) give(t world.Tile, items set) bool {
	if len(items) == 0 {
		return false
	}
	if f.items[t] == nil {
		f.items[t] = set{}
	}
	changed := false
	for it := range items {
		if f.items[t].add(it) {
			changed = true
		}
	}
	return changed
}

// deliver — предметы инсертером или буром на тайл t: лента — на ленту, машина/ящик — в неё.
func (f *flow) deliver(t world.Tile, items set) bool {
	if len(items) == 0 {
		return false
	}
	j, ok := f.at[t]
	if !ok {
		return false
	}
	n := f.l.List[j]
	e := f.p.Entities[n.Name]
	if isBelt(n.Name) || isUnderground(n.Name) || e.Type == "splitter" {
		return f.give(t, items)
	}
	if f.machine[j] == nil {
		f.machine[j] = set{}
	}
	changed := false
	for it := range items {
		if f.machine[j].add(it) {
			changed = true
		}
	}
	return changed
}

// report — машины без входа.
func (f *flow) report() []string {
	var res []string
	add := func(pl Placement, what string) {
		res = append(res, fmt.Sprintf("%s (%.1f, %.1f) [%s]: %s", pl.Name, pl.Pos.X, pl.Pos.Y, pl.Tag, what))
	}
	fuel := func(have set) bool { return have["coal"] || have["wood"] || have["solid-fuel"] }
	for _, i := range f.list {
		pl := f.l.List[i]
		e := f.p.Entities[pl.Name]
		have := f.machine[i]
		if have == nil {
			have = set{}
		}
		switch e.Type {
		case "furnace":
			var missing []string
			if !fuel(have) {
				missing = append(missing, "топлива")
			}
			ore := pl.Recipe == "" && (have["iron-ore"] || have["copper-ore"] || have["stone"]) || pl.Recipe != "" && have["iron-plate"]
			if !ore {
				missing = append(missing, "руды")
			}
			if len(missing) > 0 {
				add(pl, "нет "+strings.Join(missing, " и "))
			}
		case "assembling-machine":
			r, ok := f.p.Recipes[pl.Recipe]
			if !ok {
				continue
			}
			var missing []string
			for _, a := range r.Ingredients {
				if _, fluid := f.p.Fluids[a.Name]; fluid {
					if !f.fluidAt(i, a.Name) {
						missing = append(missing, a.Name)
					}
				} else if !have[a.Name] {
					missing = append(missing, a.Name)
				}
			}
			if len(missing) > 0 {
				add(pl, "нет "+strings.Join(missing, ", "))
			}
		case "boiler":
			var missing []string
			if !fuel(have) {
				missing = append(missing, "угля")
			}
			if !f.fluidAt(i, "water") {
				missing = append(missing, "воды")
			}
			if len(missing) > 0 {
				add(pl, "нет "+strings.Join(missing, " и "))
			}
		case "generator":
			if !f.fluidAt(i, "steam") {
				add(pl, "нет пара")
			}
		case "lab":
			if !slices.ContainsFunc(slices.Collect(mapsKeys(have)), func(it string) bool { return strings.HasSuffix(it, "-science-pack") }) {
				add(pl, "нет пакетов")
			}
		}
	}
	sort.Strings(res)
	return res
}

func mapsKeys(s set) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range s {
			if !yield(k) {
				return
			}
		}
	}
}
