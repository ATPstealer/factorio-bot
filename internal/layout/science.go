package layout

import (
	"fmt"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// SmeltRow — ряд плавки на месторождении (7.5): n пар «бур → печь» (бур сверху выдаёт
// вниз прямо в печь), под печами инсертеры кладут пластины на ленту вдоль ряда (течёт на
// восток) — на дальнюю, южную (правую) полосу: левая остаётся для подачи сбоку с севера.
// Выход — последний тайл ленты. Топливо печам (и burner-постройкам, если такие) — бот.
func (l *Layout) SmeltRow(patch []world.Tile, ore string, n int, drill, furnace, inserter string, near world.Tile, tag string) (Port, error) {
	dw, dh := Size(l.P, drill, S)
	fw, fh := Size(l.P, furnace, N)
	w := max(dw, fw)
	for _, a := range byDistance(patch, near) {
		// a — левый верхний тайл бура; ниже: печь, инсертеры, лента
		mark := l.Mark()
		ok := true
		yf, yi := a.Y+dh, a.Y+dh+fh
		yb := yi + 1
		for i := 0; i < n && ok; i++ {
			x := a.X + i*w
			pd := Placement{Name: drill, Pos: CenterAt(l.P, drill, S, world.Tile{X: x, Y: a.Y}), Dir: S, Tag: tag}
			pf := Placement{Name: furnace, Pos: CenterAt(l.P, furnace, N, world.Tile{X: x, Y: yf}), Tag: tag}
			ins := world.Tile{X: x, Y: yi}
			pi := Placement{Name: inserter, Pos: ins.Center(), Dir: InserterDir(l.P, inserter, ins, world.Tile{X: x, Y: yi - 1}), Tag: tag}
			if !l.onOre(pd, ore) || !contains(Tiles(l.P, pf), DrillOutput(l.P, pd)) || !l.CanPlace(pf, AnyLand) || !l.CanPlace(pi, AnyLand) {
				ok = false
				break
			}
			l.Add(pd)
			l.Add(pf)
			l.Add(pi)
		}
		for x := a.X; ok && x < a.X+n*w; x++ {
			b := Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: yb}.Center(), Dir: E, Tag: tag}
			if !l.CanPlace(b, AnyLand) {
				ok = false
				break
			}
			l.Add(b)
		}
		if ok {
			return Port{T: world.Tile{X: a.X + n*w - 1, Y: yb}, Dir: E}, nil
		}
		l.Rollback(mark)
	}
	return Port{}, fmt.Errorf("layout: нет места под ряд плавки %s", ore)
}

// Labs — лента науки (на восток) и под ней n лабораторий с инсертерами с ленты.
func (l *Layout) Labs(lt world.Tile, n int, inserter, tag string) Module {
	lw, _ := Size(l.P, "lab", N)
	m := Module{Name: "лаборатории", LT: lt, RB: world.Tile{X: lt.X + n*lw - 1, Y: lt.Y + 1 + lw}, Electric: true}
	for i := 0; i < n; i++ {
		x := lt.X + i*lw
		ins := world.Tile{X: x + lw/2, Y: lt.Y + 1}
		l.Add(Placement{Name: inserter, Pos: ins.Center(), Dir: InserterDir(l.P, inserter, ins, world.Tile{X: ins.X, Y: lt.Y}), Tag: tag})
		l.Add(Placement{Name: "lab", Pos: CenterAt(l.P, "lab", N, world.Tile{X: x, Y: lt.Y + 2}), Tag: tag})
	}
	for x := lt.X; x < lt.X+n*lw; x++ {
		l.Add(Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: lt.Y}.Center(), Dir: E, Tag: tag})
	}
	l.AllowEnd(world.Tile{X: lt.X + n*lw, Y: lt.Y})
	l.Reserved[world.Tile{X: lt.X + n*lw, Y: lt.Y}] = true // тупик: за ним пусто
	m.In = []Port{{lt, E}}
	return m
}

// ScienceFactory — автоматическая красная наука: ряды плавки железа и меди на
// месторождениях (электробуры, электроинсертеры — решение пользователя: с электричеством
// угольные буры и паровые инсертеры не ставить), блок науки (pairs пар сборщиков),
// лаборатории, ленты между ними, столбы (к Anchors — уже стоящей сети).
func (l *Layout) ScienceFactory(from bridge.Pos, pairs, ironPairs, copperPairs, labs int) error {
	drill, furnace, smIns, asm, ins := "electric-mining-drill", "stone-furnace", "inserter", "assembling-machine-1", "inserter"
	near := world.TileOf(from)
	ip, ok := nearestPatch(l.W, "iron-ore", from, 20)
	if !ok {
		return fmt.Errorf("layout: не видно железа")
	}
	cp, ok := nearestPatch(l.W, "copper-ore", from, 20)
	if !ok {
		return fmt.Errorf("layout: не видно меди")
	}
	ironOut, err := l.SmeltRow(ip.Tiles, "iron-ore", ironPairs, drill, furnace, smIns, near, "sci-iron")
	if err != nil {
		return err
	}
	l.Reserved[Step(ironOut.T, E)] = true
	copOut, err := l.SmeltRow(cp.Tiles, "copper-ore", copperPairs, drill, furnace, smIns, world.Tile{X: ironOut.T.X, Y: ironOut.T.Y}, "sci-copper")
	if err != nil {
		return err
	}
	l.Reserved[Step(copOut.T, E)] = true
	// наука — между рядами, не на руде
	mid := world.Tile{X: (ironOut.T.X + copOut.T.X) / 2, Y: (ironOut.T.Y + copOut.T.Y) / 2}
	sw, sh := 8*pairs, 7
	lw, _ := Size(l.P, "lab", N)
	lt, ok := l.FindArea(mid, sw+2+labs*lw, sh+lw+2, 2, 60, OffOre)
	if !ok {
		return fmt.Errorf("layout: нет места под блок науки")
	}
	sci := l.RedScience(lt, pairs, asm, ins, "science")
	labM := l.Labs(world.Tile{X: lt.X + sw + 2, Y: lt.Y + sh + 1}, labs, ins, "labs")
	// железо от ряда плавки — на правой полосе (SmeltRow), медь — сбоку с севера на левую
	copSide := world.Tile{X: sci.In[0].T.X + 1, Y: sci.In[0].T.Y - 1}
	l.Reserved[copSide] = true
	delete(l.Reserved, Step(ironOut.T, E))
	if _, err := l.RouteBelt(Step(ironOut.T, E), E, Step(sci.In[0].T, W), E, "belt-iron"); err != nil {
		return fmt.Errorf("железо → наука: %w", err)
	}
	if _, err := l.RouteBelt(Step(sci.Out[0].T, E), E, Step(labM.In[0].T, W), E, "belt-science"); err != nil {
		return fmt.Errorf("наука → лаборатории: %w", err)
	}
	delete(l.Reserved, copSide)
	delete(l.Reserved, Step(copOut.T, E))
	if _, err := l.RouteBelt(Step(copOut.T, E), E, copSide, S, "belt-copper"); err != nil {
		return fmt.Errorf("медь → наука: %w", err)
	}
	l.Poles("small-electric-pole", "power")
	return nil
}
