package layout

import (
	"fmt"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// GreenScience — блок зелёной науки (8.1) прямой подачей: сверху лента пластин на восток
// (железо и медь по полосам), под ней ряд сборщиков «кабель → схемы → инсертеры ←
// шестерни → ленты». Инсертеры и ленты — на общую ленту (y+7, на запад) по разным полосам
// (иначе одна полоса забьётся одним предметом): инсертеры — через отвод сверху (боковая
// подача → левая полоса), ленты — длинноруким прямо на ленту (дальняя → правая полоса).
// С неё берут n сборщиков науки, наука — на нижнюю ленту (на запад). Ширина 19, высота 14.
func (l *Layout) GreenScience(lt world.Tile, n int, asm, ins, tag string) (Module, error) {
	if n < 1 || n > 4 {
		return Module{}, fmt.Errorf("layout: сборщиков зелёной науки 1–4, не %d", n)
	}
	const long = "long-handed-inserter"
	m := Module{Name: "зелёная наука", LT: lt, RB: world.Tile{X: lt.X + 18, Y: lt.Y + 13}, Electric: true}
	x0, y := lt.X, lt.Y
	add := func(name string, t world.Tile, dir int, recipe string) {
		pl := Placement{Name: name, Dir: dir, Recipe: recipe, Tag: tag}
		if name == asm {
			pl.Pos = CenterAt(l.P, asm, N, t)
		} else {
			pl.Pos = t.Center()
		}
		l.Add(pl)
	}
	inserter := func(name string, at, from world.Tile) {
		add(name, at, InserterDir(l.P, name, at, from), "")
	}
	// ряд сборщиков: левый верхний x и рецепт
	row := []struct {
		x      int
		recipe string
	}{{0, "copper-cable"}, {4, "electronic-circuit"}, {8, "inserter"}, {12, "iron-gear-wheel"}, {16, "transport-belt"}}
	for _, a := range row {
		add(asm, world.Tile{X: x0 + a.x, Y: y + 2}, N, a.recipe)
		inserter(ins, world.Tile{X: x0 + a.x + 1, Y: y + 1}, world.Tile{X: x0 + a.x + 1, Y: y}) // пластины с ленты
	}
	// прямая подача между соседями (средний ряд сборщиков)
	mid := y + 3
	inserter(ins, world.Tile{X: x0 + 3, Y: mid}, world.Tile{X: x0 + 2, Y: mid})   // кабель → схемы
	inserter(ins, world.Tile{X: x0 + 7, Y: mid}, world.Tile{X: x0 + 6, Y: mid})   // схемы → инсертеры
	inserter(ins, world.Tile{X: x0 + 11, Y: mid}, world.Tile{X: x0 + 12, Y: mid}) // шестерни → инсертеры
	inserter(ins, world.Tile{X: x0 + 15, Y: mid}, world.Tile{X: x0 + 14, Y: mid}) // шестерни → ленты
	// инсертеры: на отвод (y+6, на юг) → сбоку на общую ленту — левая полоса
	inserter(ins, world.Tile{X: x0 + 9, Y: y + 5}, world.Tile{X: x0 + 9, Y: y + 4})
	add("transport-belt", world.Tile{X: x0 + 9, Y: y + 6}, S, "")
	// ленты: длинноруким из сборщика (y+3) на общую ленту (y+7) — правая полоса
	inserter(long, world.Tile{X: x0 + 17, Y: y + 5}, world.Tile{X: x0 + 17, Y: y + 3})
	// сборщики науки: берут с общей ленты, отдают на нижнюю
	for k := 0; k < n; k++ {
		ax := x0 + 4*k
		add(asm, world.Tile{X: ax, Y: y + 9}, N, "logistic-science-pack")
		inserter(ins, world.Tile{X: ax + 1, Y: y + 8}, world.Tile{X: ax + 1, Y: y + 7})
		inserter(ins, world.Tile{X: ax + 1, Y: y + 12}, world.Tile{X: ax + 1, Y: y + 11})
	}
	for x := x0; x <= x0+18; x++ {
		add("transport-belt", world.Tile{X: x, Y: y}, E, "")
		add("transport-belt", world.Tile{X: x, Y: y + 7}, W, "")
	}
	for x := x0; x < x0+4*n; x++ {
		add("transport-belt", world.Tile{X: x, Y: y + 13}, W, "")
	}
	// тупики: конец ленты пластин и общей ленты — за ними пусто
	for _, t := range []world.Tile{{X: x0 + 19, Y: y}, {X: x0 - 1, Y: y + 7}} {
		l.AllowEnd(t)
		l.Reserved[t] = true
	}
	m.In = []Port{{world.Tile{X: x0, Y: y}, E}}
	m.Out = []Port{{world.Tile{X: x0, Y: y + 13}, W}}
	return m, nil
}

// GreenFactory — зелёная наука (8.1) к уже стоящей фабрике: ряды плавки железа и меди,
// блок GreenScience, ленты пластин к нему, наука — сбоку на ленту лабораторий labsIn
// (первый тайл ленты лабораторий, на восток; красная на ней — на правой полосе, зелёная
// встаёт на левую). Столбы — к Anchors.
func (l *Layout) GreenFactory(from bridge.Pos, labsIn world.Tile, n, ironPairs, copperPairs int) error {
	drill, furnace, ins, asm := "electric-mining-drill", "stone-furnace", "inserter", "assembling-machine-1"
	ip, ok := nearestPatch(l.W, "iron-ore", from, 20)
	if !ok {
		return fmt.Errorf("layout: не видно железа")
	}
	cp, ok := nearestPatch(l.W, "copper-ore", from, 20)
	if !ok {
		return fmt.Errorf("layout: не видно меди")
	}
	near := world.TileOf(from)
	ironOut, err := l.SmeltRow(ip.Tiles, "iron-ore", ironPairs, drill, furnace, ins, near, "green-iron")
	if err != nil {
		return err
	}
	l.Reserved[Step(ironOut.T, E)] = true
	copOut, err := l.SmeltRow(cp.Tiles, "copper-ore", copperPairs, drill, furnace, ins, near, "green-copper")
	if err != nil {
		return err
	}
	l.Reserved[Step(copOut.T, E)] = true
	lt, ok := l.FindArea(near, 19, 14, 2, 80, OffOre)
	if !ok {
		return fmt.Errorf("layout: нет места под блок зелёной науки")
	}
	g, err := l.GreenScience(lt, n, asm, ins, "green")
	if err != nil {
		return err
	}
	// медь — сбоку сверху на ленту пластин (левая полоса; железо от ряда — правая)
	copSide := world.Tile{X: g.In[0].T.X + 1, Y: g.In[0].T.Y - 1}
	labSide := world.Tile{X: labsIn.X + 1, Y: labsIn.Y - 1}
	l.Reserved[copSide], l.Reserved[labSide] = true, true
	delete(l.Reserved, Step(ironOut.T, E))
	if _, err := l.RouteBelt(Step(ironOut.T, E), E, Step(g.In[0].T, W), E, "green-belt-iron"); err != nil {
		return fmt.Errorf("железо → зелёная: %w", err)
	}
	delete(l.Reserved, labSide)
	if _, err := l.RouteBelt(Step(g.Out[0].T, W), W, labSide, S, "green-belt-science"); err != nil {
		return fmt.Errorf("зелёная → лаборатории: %w", err)
	}
	delete(l.Reserved, copSide)
	delete(l.Reserved, Step(copOut.T, E))
	if _, err := l.RouteBelt(Step(copOut.T, E), E, copSide, S, "green-belt-copper"); err != nil {
		return fmt.Errorf("медь → зелёная: %w", err)
	}
	l.Poles("small-electric-pole", "green-power")
	return nil
}

// SteelRow — сталь (8.3): n столбиков «электробур (сверху, на юг) → печь (железо) →
// инсертер → печь (сталь) → инсертер → сундук» на месторождении железа. Одна печь стали
// съедает выход одной печи железа (0.31 пластины/с). Топливо печам — бот.
func (l *Layout) SteelRow(patch []world.Tile, n int, near world.Tile, tag string) error {
	drill, furnace, ins, chest := "electric-mining-drill", "stone-furnace", "inserter", "wooden-chest"
	dw, dh := Size(l.P, drill, S)
	_, fh := Size(l.P, furnace, N)
	for _, a := range byDistance(patch, near) {
		mark := l.Mark()
		ok := true
		for i := 0; i < n && ok; i++ {
			x := a.X + i*dw
			y := a.Y
			pd := Placement{Name: drill, Pos: CenterAt(l.P, drill, S, world.Tile{X: x, Y: y}), Dir: S, Tag: tag}
			pf := Placement{Name: furnace, Pos: CenterAt(l.P, furnace, N, world.Tile{X: x, Y: y + dh}), Tag: tag}
			i1 := world.Tile{X: x, Y: y + dh + fh}
			ps := Placement{Name: furnace, Pos: CenterAt(l.P, furnace, N, world.Tile{X: x, Y: i1.Y + 1}), Tag: tag}
			i2 := world.Tile{X: x, Y: i1.Y + 1 + fh}
			pc := Placement{Name: chest, Pos: world.Tile{X: x, Y: i2.Y + 1}.Center(), Tag: tag}
			pi1 := Placement{Name: ins, Pos: i1.Center(), Dir: InserterDir(l.P, ins, i1, world.Tile{X: x, Y: i1.Y - 1}), Tag: tag}
			pi2 := Placement{Name: ins, Pos: i2.Center(), Dir: InserterDir(l.P, ins, i2, world.Tile{X: x, Y: i2.Y - 1}), Tag: tag}
			if !l.onOre(pd, "iron-ore") || !contains(Tiles(l.P, pf), DrillOutput(l.P, pd)) {
				ok = false
				break
			}
			for _, p := range []Placement{pf, pi1, ps, pi2, pc} {
				if !l.CanPlace(p, AnyLand) {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
			for _, p := range []Placement{pd, pf, pi1, ps, pi2, pc} {
				l.Add(p)
			}
		}
		if ok {
			return nil
		}
		l.Rollback(mark)
	}
	return fmt.Errorf("layout: нет места под сталь (%d)", n)
}

// PlateRow — склад пластин: n столбиков «электробур (сверху, на юг) → печь → инсертер →
// сундук» на месторождении ore (каждый — где поместится, ближе к near); бот забирает
// пластины из сундуков большими партиями (стройка: сотни лент, буры). Топливо печам — бот.
func (l *Layout) PlateRow(patch []world.Tile, ore string, n int, near world.Tile, tag string) error {
	drill, furnace, ins, chest := "electric-mining-drill", "stone-furnace", "inserter", "wooden-chest"
	_, dh := Size(l.P, drill, S)
	_, fh := Size(l.P, furnace, N)
	placed := 0
	for _, a := range byDistance(patch, near) {
		if placed == n {
			return nil
		}
		x, y := a.X, a.Y
		pd := Placement{Name: drill, Pos: CenterAt(l.P, drill, S, world.Tile{X: x, Y: y}), Dir: S, Tag: tag}
		pf := Placement{Name: furnace, Pos: CenterAt(l.P, furnace, N, world.Tile{X: x, Y: y + dh}), Tag: tag}
		it := world.Tile{X: x, Y: y + dh + fh}
		pi := Placement{Name: ins, Pos: it.Center(), Dir: InserterDir(l.P, ins, it, world.Tile{X: x, Y: it.Y - 1}), Tag: tag}
		pc := Placement{Name: chest, Pos: world.Tile{X: x, Y: it.Y + 1}.Center(), Tag: tag}
		if !l.onOre(pd, ore) || !contains(Tiles(l.P, pf), DrillOutput(l.P, pd)) {
			continue
		}
		ok := true
		for _, p := range []Placement{pf, pi, pc} {
			ok = ok && l.CanPlace(p, AnyLand)
		}
		// проходы слева и справа во всю высоту столбика — иначе столбики и соседние
		// постройки складываются в лабиринт, где бот застревает
		var aisle []world.Tile
		for yy := y; yy <= it.Y+1 && ok; yy++ {
			for _, xx := range []int{x - 1, x + 3} {
				t := world.Tile{X: xx, Y: yy}
				ok = ok && l.Free(t, AnyLand)
				aisle = append(aisle, t)
			}
		}
		if !ok {
			continue
		}
		for _, p := range []Placement{pd, pf, pi, pc} {
			l.Add(p)
		}
		for _, t := range aisle {
			l.Reserved[t] = true
		}
		placed++
	}
	if placed == n {
		return nil
	}
	return fmt.Errorf("layout: нет места под склад пластин %s (%d из %d)", ore, placed, n)
}
