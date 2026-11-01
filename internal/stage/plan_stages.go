package stage

import (
	"slices"
	"strconv"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадии построек плана всей базы (stages.md: строки «Ставит», «Ленты и трубы шин — вместе с
// кластерами»): постройка кластера, его логистика и части — стадия кластера (блок добычи с тегом
// «later» — стадия 30, часть блока лабораторий — стадия своего пакета); лента или труба шины —
// стадия первого, кто ей пользуется (поставщик или потребитель), лента добытого — стадия блока,
// чей подвод в неё входит; вода химии — стадия 35, насосы у склада нефти — стадия склада (33),
// труба нефти bus-1 — стадия НПЗ (качалки кладут нефть в склад, не в шину).

// Стадии частей блока лабораторий по пакетам (stages.md, 21, 27, 41, 48, 61).
var labPackStage = map[string]int{redPack: 21, greenPack: 27, bluePack: 41, purplePack: 48, yellowPack: 61}

// Особые стадии: пластины на шину — стадия 18 (стадия 17 копит их в буферах выхода); кирпич на
// шину — со своим потребителем (стадия 30 — только в буфер выхода, stages.md); вода химии —
// своя стадия 35.
const (
	platesOnBus = 18
	waterStage  = 35
)

// onBusWithUser — продукт, чья лента шины — со стадией первого потребителя, а не поставщика.
var onBusWithUser = map[string]bool{"stone-brick": true}

// stageMap — стадия каждой постройки плана.
type stageMap map[layout.Placement]int

// busKey — предмет на шине k (0 — bus-1).
type busKey struct {
	k    int
	item string
}

// planStages — стадии построек плана s.
func planStages(s *ClusterPlan) stageMap {
	by := map[string]*cluster.Cluster{}
	for _, c := range s.Clusters {
		by[c.Name] = c
	}
	// стадия предмета на шине k: первый, кто кладёт его в эту шину (этаж под ней, добыча — в
	// bus-1) или берёт с неё (этаж над ней; сборка и лаборатории — и с шины над собой)
	itemStage := map[busKey]int{}
	use := func(k int, it string, st int) {
		if v, ok := itemStage[busKey{k, it}]; !ok || st < v {
			itemStage[busKey{k, it}] = st
		}
	}
	fluidBus := func(it string) (int, bool) { k, ok := cluster.FluidBus[it]; return k, ok }
	for _, c := range s.Clusters {
		if c.Stage == 0 && c.Kind != cluster.Mine || c.Kind == cluster.Tank || c.Kind == cluster.Bus {
			continue
		}
		f := cluster.FloorOf(c)
		for _, o := range c.Out {
			if c.IsOil() {
				break // нефть — трассой в склад; труба нефти bus-1 — от склада, с НПЗ (стадия 36)
			}
			st, k := clusterStage(c), f
			if c.Kind == cluster.Mine {
				k = 0
			}
			if kf, ok := fluidBus(o.Item); ok {
				k = kf
			}
			if c.Kind == cluster.Smelt && (o.Item == "iron-plate" || o.Item == "copper-plate") {
				st = max(st, platesOnBus)
			}
			if onBusWithUser[o.Item] {
				continue
			}
			use(k, o.Item, st)
		}
		for _, in := range c.In {
			st := clusterStage(c)
			if c.Kind == cluster.Lab {
				if ps, ok := labPackStage[in.Item]; ok {
					st = ps
				}
			}
			if kf, ok := fluidBus(in.Item); ok {
				use(kf, in.Item, st)
				continue
			}
			if f > 0 {
				use(f-1, in.Item, st) // с шины под этажом
			}
			if c.TakesAbove() && f > 0 {
				use(f, in.Item, st) // и с шины над ним
			}
		}
	}
	use(0, "water", waterStage)
	// сквозная лента с шины from на шину to — стадия ленты на to; лента на from — не позже неё
	// (медь красной науки: сквозная bus-2 → bus-3 — стадия 20, а лента меди bus-2 — только со
	// схемами, 23 — на бумаге до красной медь не доходила, 267145981); цепочки — сверху вниз
	busByName := map[string]int{}
	for _, c := range s.Clusters {
		if c.Kind == cluster.Bus {
			n, _ := strconv.Atoi(strings.TrimPrefix(c.Name, "bus-"))
			busByName[c.Name] = n - 1
		}
	}
	type through struct {
		to, from int
		item     string
	}
	var throughs []through
	seen := map[through]bool{}
	for _, pl := range s.Layout {
		name, ok := strings.CutPrefix(pl.Tag, cluster.LogisticsTag+"/")
		if !ok {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) != 3 {
			continue
		}
		to, ok1 := busByName[parts[0]]
		from, ok2 := busByName[parts[1]]
		if t := (through{to, from, parts[2]}); ok1 && ok2 && !seen[t] {
			seen[t] = true
			throughs = append(throughs, t)
		}
	}
	slices.SortFunc(throughs, func(a, b through) int { return b.to - a.to })
	for _, t := range throughs {
		if st, ok := itemStage[busKey{t.to, t.item}]; ok {
			use(t.from, t.item, st)
		}
	}
	// лента добытого в bus-1 — стадия блока, чей подвод входит в неё (у головы ряда)
	feederAt := map[world.Tile]int{}
	for _, pl := range s.Layout {
		if name, ok := strings.CutPrefix(pl.Tag, cluster.LogisticsTag+"/"); ok {
			if i := strings.LastIndex(name, "/"); i >= 0 {
				if c := by[name[i+1:]]; c != nil && c.Kind == cluster.Mine {
					feederAt[world.TileOf(pl.Pos)] = mineStage(c)
				}
			}
		}
	}
	rowStage := map[string]int{}   // «шина/y» → стадия ряда (лента добытого)
	rowItem := map[string]string{} // «шина/y» → предмет ленты ряда (делители и подземки — без него)
	for _, pl := range s.Layout {
		if !strings.HasPrefix(pl.Tag, "c/bus-") || strings.Count(pl.Tag, "/") != 1 || pl.Recipe == "" {
			continue
		}
		t := world.TileOf(pl.Pos)
		if strings.HasSuffix(pl.Name, "transport-belt") {
			rowItem[pl.Tag+"/"+strconv.Itoa(t.Y)] = pl.Recipe
		}
		for _, n := range []world.Tile{{X: t.X - 1, Y: t.Y}, {X: t.X, Y: t.Y - 1}, {X: t.X, Y: t.Y + 1}} {
			if st, ok := feederAt[n]; ok {
				key := pl.Tag + "/" + strconv.Itoa(t.Y)
				if v, had := rowStage[key]; !had || st < v {
					rowStage[key] = st
				}
			}
		}
	}
	tank := map[world.Tile]bool{} // у склада нефти (насосы на входе и выходе — его стадия)
	tankStage := 0
	for _, c := range s.Clusters {
		if c.Kind == cluster.Tank {
			tankStage = c.Stage
			g := c.Area.Grow(3)
			for y := g.Min.Y; y < g.Max.Y; y++ {
				for x := g.Min.X; x < g.Max.X; x++ {
					tank[world.Tile{X: x, Y: y}] = true
				}
			}
		}
	}
	// уголь станции лентой — со своим угольным блоком (стадия 16; до неё — сундук у бойлера,
	// stages.md, стадия 11): лента и инсертеры ряда, буфер угля, лента от блока
	coalStage := 0
	power := map[string]bool{}
	for _, c := range s.Clusters {
		switch {
		case c.Kind == cluster.Mine && c.ToPower:
			coalStage = clusterStage(c)
		case c.Kind == cluster.Power:
			power[cluster.Tag(c.Name)], power[cluster.LogisticsTag+"/"+c.Name] = true, true
		}
	}
	coalPart := func(pl layout.Placement) bool {
		return power[pl.Tag] && (strings.HasSuffix(pl.Name, "inserter") || strings.HasSuffix(pl.Name, "chest") ||
			strings.HasSuffix(pl.Name, "belt") || strings.HasSuffix(pl.Name, "splitter"))
	}
	res := stageMap{}
	for _, pl := range s.Layout {
		res[pl] = stageOf(pl, by, itemStage, rowStage, rowItem)
		switch {
		case pl.Name == "pump" && tank[world.TileOf(pl.Pos)] && tankStage > 0:
			res[pl] = tankStage
		case coalStage > 0 && coalPart(pl):
			res[pl] = coalStage
		}
	}
	return res
}

// mineStage — стадия блока добычи (нефть — 34).
func mineStage(c *cluster.Cluster) int {
	if c.Stage > 0 {
		return c.Stage
	}
	if c.IsOil() {
		return 34
	}
	return 30
}

// stageOf — стадия постройки pl по её тегу (0 — неизвестно).
func stageOf(pl layout.Placement, by map[string]*cluster.Cluster, itemStage map[busKey]int, rowStage map[string]int, rowItem map[string]string) int {
	tag := pl.Tag
	busOf := func(name string) int { // bus-N → N−1
		n, _ := strconv.Atoi(strings.TrimPrefix(name, "bus-"))
		return n - 1
	}
	if name, ok := strings.CutPrefix(tag, cluster.LogisticsTag+"/"); ok { // логистика кластера
		parts := strings.Split(name, "/")
		isBus := func(s string) bool { c := by[s]; return c != nil && c.Kind == cluster.Bus }
		if len(parts) == 3 && isBus(parts[0]) && isBus(parts[1]) { // сквозная: шина/шина/предмет —
			return itemStage[busKey{busOf(parts[0]), parts[2]}] // стадия ленты наверху
		}
		if c := by[parts[0]]; len(parts) == 2 && c != nil && c.Kind == cluster.Lab { // отвод пакета
			if st, ok := labPackStage[parts[1]]; ok {
				return st
			}
		}
		for i := len(parts) - 1; i >= 0; i-- { // подвод добычи: logistics/<шина>/<блок>
			if c := by[parts[i]]; c != nil && c.Kind != cluster.Bus {
				return clusterStage(c)
			}
		}
		return 0
	}
	name, ok := strings.CutPrefix(tag, "c/")
	if !ok {
		return 0
	}
	parts := strings.Split(name, "/")
	c := by[parts[0]]
	if c == nil {
		return 0
	}
	if c.Kind == cluster.Bus { // лента или труба шины
		k := busOf(c.Name)
		if len(parts) > 1 { // труба жидкости parts[1] (её насосы и столбы — тоже)
			return itemStage[busKey{k, parts[1]}]
		}
		row := tag + "/" + strconv.Itoa(world.TileOf(pl.Pos).Y)
		if st, ok := rowStage[row]; ok {
			return st
		}
		it := pl.Recipe
		if !strings.HasSuffix(pl.Name, "transport-belt") { // делитель, подземка — по ленте своего ряда
			it = rowItem[row]
		}
		return itemStage[busKey{k, it}]
	}
	if len(parts) > 1 {
		switch {
		case parts[1] == "later":
			return 30
		case c.Kind == cluster.Lab:
			if st, ok := labPackStage[parts[1]]; ok {
				return st
			}
		}
	}
	return clusterStage(c)
}

// clusterStage — стадия кластера (добыча без стадии — 30, нефть — 34).
func clusterStage(c *cluster.Cluster) int {
	if c.Kind == cluster.Mine {
		return mineStage(c)
	}
	return c.Stage
}

// planBefore — чертёж плана к стадии n: постройки плана стадий до n (обслуживание сверяет с ним
// построенное).
func planBefore(n int) func(*act.Hands) ([]layout.Placement, error) {
	return func(*act.Hands) ([]layout.Placement, error) {
		plan, err := LoadPlan()
		if err != nil {
			return nil, err
		}
		return stageAdded(plan, 1, n-1), nil
	}
}

// stageAdded — постройки плана стадий from…to (их строят эти стадии).
func stageAdded(s *ClusterPlan, from, to int) []layout.Placement {
	st := planStages(s)
	var res []layout.Placement
	for _, pl := range s.Layout {
		if n := st[pl]; n >= from && n <= to {
			res = append(res, pl)
		}
	}
	return res
}
