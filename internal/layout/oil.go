package layout

import (
	"fmt"
	"sort"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// Oil — нефть (8.5): до jacks насосов на ближайшем к from нефтяном поле → труба → нефтезавод
// (basic-oil-processing; вход/выход — FluidConn) → химзаводы:
// plastic на пластик (газ — первый вход; уголь из сундука инсертером), sulfur на серу
// (вода — первый вход от помпы, газ — второй). Продукт — инсертером в сундук (забирает бот).
// Жидкости по сетям: oil-crude, oil-gas, oil-water. Столбы — к Anchors.
func (l *Layout) Oil(from bridge.Pos, jacks, plastic, sulfur int) error {
	const jack, refinery, chem, ins, chest = "pumpjack", "oil-refinery", "chemical-plant", "inserter", "wooden-chest"
	field, ok := nearestPatch(l.W, "crude-oil", from, 1)
	if !ok {
		return fmt.Errorf("layout: не видно нефти")
	}
	// насосы: на скважинах поля, ближайших к центру; выход — свободный тайл суши
	var jackOut []Conn
	for _, t := range byDistance(field.Tiles, world.TileOf(field.Center)) {
		if len(jackOut) == jacks {
			break
		}
		for _, d := range []int{N, E, S, W} {
			pl := Placement{Name: jack, Pos: t.Center(), Dir: d, Tag: "oil-jack"}
			c := Connections(l.P, pl)[0]
			if l.CanPlace(pl, AnyLand) && l.Free(c.To, AnyLand) && !l.fluidNeighbour(c.To, map[world.Tile]bool{c.At: true}) {
				l.Add(pl)
				l.Reserved[c.To] = true
				jackOut = append(jackOut, c)
				break
			}
		}
	}
	if len(jackOut) == 0 { // видно меньше скважин — сколько есть, остальные потом
		return fmt.Errorf("layout: на поле нефти нет места под насос")
	}
	near := world.TileOf(field.Center)
	// нефтезавод: поле в 2 тайла под трубы со всех сторон
	rw, rh := Size(l.P, refinery, N)
	lt, ok := l.FindArea(near, rw, rh, 2, 60, OffOre)
	if !ok {
		return fmt.Errorf("layout: нет места под нефтезавод")
	}
	ref := Placement{Name: refinery, Pos: CenterAt(l.P, refinery, N, lt), Recipe: "basic-oil-processing", Tag: "oil-refinery"}
	l.Add(ref)
	crudeIn, ok1 := FluidConn(l.P, ref, "crude-oil")
	gasOut, ok2 := FluidConn(l.P, ref, "petroleum-gas")
	if !ok1 || !ok2 {
		return fmt.Errorf("layout: у нефтезавода нет входа нефти/выхода газа")
	}
	// химзаводы в ряд: сундук, инсертер, завод, инсертер, сундук; ряд над ними — под трубы
	cw, ch := Size(l.P, chem, N)
	n := plastic + sulfur
	blockW := cw + 4
	clt, ok := l.FindArea(near, blockW*n, ch+2, 1, 60, OffOre)
	if !ok {
		return fmt.Errorf("layout: нет места под %d химзаводов", n)
	}
	var plants []Placement
	for i := 0; i < n; i++ {
		x, y := clt.X+i*blockW, clt.Y+1
		recipe := "plastic-bar"
		if i >= plastic {
			recipe = "sulfur"
		}
		pl := Placement{Name: chem, Pos: CenterAt(l.P, chem, N, world.Tile{X: x + 2, Y: y}), Recipe: recipe, Tag: "oil-chem"}
		l.Add(pl)
		mid := y + ch/2
		out := world.Tile{X: x + 2 + cw, Y: mid}
		l.Add(Placement{Name: ins, Pos: out.Center(), Dir: InserterDir(l.P, ins, out, Step(out, W)), Tag: "oil-chem"})
		l.Add(Placement{Name: chest, Pos: Step(out, E).Center(), Tag: "oil-chem"})
		if recipe == "plastic-bar" { // уголь
			in := world.Tile{X: x + 1, Y: mid}
			l.Add(Placement{Name: ins, Pos: in.Center(), Dir: InserterDir(l.P, ins, in, Step(in, W)), Tag: "oil-chem"})
			l.Add(Placement{Name: chest, Pos: Step(in, W).Center(), Tag: "oil-chem"})
		}
		plants = append(plants, pl)
	}
	// трубы: нефть от насосов, газ к заводам, вода к сернистым
	for i, c := range jackOut {
		delete(l.Reserved, c.To)
		var err error
		if i == 0 {
			_, err = l.RoutePipe(c, crudeIn, "oil-crude")
		} else {
			_, err = l.JoinPipe(c, "oil-crude")
		}
		if err != nil {
			return fmt.Errorf("нефть от насоса %d: %w", i+1, err)
		}
	}
	for i, p := range plants {
		gas, _ := FluidConn(l.P, p, "petroleum-gas")
		var err error
		if i == 0 {
			_, err = l.RoutePipe(gasOut, gas, "oil-gas")
		} else {
			_, err = l.JoinPipe(gas, "oil-gas")
		}
		if err != nil {
			return fmt.Errorf("газ к химзаводу %d: %w", i+1, err)
		}
	}
	first := true
	for i, p := range plants {
		if p.Recipe != "sulfur" {
			continue
		}
		water, _ := FluidConn(l.P, p, "water")
		var err error
		if first {
			_, err = l.WaterPump(near, "oil-water", func(pump Placement) bool {
				if !l.CanPlace(pump, OffOre) {
					return false
				}
				l.Add(pump)
				_, err := l.RoutePipe(Connections(l.P, pump)[0], water, "oil-water")
				return err == nil
			})
			first = false
		} else {
			_, err = l.JoinPipe(water, "oil-water")
		}
		if err != nil {
			return fmt.Errorf("вода к химзаводу %d: %w", i+1, err)
		}
	}
	l.Poles("small-electric-pole", "oil-power")
	return nil
}

// OilBelt — пластик и сера с химзаводов Oil (уже в раскладке) на базу одной лентой по
// разным полосам (иначе одна полоса забьётся одним): пластик — инсертером на первую ленту
// поперёк (дальняя от инсертера полоса), лента до to (там смотрит toDir); сера — своей
// лентой, входящей в основную сбоку с другой стороны (боковая подача → ближняя полоса).
// Серы нужно мало (≈1700 на всю синюю науку до ракеты): её ленту не подвести (сундук
// обступили трубы) — только пластик (sulfur = false), серу возит бот.
func (l *Layout) OilBelt(to world.Tile, toDir int, tag string) (sulfur bool, err error) {
	chests := map[string]world.Tile{}
	for _, p := range l.List {
		if l.P.Entities[p.Name].Type != "inserter" {
			continue
		}
		pick, drop := InserterEnds(l.P, p)
		if src, dst := l.At(pick), l.At(drop); src != nil && dst != nil && src.Name == "chemical-plant" && dst.Name == "wooden-chest" {
			chests[src.Recipe] = drop
		}
	}
	pc, ok := chests["plastic-bar"]
	if !ok {
		return false, fmt.Errorf("layout: нет сундука пластика")
	}
	if sc, ok := chests["sulfur"]; ok && l.oilBelt(pc, &sc, to, toDir, tag) {
		return true, nil
	}
	if l.oilBelt(pc, nil, to, toDir, tag) {
		return false, nil
	}
	return false, fmt.Errorf("layout: пластик на базу не провести")
}

// oilBelt — лента пластика от сундука pc до to; sc != nil — и сера сбоку на правую полосу.
func (l *Layout) oilBelt(pc world.Tile, sc *world.Tile, to world.Tile, toDir int, tag string) bool {
	const ins, belt = "inserter", "transport-belt"
	// сторона сундука серы под инсертер и начало её ленты — до основной ленты (иначе та
	// пройдёт вплотную и займёт все стороны)
	sd := -1
	var sIn, sDrop world.Tile
	if sc != nil {
		for _, d := range []int{S, E, N, W} {
			if in, drop := Step(*sc, d), Step(Step(*sc, d), d); l.Free(in, AnyLand) && l.Free(drop, AnyLand) {
				sd, sIn, sDrop = d, in, drop
				break
			}
		}
		if sd < 0 {
			return false
		}
		l.Reserved[sIn], l.Reserved[sDrop] = true, true
		defer func() {
			delete(l.Reserved, sIn)
			delete(l.Reserved, sDrop)
		}()
	}
	// инсертер из pc в сторону d на первую ленту поперёк (ход bd = d+4, по часовой): кладёт
	// на дальнюю от себя полосу — со стороны d, а она при ходе bd левая. Пластик — слева
	for _, d := range []int{S, E, N, W} {
		in, drop := Step(pc, d), Step(Step(pc, d), d)
		bd := (d + 4) % 16
		next := Step(drop, bd)
		if !l.Free(in, AnyLand) || !l.Free(drop, AnyLand) || !l.Free(next, AnyLand) {
			continue
		}
		mark := l.Mark()
		l.Add(Placement{Name: ins, Pos: in.Center(), Dir: InserterDir(l.P, ins, in, pc), Tag: tag})
		l.Add(Placement{Name: belt, Pos: drop.Center(), Dir: bd, Tag: tag})
		n0 := len(l.List)
		if _, err := l.RouteBelt(next, bd, to, toDir, tag); err != nil {
			l.Rollback(mark)
			continue
		}
		if sc == nil {
			return true
		}
		main := append([]Placement(nil), l.List[n0:]...)
		delete(l.Reserved, sIn)
		delete(l.Reserved, sDrop)
		if l.sulfurJoin(*sc, sd, main, tag) {
			return true
		}
		l.Reserved[sIn], l.Reserved[sDrop] = true, true
		l.Rollback(mark)
	}
	return false
}

// sulfurJoin — лента серы от сундука sc (инсертер в сторону d) к основной ленте main:
// входит сбоку справа (по ходу ленты) на прямом участке — ложится на правую полосу.
func (l *Layout) sulfurJoin(sc world.Tile, d int, main []Placement, tag string) bool {
	const ins, belt = "inserter", "transport-belt"
	// прямые участки основной ленты, ближние к сундуку серы
	type cand struct {
		side world.Tile
		dir  int
		d    int
	}
	var cs []cand
	for i := 1; i+1 < len(main); i++ {
		p := main[i]
		if p.Name != belt || main[i-1].Name != belt || main[i+1].Name != belt || main[i-1].Dir != p.Dir || main[i+1].Dir != p.Dir {
			continue
		}
		t := world.TileOf(p.Pos)
		side := Step(t, (p.Dir+4)%16) // справа по ходу
		if !l.Free(side, AnyLand) {
			continue
		}
		cs = append(cs, cand{side, (p.Dir + 12) % 16, abs(side.X-sc.X) + abs(side.Y-sc.Y)})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	for k, c := range cs {
		if k >= 8 {
			break
		}
		in, drop := Step(sc, d), Step(Step(sc, d), d)
		mark := l.Mark()
		l.Add(Placement{Name: ins, Pos: in.Center(), Dir: InserterDir(l.P, ins, in, sc), Tag: tag})
		if drop == c.side {
			l.Add(Placement{Name: belt, Pos: drop.Center(), Dir: c.dir, Tag: tag})
			return true
		}
		if _, err := l.RouteBelt(drop, d, c.side, c.dir, tag); err == nil {
			return true
		}
		l.Rollback(mark)
	}
	return false
}
