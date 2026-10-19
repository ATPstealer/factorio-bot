package layout

import (
	"fmt"
	"sort"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// BlueScience — блок синей науки (8.8) прямой подачей; lt — левый верхний угол, ширина 23,
// высота 11. Сверху лента пластин на восток (железо и медь по полосам). Ряд 1 (y+2..4):
// кабель → зелёные схемы → красные схемы ← кабель, шестерни, трубы. Ряд 2 (y+6..8):
// синяя наука под красными схемами (схемы — сверху), двигатели под шестернями (шестерни —
// сверху, трубы — через сундук справа, сталь — из сундука снизу), двигатели в синюю — через
// сундук слева. Сундуки пластика, серы и стали наполняет бот (Placement.Recipe — что в нём
// держать). Синяя — на ленту из-под блока (Out).
func (l *Layout) BlueScience(lt world.Tile, asm, ins, tag string) (Module, error) {
	m := Module{Name: "синяя наука", LT: lt, RB: world.Tile{X: lt.X + 22, Y: lt.Y + 10}, Electric: true}
	at := func(dx, dy int) world.Tile { return world.Tile{X: lt.X + dx, Y: lt.Y + dy} }
	var pls []Placement
	machine := func(dx, dy int, recipe string) {
		pls = append(pls, Placement{Name: asm, Pos: CenterAt(l.P, asm, N, at(dx, dy)), Recipe: recipe, Tag: tag})
	}
	inserter := func(x, y, fx, fy int) {
		a := at(x, y)
		pls = append(pls, Placement{Name: ins, Pos: a.Center(), Dir: InserterDir(l.P, ins, a, at(fx, fy)), Tag: tag})
	}
	chest := func(x, y int, keep string) {
		pls = append(pls, Placement{Name: "wooden-chest", Pos: at(x, y).Center(), Recipe: keep, Tag: tag})
	}
	// ряд 1 и подача с ленты пластин
	machine(0, 2, "copper-cable")
	machine(4, 2, "electronic-circuit")
	machine(8, 2, "advanced-circuit")
	machine(12, 2, "copper-cable")
	machine(16, 2, "iron-gear-wheel")
	machine(20, 2, "pipe")
	for _, x := range []int{1, 5, 13, 17, 21} {
		inserter(x, 1, x, 0)
	}
	inserter(3, 3, 2, 3)   // кабель → зелёные
	inserter(7, 3, 6, 3)   // зелёные → красные
	inserter(11, 3, 12, 3) // кабель → красные
	// пластик в красные — снизу из сундука
	inserter(8, 5, 8, 6)
	chest(8, 6, "plastic-bar")
	// ряд 2: синяя под красными, двигатели под шестернями
	machine(10, 6, "chemical-science-pack")
	machine(16, 6, "engine-unit")
	inserter(10, 5, 10, 4) // красные → синяя
	inserter(17, 5, 17, 4) // шестерни → двигатели
	inserter(20, 5, 20, 4) // трубы → сундук труб
	chest(20, 6, "")
	inserter(19, 6, 20, 6) // сундук труб → двигатели
	inserter(17, 9, 17, 10)
	chest(17, 10, "steel-plate")
	inserter(15, 7, 16, 7) // двигатели → сундук
	chest(14, 7, "")
	inserter(13, 7, 14, 7) // сундук → синяя
	inserter(11, 9, 11, 10)
	chest(11, 10, "sulfur")
	inserter(12, 9, 12, 8) // синяя → лента
	for _, p := range pls {
		if !l.CanPlace(p, OffOre) {
			return Module{}, fmt.Errorf("layout: под синюю науку занято (%s)", p)
		}
	}
	for _, p := range pls {
		l.Add(p)
	}
	for x := 0; x <= 22; x++ {
		l.Add(Placement{Name: "transport-belt", Pos: at(x, 0).Center(), Dir: E, Tag: tag})
	}
	l.AllowEnd(at(23, 0))
	l.Reserved[at(23, 0)] = true // тупик ленты пластин
	m.In = []Port{{at(0, 0), E}}
	m.Out = []Port{{at(12, 10), S}} // сюда инсертер кладёт синюю: начало ленты к лабораториям
	return m, nil
}

// BlueFactory — синяя наука (8.8) у базы: ряды плавки железа и меди (SmeltRow), блок
// BlueScience, лента пластин к нему, синяя — второй лентой над лентой лабораторий (labs —
// лаборатории фабрики: длинноруким через ленту в каждую). Столбы — к Anchors.
func (l *Layout) BlueFactory(from bridge.Pos, labs []Placement, ironPairs, copperPairs int) error {
	drill, furnace, ins, asm := "electric-mining-drill", "stone-furnace", "inserter", "assembling-machine-2"
	if !l.usable(asm) {
		asm = "assembling-machine-1"
	}
	near := world.TileOf(from)
	// синяя — своей лентой вдоль лабораторий, по инсертеру в каждую, до какой дотянуться
	if len(labs) == 0 {
		return fmt.Errorf("layout: нет лабораторий")
	}
	entry, entryDir, err := l.labsFeed(labs)
	if err != nil {
		return err
	}
	l.Reserved[entry] = true

	ironOut, err := l.smeltRowNearest("iron-ore", from, ironPairs, drill, furnace, ins, "blue-iron")
	if err != nil {
		return err
	}
	l.Reserved[Step(ironOut.T, E)] = true
	copOut, err := l.smeltRowNearest("copper-ore", from, copperPairs, drill, furnace, ins, "blue-copper")
	if err != nil {
		return err
	}
	l.Reserved[Step(copOut.T, E)] = true
	lt, ok := l.FindArea(near, 23, 11, 2, 100, OffOre)
	if !ok {
		return fmt.Errorf("layout: нет места под блок синей науки")
	}
	b, err := l.BlueScience(lt, asm, ins, "blue")
	if err != nil {
		return err
	}
	// медь — сбоку сверху на ленту пластин (левая полоса; железо от ряда — правая)
	copSide := world.Tile{X: b.In[0].T.X + 1, Y: b.In[0].T.Y - 1}
	l.Reserved[copSide] = true
	delete(l.Reserved, Step(ironOut.T, E))
	if _, err := l.RouteBelt(Step(ironOut.T, E), E, Step(b.In[0].T, W), E, "blue-belt-iron"); err != nil {
		return fmt.Errorf("железо → синяя: %w", err)
	}
	delete(l.Reserved, copSide)
	delete(l.Reserved, Step(copOut.T, E))
	if _, err := l.RouteBelt(Step(copOut.T, E), E, copSide, S, "blue-belt-copper"); err != nil {
		return fmt.Errorf("медь → синяя: %w", err)
	}
	delete(l.Reserved, entry)
	if _, err := l.RouteBelt(b.Out[0].T, S, entry, entryDir, "blue-belt-science"); err != nil {
		return fmt.Errorf("синяя → лаборатории: %w", err)
	}
	l.Poles("small-electric-pole", "blue-power")
	return nil
}

// smeltRowNearest — ряд плавки на ближайшем к from месторождении ore, где он помещается
// (ближнее занято рядами прежних фабрик).
func (l *Layout) smeltRowNearest(ore string, from bridge.Pos, pairs int, drill, furnace, ins, tag string) (Port, error) {
	ps := l.W.Patches()
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Distance(from) < ps[j].Distance(from) })
	err := fmt.Errorf("layout: не видно %s", ore)
	for _, p := range ps {
		if p.Name != ore || len(p.Tiles) < 20 {
			continue
		}
		out, e := l.SmeltRow(p.Tiles, ore, pairs, drill, furnace, ins, world.TileOf(from), tag)
		if e == nil {
			return out, nil
		}
		err = e
	}
	return Port{}, err
}

// labsFeed — лента синей вдоль ряда лабораторий (Labs: лента y, инсертеры y+1, лаборатории
// y+2..y+4) и по инсертеру в каждую, до какой дотянуться: над лентой лабораторий (ряд y-1,
// длиннорукий через ленту) или под лабораториями (ряд y+6, обычный). Ряд бывает занят
// чужими лентами — берётся самый длинный свободный отрезок с наибольшим числом лабораторий.
// Возвращает тайл, куда подвести ленту, и её направление там.
func (l *Layout) labsFeed(labs []Placement) (world.Tile, int, error) {
	minX, maxX, top, bottom := 1<<30, -(1 << 30), 1<<30, -(1 << 30)
	var lxs []int
	for _, lab := range labs {
		lx := 1 << 30
		for _, t := range Tiles(l.P, lab) {
			minX, maxX, top, bottom = min(minX, t.X), max(maxX, t.X), min(top, t.Y), max(bottom, t.Y)
			lx = min(lx, t.X)
		}
		lxs = append(lxs, lx)
	}
	type variant struct {
		ins, row, side int // ряд инсертеров, ряд ленты, откуда входит лента (N — сверху)
		name           string
		xs             func(lx int) []int
	}
	vars := []variant{
		{top - 1, top - 3, N, "long-handed-inserter", func(lx int) []int { return []int{lx + 2, lx} }},
		{bottom + 1, bottom + 2, S, "inserter", func(lx int) []int { return []int{lx + 1, lx, lx + 2} }},
	}
	var best []Placement
	var bestEntry world.Tile
	bestDir := 0
	for _, v := range vars {
		if !l.usable(v.name) {
			continue
		}
		ok := func(x int) bool {
			t := world.Tile{X: x, Y: v.row}
			return l.Free(t, AnyLand) && !l.beltInto(t)
		}
		// свободные отрезки ряда
		for x0 := minX - 1; x0 <= maxX; {
			if !ok(x0) {
				x0++
				continue
			}
			x1 := x0
			for x1+1 <= maxX+1 && ok(x1+1) {
				x1++
			}
			// лента x0..x1-1, x1 — пустой тайл за её концом (иначе упрётся в соседа и
			// сольёт синюю в чужую ленту)
			end := x1 - 1
			var pls []Placement
			for _, lx := range lxs {
				for _, x := range v.xs(lx) {
					a := world.Tile{X: x, Y: v.ins}
					if x >= x0 && x <= end && l.Free(a, AnyLand) {
						pls = append(pls, Placement{Name: v.name, Pos: a.Center(), Dir: InserterDir(l.P, v.name, a, world.Tile{X: x, Y: v.row}), Tag: "blue-labs"})
						break
					}
				}
			}
			entry := Step(world.Tile{X: x0, Y: v.row}, v.side)
			if len(pls) > 0 && end >= x0 && l.Free(entry, AnyLand) && (len(pls) > countIns(best) || len(pls) == countIns(best) && end-x0+1 > len(best)-countIns(best)) {
				best = pls
				for x := x0; x <= end; x++ {
					best = append(best, Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: v.row}.Center(), Dir: E, Tag: "blue-labs"})
				}
				bestEntry, bestDir = entry, (v.side+8)%16
			}
			x0 = x1 + 1
		}
	}
	if len(best) == 0 {
		return world.Tile{}, 0, fmt.Errorf("layout: ни к одной лаборатории не подвести синюю")
	}
	end := world.Tile{}
	for _, p := range best {
		l.Add(p)
		if isBelt(p.Name) {
			end = world.TileOf(p.Pos)
		}
	}
	l.AllowEnd(Step(end, E))
	l.Reserved[Step(end, E)] = true
	return bestEntry, bestDir, nil
}

// countIns — сколько инсертеров в размещениях.
func countIns(pls []Placement) int {
	n := 0
	for _, p := range pls {
		if !isBelt(p.Name) {
			n++
		}
	}
	return n
}
