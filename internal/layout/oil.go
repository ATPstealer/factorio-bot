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

// nearestPatch — ближайшее к from месторождение ресурса (не меньше minTiles тайлов) и не
// занятое шахтой раньше (skip).
func nearestPatch(w *world.World, ore string, from bridge.Pos, minTiles int) (world.Patch, bool) {
	ps := w.Patches()
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Distance(from) < ps[j].Distance(from) })
	for _, p := range ps {
		if p.Name == ore && len(p.Tiles) >= minTiles {
			return p, true
		}
	}
	return world.Patch{}, false
}
