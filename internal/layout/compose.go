package layout

import (
	"fmt"
	"math"
	"sort"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// DemoResult — что разложено и сколько чего.
type DemoResult struct {
	Mines   map[string]MineResult
	Modules []Module
	Notes   []string
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

// Demo — критерий стадии 6: шахты железа, меди и угля, две колонны плавки, блок красной
// науки (n пар сборщиков), паровая станция и столбы; всё связано лентами.
// Буры — burner (без электричества), сборщики — assembling-machine-1.
func (l *Layout) Demo(from bridge.Pos, pairs int) (DemoResult, error) {
	res := DemoResult{Mines: map[string]MineResult{}}
	asm, sciIns, smeltIns, drill, furnace := "assembling-machine-1", "inserter", "burner-inserter", "burner-mining-drill", "stone-furnace"

	// потребность науки → пластины → печи → буры
	sciRate := float64(pairs) * l.P.Entities[asm].CraftingSpeed / l.P.Recipes["automation-science-pack"].Energy
	plate := func(item string) float64 {
		n := 0.0
		for _, in := range l.P.Recipes["automation-science-pack"].Ingredients {
			k := in.Amount
			if in.Name == "iron-gear-wheel" {
				k *= l.P.Recipes["iron-gear-wheel"].Ingredients[0].Amount // 2 железа на шестерню
				in.Name = "iron-plate"
			}
			if in.Name == item {
				n += k
			}
		}
		return n * sciRate
	}
	furnaceRate := l.P.Entities[furnace].CraftingSpeed / l.P.Recipes["iron-plate"].Energy
	drillRate := l.P.Entities[drill].MiningSpeed // руда с mining_time 1
	need := map[string]float64{"iron-ore": plate("iron-plate"), "copper-ore": plate("copper-plate")}
	res.Notes = append(res.Notes, fmt.Sprintf("наука %.2f/с: железо %.2f/с, медь %.2f/с", sciRate, need["iron-ore"], need["copper-ore"]))

	type line struct {
		ore    string
		mine   MineResult
		smelt  Module
		coal   MineResult
		plates string
	}
	var lines []line
	for _, ore := range []string{"iron-ore", "copper-ore"} {
		furnaces := int(math.Ceil(need[ore]/furnaceRate - 1e-9))
		drills := int(math.Ceil(need[ore]/drillRate - 1e-9))
		pt, ok := nearestPatch(l.W, ore, from, 30)
		if !ok {
			return res, fmt.Errorf("layout: не видно месторождения %s", ore)
		}
		mr, err := l.Mine(pt.Tiles, ore, drill, drills, "mine:"+ore)
		if err != nil {
			return res, err
		}
		res.Mines[ore] = mr
		// плавильня — у выхода шахты, не на руде
		_, fh := Size(l.P, furnace, N)
		lt, ok := l.FindArea(world.Tile{X: mr.Out.X + 3, Y: mr.Out.Y - 2}, 6, furnaces*fh, 1, 40, OffOre)
		if !ok {
			return res, fmt.Errorf("layout: нет места под плавильню %s", ore)
		}
		sm := l.SmeltingColumn(lt, furnaces, furnace, smeltIns, "smelt:"+ore)
		res.Modules = append(res.Modules, sm)
		side := world.Tile{X: sm.In[0].T.X - 1, Y: sm.In[0].T.Y + 1} // сюда сбоку — уголь
		exit := Step(sm.Out[0].T, S)                                 // отсюда — пластины
		// подходы модуля держим свободными, пока до них не дойдёт очередь
		l.Reserved[side], l.Reserved[exit] = true, true
		if _, err := l.RouteBelt(mr.Out, mr.OutDir, Step(sm.In[0].T, N), S, "belt:"+ore); err != nil {
			return res, fmt.Errorf("руда %s → плавильня: %w", ore, err)
		}
		delete(l.Reserved, side)
		// уголь: своя маленькая шахта, лента сбоку во вход плавильни (на другую полосу)
		cp, ok := nearestPatch(l.W, "coal", world.Tile{X: lt.X, Y: lt.Y}.Center(), 10)
		if !ok {
			return res, fmt.Errorf("layout: не видно угля")
		}
		// топливо: печи + инсертеры — немного; 1–2 бура хватает
		cm, err := l.Mine(cp.Tiles, "coal", drill, 2, "mine:coal:"+ore)
		if err != nil {
			return res, err
		}
		if _, err := l.RouteBelt(cm.Out, cm.OutDir, side, E, "belt:coal:"+ore); err != nil {
			return res, fmt.Errorf("уголь → плавильня %s: %w", ore, err)
		}
		lines = append(lines, line{ore: ore, mine: mr, smelt: sm, coal: cm})
		res.Notes = append(res.Notes, fmt.Sprintf("%s: буров %d (%.2f/с), печей %d", ore, mr.Drills, mr.Rate, furnaces))
	}

	// наука — рядом с плавильней железа, не на руде
	iron := lines[0].smelt
	sw, sh := 8*pairs, 7
	lt, ok := l.FindArea(world.Tile{X: iron.RB.X + 4, Y: iron.RB.Y + 3}, sw, sh, 2, 60, OffOre)
	if !ok {
		return res, fmt.Errorf("layout: нет места под блок науки")
	}
	sci := l.RedScience(lt, pairs, asm, sciIns, "science")
	res.Modules = append(res.Modules, sci)
	// железо — прямо во вход ленты пластин, медь — сбоку на вторую полосу
	copSide := world.Tile{X: sci.In[0].T.X + 1, Y: sci.In[0].T.Y - 1}
	l.Reserved[copSide] = true
	ironExit, copExit := Step(iron.Out[0].T, S), Step(lines[1].smelt.Out[0].T, S)
	delete(l.Reserved, ironExit)
	if _, err := l.RouteBelt(ironExit, S, Step(sci.In[0].T, W), E, "belt:iron-plate"); err != nil {
		return res, fmt.Errorf("железо → наука: %w", err)
	}
	delete(l.Reserved, copSide)
	delete(l.Reserved, copExit)
	if _, err := l.RouteBelt(copExit, S, copSide, S, "belt:copper-plate"); err != nil {
		return res, fmt.Errorf("медь → наука: %w", err)
	}

	// электричество: станция у воды, столбы на всё электрическое
	engines := 1
	st, err := l.SteamPower(world.Tile{X: lt.X + sw/2, Y: lt.Y + sh/2}, engines, "power")
	if err != nil {
		res.Notes = append(res.Notes, "станции нет: "+err.Error())
	} else {
		res.Modules = append(res.Modules, st)
	}
	n := l.Poles("small-electric-pole", "power")
	res.Notes = append(res.Notes, fmt.Sprintf("столбов %d", n))
	return res, nil
}
