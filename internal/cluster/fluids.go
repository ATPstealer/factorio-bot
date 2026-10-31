package cluster

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Кластеры с жидкостями проекта жёлтой науки (stages.md, стадия 7; cluster.md §2а «Нефть»,
// «Сборка»): НПЗ продвинутой переработки (2 входа, 3 выхода), химия с выходом жидкостью
// (крекинг, смазка, кислота), сборка с жидкостью на входе (электродвигатели, процессоры).
//
// Общий приём: у машины ряда несколько трубных соединений в одном ряду поперёк (v) — ряд трубы
// одной жидкости проходит через своё соединение прямо, чужие соединения в том же ряду обходит
// парой подземных труб (подземная труба сбоку не соединяется), а соединение чужой жидкости
// уводится подземной парой в свой ряд дальше от машины. Ряды труб разных жидкостей — через
// пустой ряд (cluster.md §5: соседние трубы соединяются).

// coord — координаты тайла t в системе ряда f (поиск в окне); false — вне окна.
func (f frame) coord(t world.Tile, umax, vmax int) (u, v int, ok bool) {
	for u := -2; u <= umax+2; u++ {
		for v := -2; v <= vmax+2; v++ {
			if f.tile(u, v) == t {
				return u, v, true
			}
		}
	}
	return 0, 0, false
}

// ugPair — пара подземных труб от тайла a до тайла b (по одной оси, направление d от a к b в
// системе ряда): вход в a (наземное соединение — назад), выход в b (наземное — вперёд).
func ugPair(f frame, a, b world.Tile, d int, tag string, add func(layout.Placement) error) error {
	wd := f.dir(d)
	if err := add(layout.Placement{Name: "pipe-to-ground", Pos: a.Center(), Dir: (wd + 8) % 16, Tag: tag}); err != nil {
		return err
	}
	return add(layout.Placement{Name: "pipe-to-ground", Pos: b.Center(), Dir: wd, Tag: tag})
}

// pipeRowSkip — ряд трубы v от u0 до u1 (не включая): прямая труба на свободных тайлах; тайл из
// skip (чужое соединение) — обход парой подземных через него (вход за тайл до, выход за тайл
// после); занятые тайлы (уже положенные соединения и подземки этой трубы) пропускаются.
func pipeRowSkip(l *layout.Layout, f frame, v, u0, u1 int, tag string, skip map[world.Tile]bool, add func(layout.Placement) error) error {
	for u := u0; u < u1; u++ {
		t := f.tile(u, v)
		if skip[f.tile(u+1, v)] { // следующий — чужое соединение: пара подземных через него
			if err := ugPair(f, t, f.tile(u+2, v), layout.E, tag, add); err != nil {
				return err
			}
			u += 2
			continue
		}
		if l.At(t) != nil {
			continue
		}
		if err := add(layout.Placement{Name: "pipe", Pos: t.Center(), Tag: tag}); err != nil {
			return err
		}
	}
	return nil
}

// facing — направление (N или S в системе ряда) машины name с левым верхним углом (u, v), при
// котором её входные соединения выходят в ряд inV.
func (f frame) facing(l *layout.Layout, name string, u, v, inV, umax, vmax int) (int, error) {
	for _, d := range []int{layout.N, layout.S} {
		m := f.at(l, name, u, v, d, "")
		for _, cn := range layout.Connections(l.P, m) {
			if cn.Flow != "input" {
				continue
			}
			if _, cv, ok := f.coord(cn.To, umax, vmax); ok && cv == inV {
				return d, nil
			}
		}
	}
	return 0, fmt.Errorf("cluster: у %s нет входа жидкости в ряд %d", name, inV)
}

// splitIO — соединения машины: входы и выходы (в порядке соединений прототипа — по порядку
// жидкостей рецепта).
func splitIO(l *layout.Layout, m layout.Placement) (ins, outs []layout.Conn) {
	for _, cn := range layout.Connections(l.P, m) {
		if cn.Flow == "input" {
			ins = append(ins, cn)
		} else {
			outs = append(outs, cn)
		}
	}
	return
}

// recipeFluids — жидкости и предметы рецепта: входы и выходы по порядку.
func recipeFluids(p *knowledge.Prototypes, recipe string) (fluidsIn, solids, fluidsOut []string) {
	r := p.Recipes[recipe]
	for _, i := range r.Ingredients {
		if _, fl := p.Fluids[i.Name]; fl {
			fluidsIn = append(fluidsIn, i.Name)
		} else {
			solids = append(solids, i.Name)
		}
	}
	for _, o := range r.Products {
		if _, fl := p.Fluids[o.Name]; fl {
			fluidsOut = append(fluidsOut, o.Name)
		}
	}
	return
}

// RefineryAdv — НПЗ продвинутой переработки (stages.md, стадия 51): n заводов в столбец этажа
// переработки, рецепт advanced-oil-processing (нефть + вода → тяжёлое, лёгкое, газ).
func RefineryAdv(p *knowledge.Prototypes, n int) *Cluster {
	const recipe = "advanced-oil-processing"
	r := p.Recipes[recipe]
	k := float64(n) * p.Entities["oil-refinery"].CraftingSpeed / r.Energy
	c := &Cluster{Kind: Make, Product: "heavy-oil", Reserve: 1,
		Units:  []Unit{{Recipe: recipe, Machine: "oil-refinery", Machines: float64(n)}},
		PowerW: float64(n) * p.Entities["oil-refinery"].EnergyUsage * 60}
	for _, i := range r.Ingredients {
		c.In = append(c.In, Flow{i.Name, i.Amount * k})
	}
	for _, o := range r.Products {
		c.Out = append(c.Out, Flow{o.Name, o.Amount * k})
	}
	c.Cross, c.Pitch, c.Head = refAdvCross, 5, 1
	c.Count, c.Room, c.Need = n, n, n
	return c
}

// Ряды НПЗ‑2 поперёк столбца (refineryAdvRow): 0 — труба второй жидкости входа, 1 — выходы её
// подземных пар, 2 — пусто, 3 — труба первой жидкости входа (соединения), 4–8 — НПЗ, 9 —
// соединения выходов (входы подземных пар), 10 — пусто, 11, 13, 15 — трубы трёх продуктов
// (через пустой ряд: соседние трубы соединяются, cluster.md §5).
const (
	refAdvCross = 16
	refAdvIn2V  = 0
	refAdvInV   = 3
	refAdvOutV  = 9
)

// refAdvOutRows — ряды труб продуктов НПЗ‑2 по порядку продуктов рецепта.
var refAdvOutRows = []int{11, 13, 15}

// IsRefineryAdv — НПЗ продвинутой переработки.
func (c *Cluster) IsRefineryAdv() bool {
	return c.IsRefinery() && len(c.Units) > 0 && c.Units[0].Recipe == "advanced-oil-processing"
}

func refineryAdvRow(l *layout.Layout, f frame, c *Cluster, L int, tag string, add func(layout.Placement) error) error {
	recipe := c.Units[0].Recipe
	rowTag := func(fluid string) string { return tag + "/" + fluid }
	fluidsIn, _, fluidsOut := recipeFluids(l.P, recipe)
	if len(fluidsIn) != 2 || len(fluidsOut) != 3 {
		return fmt.Errorf("cluster %s: рецепт %s — ожидалось 2 входа и 3 выхода жидкости", c.Name, recipe)
	}
	skipIn := map[world.Tile]bool{} // тайлы ряда входа 1, занятые чужим соединением: обход парой
	end := c.Head + c.Count*c.Pitch
	for i := 0; i < c.Count; i++ {
		u0 := c.Head + i*c.Pitch
		d, err := f.facing(l, "oil-refinery", u0, refAdvInV+1, refAdvInV, L, c.Cross)
		if err != nil {
			return err
		}
		m := f.at(l, "oil-refinery", u0, refAdvInV+1, d, tag)
		m.Recipe = recipe
		if err := add(m); err != nil {
			return err
		}
		ins, outs := splitIO(l, m)
		if len(ins) < 2 || len(outs) < 3 {
			return fmt.Errorf("cluster %s: у НПЗ соединений %d/%d", c.Name, len(ins), len(outs))
		}
		// вход 1 — прямо в ряд refAdvInV; вход 2 — парой подземных в ряд refAdvIn2V
		if err := add(layout.Placement{Name: "pipe", Pos: ins[0].To.Center(), Tag: rowTag(fluidsIn[0])}); err != nil {
			return err
		}
		u2, _, _ := f.coord(ins[1].To, L, c.Cross)
		if err := ugPair(f, ins[1].To, f.tile(u2, refAdvIn2V+1), layout.N, rowTag(fluidsIn[1]), add); err != nil {
			return err
		}
		skipIn[ins[1].To] = true
		// выходы: каждый — парой подземных в свой ряд
		for k, o := range outs[:3] {
			uo, vo, _ := f.coord(o.To, L, c.Cross)
			if vo != refAdvOutV {
				return fmt.Errorf("cluster %s: выход НПЗ не в ряду %d (%d)", c.Name, refAdvOutV, vo)
			}
			if err := ugPair(f, o.To, f.tile(uo, refAdvOutRows[k]-1), layout.S, rowTag(fluidsOut[k]), add); err != nil {
				return err
			}
		}
	}
	// ряд входа 1: прямые трубы, через чужие соединения — подземной парой; вход 2 и продукты —
	// свои ряды целиком
	if err := pipeRowSkip(l, f, refAdvInV, 0, end, rowTag(fluidsIn[0]), skipIn, add); err != nil {
		return err
	}
	if err := pipeRowSkip(l, f, refAdvIn2V, 0, end, rowTag(fluidsIn[1]), nil, add); err != nil {
		return err
	}
	for k, v := range refAdvOutRows {
		if err := pipeRowSkip(l, f, v, c.Head, L, rowTag(fluidsOut[k]), nil, add); err != nil {
			return err
		}
	}
	c.Ports = append(c.Ports,
		Port{T: f.tile(-1, refAdvInV), Dir: f.dir(layout.E), Item: fluidsIn[0], In: true},
		Port{T: f.tile(-1, refAdvIn2V), Dir: f.dir(layout.E), Item: fluidsIn[1], In: true})
	for k, v := range refAdvOutRows {
		c.Ports = append(c.Ports, Port{T: f.tile(L, v), Dir: f.dir(layout.E), Item: fluidsOut[k]})
	}
	return nil
}

// ChemFluid — химия с выходом жидкостью (stages.md, стадии 52–54): n химзаводов рецепта recipe
// (крекинг тяжёлого — тяжёлое + вода → лёгкое; смазка — тяжёлое → смазка; кислота — сера +
// железо + вода → кислота) в столбец этажа переработки; продукт — в трубу шины над этажом.
func ChemFluid(p *knowledge.Prototypes, recipe string, n int) *Cluster {
	c := Chem(p, recipe, n)
	c.Cross, c.Pitch, c.Head = chemFluidCross, 4, 1
	if c.solidInputs(p) > 0 { // буферы предметов в голове (cluster.md §6)
		c.Head = bufferHead
	}
	return c
}

// HeadBuffers — буферы предметов входа в голове ряда (cluster.md §6) у кластера, собранного не
// через Split (Chem: батареи — железо | медь).
func (c *Cluster) HeadBuffers() { c.Head = bufferHead }

// IsChemFluid — химия с продуктом-жидкостью (свой шаблон).
func (c *Cluster) IsChemFluid(p *knowledge.Prototypes) bool {
	if !c.IsChem() || len(c.Out) == 0 {
		return false
	}
	_, fl := p.Fluids[c.Out[0].Item]
	return fl
}

// Ряды химии с выходом жидкостью (chemFluidRow): 0 — труба второй жидкости входа (или сундуки
// буфера предмета 1), 1 — выходы её подземных пар (или инсертеры буфера 1), 2 — лента предметов,
// 3 — соединения входов: труба первой жидкости, у второго соединения — инсертер с ленты или
// вход подземной пары второй жидкости, 4–6 — химзаводы (в голове — буфер предмета 2), 7 —
// соединения выходов и труба продукта. Подача предметов — боком в голову ленты по полосам
// (sideIn), жидкости — с дальнего конца ряда (голова занята подачами), продукт — в конце ряда.
const chemFluidCross = 8

func chemFluidRow(l *layout.Layout, f frame, c *Cluster, L int, tag string, add func(layout.Placement) error,
	row func(v, from, to, d int) error, deadEnd func(v, to int), sideIn func(v int) error) error {
	recipe := c.Units[0].Recipe
	rowTag := func(fluid string) string { return tag + "/" + fluid }
	fluidsIn, solids, _ := recipeFluids(l.P, recipe)
	if len(fluidsIn) == 0 || len(fluidsIn) > 2 || len(solids) > 2 || len(fluidsIn) == 2 && len(solids) > 0 {
		return fmt.Errorf("cluster %s: рецепт %s — жидкостей %d, предметов %d: шаблона нет", c.Name, recipe, len(fluidsIn), len(solids))
	}
	product := c.Out[0].Item
	const in2V, beltV, inV, plantV, outV = 0, 2, 3, 4, 7
	skipIn := map[world.Tile]bool{}
	end := c.Head + c.Count*c.Pitch
	for i := 0; i < c.Count; i++ {
		u0 := c.Head + i*c.Pitch
		d, err := f.facing(l, "chemical-plant", u0, plantV, inV, L, c.Cross)
		if err != nil {
			return err
		}
		m := f.at(l, "chemical-plant", u0, plantV, d, tag)
		m.Recipe = recipe
		if err := add(m); err != nil {
			return err
		}
		ins, outs := splitIO(l, m)
		if len(ins) < 2 || len(outs) < 1 {
			return fmt.Errorf("cluster %s: у химзавода соединений %d/%d", c.Name, len(ins), len(outs))
		}
		if err := add(layout.Placement{Name: "pipe", Pos: ins[0].To.Center(), Tag: rowTag(fluidsIn[0])}); err != nil {
			return err
		}
		u2, _, _ := f.coord(ins[1].To, L, c.Cross)
		switch {
		case len(fluidsIn) == 2: // вторая жидкость — парой подземных в ряд in2V
			if err := ugPair(f, ins[1].To, f.tile(u2, in2V+1), layout.N, rowTag(fluidsIn[1]), add); err != nil {
				return err
			}
			skipIn[ins[1].To] = true
		case len(solids) > 0: // предметы — инсертером с ленты через тайл второго соединения
			if err := add(f.inserter(l, "inserter", u2, inV, beltV, tag)); err != nil {
				return err
			}
			skipIn[ins[1].To] = true
		}
		// выход — прямая труба в ряду outV через первое соединение выхода
		if err := add(layout.Placement{Name: "pipe", Pos: outs[0].To.Center(), Tag: rowTag(product)}); err != nil {
			return err
		}
	}
	if err := pipeRowSkip(l, f, inV, 1, L, rowTag(fluidsIn[0]), skipIn, add); err != nil {
		return err
	}
	c.Ports = append(c.Ports, Port{T: f.tile(L, inV), Dir: f.dir(layout.W), Item: fluidsIn[0], In: true})
	if len(fluidsIn) == 2 {
		if err := pipeRowSkip(l, f, in2V, 1, L, rowTag(fluidsIn[1]), nil, add); err != nil {
			return err
		}
		c.Ports = append(c.Ports, Port{T: f.tile(L, in2V), Dir: f.dir(layout.W), Item: fluidsIn[1], In: true})
	}
	if len(solids) > 0 { // лента предметов: подачи боком в голову по полосам, буферы в голове
		if err := sideIn(beltV); err != nil {
			return err
		}
		if err := row(beltV, 1, end-1, layout.E); err != nil {
			return err
		}
		deadEnd(beltV, end-1)
		if c.Head >= bufferHead {
			if err := bufferAt(l, f, c, beltV, beltV-1, beltV-2, bufferChestsN, solids[0], tag, 1); err != nil {
				return err
			}
			if len(solids) > 1 {
				if err := bufferAt(l, f, c, beltV, inV, plantV, bufferChestsN, solids[1], tag, 1); err != nil {
					return err
				}
			}
		}
	}
	if err := pipeRowSkip(l, f, outV, c.Head, L, rowTag(product), nil, add); err != nil {
		return err
	}
	c.Ports = append(c.Ports, Port{T: f.tile(L, outV), Dir: f.dir(layout.E), Item: product})
	return nil
}

// FluidMake — сборка с жидкостью на входе (stages.md, стадия 55; cluster.md §2а «Сборка»):
// кластер из Split переводится на шаблон fluidMakeRow; размер — по темпу (ByRate).
func FluidMake(c *Cluster) *Cluster {
	c.Cross, c.Pitch, c.Head = fluidMakeCross, 4, bufferHead
	c.ByRate()
	return c
}

// IsFluidMake — сборщик с жидкостью на входе (свой шаблон).
func (c *Cluster) IsFluidMake(p *knowledge.Prototypes) bool {
	return c.Kind == Make && strings.HasPrefix(c.machine(), "assembling-machine") && c.fluids(p) > 0
}

// Ряды сборки с жидкостью (fluidMakeRow): 0 — труба жидкости (соединения сборщиков), 1–3 —
// сборщики (в голове — сундуки буфера предмета 1 в ряду 3), 4 — инсертеры (вход обычный с ряда
// 5, выход длинный в ряд 6; в голове — инсертеры буфера 1), 5 — лента входа (два предмета по
// полосам), 6 — лента выхода (в голове — инсертеры буфера 2), 7 — сундуки буфера 2.
const fluidMakeCross = 8

func fluidMakeRow(l *layout.Layout, f frame, c *Cluster, L int, tag string, add func(layout.Placement) error,
	row func(v, from, to, d int) error, deadEnd func(v, to int), sideIn func(v int) error) error {
	const pipeV, machV, insV, beltV, outV = 0, 1, 4, 5, 6
	recipe := c.Units[0].Recipe
	fluidsIn, solids, _ := recipeFluids(l.P, recipe)
	if len(fluidsIn) != 1 || len(solids) > 2 {
		return fmt.Errorf("cluster %s: рецепт %s — жидкостей %d, предметов %d: шаблона нет", c.Name, recipe, len(fluidsIn), len(solids))
	}
	fluid := fluidsIn[0]
	rowTag := tag + "/" + fluid
	mach := c.machine()
	end := c.Head + c.Count*c.Pitch
	for i := 0; i < c.Count; i++ {
		u0 := c.Head + i*c.Pitch
		d, err := f.facing(l, mach, u0, machV, pipeV, L, c.Cross)
		if err != nil {
			return err
		}
		m := f.at(l, mach, u0, machV, d, tag)
		m.Recipe = recipe
		if err := add(m); err != nil {
			return err
		}
		ins, _ := splitIO(l, m)
		if err := add(layout.Placement{Name: "pipe", Pos: ins[0].To.Center(), Tag: rowTag}); err != nil {
			return err
		}
		if err := add(f.inserter(l, "inserter", u0, insV, beltV, tag)); err != nil {
			return err
		}
		if err := add(f.inserter(l, "long-handed-inserter", u0+2, insV, insV-2, tag)); err != nil {
			return err
		}
	}
	if err := pipeRowSkip(l, f, pipeV, c.Head, L, rowTag, nil, add); err != nil {
		return err
	}
	c.Ports = append(c.Ports, Port{T: f.tile(L, pipeV), Dir: f.dir(layout.W), Item: fluid, In: true})
	if err := sideIn(beltV); err != nil {
		return err
	}
	if err := row(beltV, 1, end-1, layout.E); err != nil {
		return err
	}
	deadEnd(beltV, end-1)
	if c.Head >= bufferHead { // буферы входов в голове (cluster.md §6)
		if err := bufferAt(l, f, c, beltV, insV, insV-1, bufferChestsN, solids[0], tag, 1); err != nil {
			return err
		}
		if len(solids) > 1 {
			if err := bufferAt(l, f, c, beltV, outV, outV+1, bufferChestsN, solids[1], tag, 1); err != nil {
				return err
			}
		}
	}
	if err := row(outV, c.Head, L, layout.E); err != nil {
		return err
	}
	l.AllowEnd(f.tile(L, outV))
	c.Ports = append(c.Ports, Port{T: f.tile(L, outV), Dir: f.dir(layout.E), Item: outItem(c)})
	return nil
}

// FluidPipes — трубы шин для жидкостных портов кластеров cs (cluster.md §5): шина жидкости —
// bus[fluid]; труба уже есть — продлевается до дальнего потребителя, нет — прокладывается;
// выходы — раньше входов (трубы продуктов появляются), цистерны — к трубе своей жидкости через
// насос (TankJoinPump). Перед подключением трубы шин выводятся на поверхность (SurfaceLanes:
// к подземной подключиться негде), после — снова под землю (UnderPipes) зовёт стадия.
func (b *Base) FluidPipes(l *layout.Layout, buses []*ZBus, cs []*Cluster, bus map[string]int) error {
	// докуда нужна труба жидкости: дальний потребитель или поставщик + 20; добыча нефти — не к
	// трубе шины, а трассой в склад (OilTrunk): труба нефти тянулась к дальнему месторождению
	// за край шины (1330900585, стадия 7)
	ends := map[string]int{}
	for _, c := range b.Clusters {
		if c.Kind == Mine {
			continue
		}
		for _, fl := range append(slices.Clone(c.In), c.Out...) {
			if _, ok := b.P.Fluids[fl.Item]; ok {
				ends[fl.Item] = max(ends[fl.Item], c.Area.Max.X+20)
			}
		}
		if c.Kind == Tank {
			ends[c.Product] = max(ends[c.Product], c.Area.Max.X+20)
		}
	}
	ensure := func(fluid string) (string, error) {
		k, ok := bus[fluid]
		if !ok || k >= len(buses) {
			return "", fmt.Errorf("cluster: жидкость %s — нет шины под трубу", fluid)
		}
		tag := PipeLaneTag(buses[k].Name, fluid)
		end := min(ends[fluid], buses[k].Rect.Max.X-1)
		if !laneExists(l, tag) {
			_, err := b.PipeLane(l, buses, k, fluid, end)
			return tag, err
		}
		return tag, extendPipeLane(l, tag, end)
	}
	for _, in := range []bool{false, true} {
		for _, c := range cs {
			if c.Kind == Mine {
				continue // нефть с месторождения — трассой в склад (46082668: oil-3 подключался к трубе bus-1)
			}
			if c.Kind == Tank {
				if in {
					tag, err := ensure(c.Product)
					if err != nil {
						return err
					}
					if err := TankJoinPump(l, c, tag); err != nil {
						return err
					}
				}
				continue
			}
			for _, p := range c.Ports {
				if _, fl := b.P.Fluids[p.Item]; !fl || p.In != in {
					continue
				}
				tag, err := ensure(p.Item)
				if err != nil {
					return err
				}
				if err := JoinLane(l, c, p, tag); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// laneExists — труба шины с тегом tag уже в раскладке.
func laneExists(l *layout.Layout, tag string) bool {
	for _, p := range l.List {
		if p.Tag == tag {
			return true
		}
	}
	return false
}

// pipeLaneRow — ряд (Y) трубы шины tag: где тайлов с её тегом больше всего (отводы — тем же
// тегом, но поперёк).
func pipeLaneRow(l *layout.Layout, tag string) (y int, ok bool) {
	rows := map[int]int{}
	for _, p := range l.List {
		if (p.Name == "pipe" || p.Name == "pipe-to-ground") && p.Tag == tag {
			rows[world.TileOf(p.Pos).Y]++
		}
	}
	n := -1
	for r, k := range rows {
		if k > n || k == n && r < y {
			y, n = r, k
		}
	}
	return y, n > 0
}

// extendPipeLane — продлить трубу шины tag до end по её ряду (труба кладётся до последнего
// потребителя и потом продлевается, cluster.md §5).
func extendPipeLane(l *layout.Layout, tag string, end int) error {
	y, ok := pipeLaneRow(l, tag)
	if !ok {
		return fmt.Errorf("cluster: трубы %s нет", tag)
	}
	last := -1 << 30
	for _, p := range l.List {
		if p.Tag == tag && strings.HasPrefix(p.Name, "pipe") {
			if t := world.TileOf(p.Pos); t.Y == y && t.X > last {
				last = t.X
			}
		}
	}
	if last+1 >= end {
		return nil
	}
	// по ряду не прошла (препятствие длиннее подземки: 1330900585, труба газа bus-2) — сколько
	// легло, столько и есть: к трубе подключаются в любом месте (JoinPipe ищет путь сам)
	_ = pipeAlong(l, last+1, end, y, tag)
	return nil
}

// SurfaceLanes — трубы шин buses обратно на поверхность (обратно UnderPipes): подземная пара,
// под которой тайлы свободны, — снова обычные трубы (к ним можно подключиться, JoinPipe);
// пары через занятое — на месте. Зовётся перед подключением новых потребителей.
func SurfaceLanes(l *layout.Layout, buses []*ZBus) {
	const pipe, ptg = "pipe", "pipe-to-ground"
	maxUG := l.P.Entities[ptg].MaxUndergroundDistance
	tags := map[string]bool{}
	for _, bus := range buses {
		prefix := Tag(bus.Name) + "/"
		for _, p := range l.List {
			if (p.Name == pipe || p.Name == ptg) && strings.HasPrefix(p.Tag, prefix) {
				tags[p.Tag] = true
			}
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(tags)) {
		y, ok := pipeLaneRow(l, tag)
		if !ok {
			continue
		}
		var ends []int // входы пар (смотрят на запад) по ряду
		for _, p := range l.List {
			if p.Tag == tag && p.Name == ptg && p.Dir == layout.W && world.TileOf(p.Pos).Y == y {
				ends = append(ends, world.TileOf(p.Pos).X)
			}
		}
		slices.Sort(ends)
		var drop []world.Tile
		var add []layout.Placement
		for _, x := range ends {
			out := -1
			for k := x + 1; k <= x+maxUG+1; k++ {
				q := l.At(world.Tile{X: k, Y: y})
				if q == nil {
					continue
				}
				if q.Tag == tag && q.Name == ptg && q.Dir == layout.E {
					out = k
				}
				break
			}
			if out < 0 {
				continue // пара через занятое (между концами что-то стоит) — на месте
			}
			drop = append(drop, world.Tile{X: x, Y: y}, world.Tile{X: out, Y: y})
			for k := x; k <= out; k++ {
				add = append(add, layout.Placement{Name: pipe, Pos: world.Tile{X: k, Y: y}.Center(), Tag: tag})
			}
		}
		if len(drop) == 0 {
			continue
		}
		gone := map[world.Tile]bool{}
		for _, t := range drop {
			gone[t] = true
		}
		l.List = slices.DeleteFunc(l.List, func(p layout.Placement) bool {
			return p.Tag == tag && p.Name == ptg && gone[world.TileOf(p.Pos)]
		})
		l.Rollback(-1)
		for _, p := range add {
			if l.Free(world.TileOf(p.Pos), layout.AnyLand) {
				l.Add(p)
			}
		}
	}
}

// TankJoinPump — блок цистерн tank — к трубе шины tag через насос на входе (stages.md, стадия
// 51: склад наполняется, пока в трубе есть давление, и не отдаёт назад); насос не встаёт — без
// него (TankJoin). Столб у насоса — polesAlong.
func TankJoinPump(l *layout.Layout, tank *Cluster, tag string) error {
	own := Tag(tank.Name)
	for _, cn := range tankConns(l, tank) {
		pump, in, ok := inletPump(l, cn, own)
		if !ok {
			continue
		}
		mark := l.Mark()
		l.Add(pump)
		if _, err := l.JoinPipe(in, tag); err != nil {
			l.Rollback(mark)
			continue
		}
		polesAlong(l, []layout.Placement{pump}, own)
		return nil
	}
	return TankJoin(l, tank, tag)
}
