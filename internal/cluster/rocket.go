package cluster

import (
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
)

// Ракета (stages.md, стадии 64 и 66; cluster.md «Сборка», «Ракетная шахта»).

const (
	rocketFuel = "rocket-fuel"
	solidFuel  = "solid-fuel-from-light-oil"
	rocketPart = "rocket-part"
	rocketSilo = "rocket-silo"
)

// RocketFuel — ракетное топливо тройками (cluster.md «Сборка»): сборщик 2 ← химзавод (твёрдое
// топливо из лёгкого масла) → сборщик 2; n троек — 2n сборщиков, n химзаводов; лёгкое масло —
// трубой ко всем.
func RocketFuel(p *knowledge.Prototypes, n int) *Cluster {
	main, sub := p.Recipes[rocketFuel], p.Recipes[solidFuel]
	asm, chem := "assembling-machine-2", "chemical-plant"
	crafts := float64(2*n) * p.Entities[asm].CraftingSpeed / main.Energy // ракетного топлива в секунду
	in, internal := map[string]float64{}, map[string]float64{}
	for _, i := range main.Ingredients {
		if i.Name == product(sub) {
			internal[i.Name] += amountOf(i) * crafts
		} else {
			in[i.Name] += amountOf(i) * crafts
		}
	}
	subCrafts := internal[product(sub)] / amount(sub.Products, product(sub))
	for _, i := range sub.Ingredients {
		in[i.Name] += amountOf(i) * subCrafts
	}
	c := &Cluster{Kind: Make, Product: rocketFuel, Reserve: 1, Main: rocketFuel, Sub: solidFuel, SubPer: 1, SubMid: true,
		Units: []Unit{{Recipe: rocketFuel, Machine: asm, Machines: float64(2 * n)}, {Recipe: solidFuel, Machine: chem, Machines: float64(n)}},
		In:    toFlows(in), Internal: toFlows(internal), Out: toFlows(map[string]float64{rocketFuel: crafts}),
		PowerW: float64(2*n)*p.Entities[asm].EnergyUsage*60 + float64(n)*p.Entities[chem].EnergyUsage*60}
	c.Cross, c.Pitch, c.Head = rocketFuelCross, rocketFuelPitch, 1
	c.Count, c.Room, c.Need = n, n, n
	return c
}

// Ряды тройки ракетного топлива (rocketFuelRow): 0 — труба лёгкого масла, 1–3 — машины, 4 —
// инсертеры выхода, 5 — лента выхода. Вдоль: сборщик (3), инсертер, химзавод (3), инсертер,
// сборщик (3), ряд под столб.
const rocketFuelCross, rocketFuelPitch = 6, 12

func rocketFuelRow(l *layout.Layout, f frame, c *Cluster, L int, tag string, add func(layout.Placement) error,
	row func(v, from, to, d int) error) error {
	const pipeV, machV, insV, outV = 0, 1, 4, 5
	fluid := "light-oil"
	rowTag := tag + "/" + fluid
	machine := func(name, recipe string, u int) error {
		d, err := f.facing(l, name, u, machV, pipeV, L, c.Cross)
		if err != nil {
			return err
		}
		m := f.at(l, name, u, machV, d, tag)
		m.Recipe = recipe
		return add(m)
	}
	direct := func(at, from int) error { // инсертер в среднем ряду машин: из химзавода в сборщик
		a := f.tile(at, machV+1)
		return add(layout.Placement{Name: "inserter", Pos: a.Center(), Dir: layout.InserterDir(l.P, "inserter", a, f.tile(from, machV+1)), Tag: tag})
	}
	end := c.Head + c.Count*c.Pitch
	for i := 0; i < c.Count; i++ {
		u0 := c.Head + i*c.Pitch
		for _, m := range []struct {
			name, recipe string
			u            int
		}{{"assembling-machine-2", rocketFuel, u0}, {"chemical-plant", solidFuel, u0 + 4}, {"assembling-machine-2", rocketFuel, u0 + 8}} {
			if err := machine(m.name, m.recipe, m.u); err != nil {
				return err
			}
		}
		if err := direct(u0+3, u0+4); err != nil {
			return err
		}
		if err := direct(u0+7, u0+6); err != nil {
			return err
		}
		for _, u := range []int{u0 + 1, u0 + 9} { // выход сборщиков — на ленту
			if err := add(f.inserter(l, "inserter", u, insV, machV+2, tag)); err != nil {
				return err
			}
		}
	}
	// лёгкое масло — со стороны его шины (bus-2 под снабжением: у головы)
	if err := fluidPortAt(l, f, c, pipeV, c.Head, end, L, fluid, rowTag, true, nil, add); err != nil {
		return err
	}
	if err := row(outV, c.Head, L, layout.E); err != nil {
		return err
	}
	// буфер выхода — в зазоре перед последней тройкой: сундук в ряду машин, инсертер с ленты выхода
	if err := outBuffer(l, f, c, c.Head+(c.Count-1)*c.Pitch-1, insV, outV, machV+2, tag, add); err != nil {
		return err
	}
	l.AllowEnd(f.tile(L, outV))
	c.Ports = append(c.Ports, Port{T: f.tile(L, outV), Dir: f.dir(layout.E), Item: rocketFuel})
	return nil
}

// Silo — ракетная шахта (cluster.md «Ракетная шахта»): 9×9, процессоры и ЛПС — одной лентой по
// полосам, ракетное топливо — второй; выхода нет. Потоки — по тому, что даёт план (ракетного
// топлива 0.2/с — 0.02 части ракеты в секунду), а не по шахте на полном ходу (3.3/с каждого):
// шахта ждёт подачу.
func Silo(p *knowledge.Prototypes) *Cluster {
	const parts = 0.02
	in := map[string]float64{}
	for _, i := range p.Recipes[rocketPart].Ingredients {
		in[i.Name] += amountOf(i) * parts
	}
	c := &Cluster{Kind: Make, Product: rocketPart, Reserve: 1,
		Units:  []Unit{{Recipe: rocketPart, Machine: rocketSilo, Machines: 1}},
		In:     toFlows(in),
		PowerW: p.Entities[rocketSilo].EnergyUsage * 60}
	c.Cross, c.Pitch, c.Head = siloCross, siloSize, 1
	c.Count, c.Room, c.Need = 1, 1, 1
	return c
}

// IsSilo — ракетная шахта.
func (c *Cluster) IsSilo() bool { return c.machine() == rocketSilo }

// Ряды шахты (siloBlock): 1 — лента A (процессоры | ЛПС по полосам; подачи боком в голову из
// рядов 0 и 2), 2 — инсертеры с A, 3–11 — шахта, 12 — инсертеры с B, 13 — лента B (ракетное
// топливо, подача с торца).
const siloSize, siloCross = 9, 14

func siloBlock(l *layout.Layout, f frame, c *Cluster, tag string, add func(layout.Placement) error,
	belt func(u, v, d int) error, row func(v, from, to, d int) error, deadEnd func(v, to int), sideIn func(v int) error) error {
	const aV, aInsV, siloV, bInsV, bV = 1, 2, 3, 12, 13
	m := f.at(l, rocketSilo, c.Head, siloV, layout.N, tag)
	m.Recipe = rocketPart
	if err := add(m); err != nil {
		return err
	}
	mid := c.Head + siloSize/2
	if err := add(f.inserter(l, "inserter", mid, aInsV, aV, tag)); err != nil {
		return err
	}
	if err := add(f.inserter(l, "inserter", mid, bInsV, bV, tag)); err != nil {
		return err
	}
	// лента A: два первых входа (процессоры, ЛПС) — боком в голову по полосам
	if err := sideIn(aV); err != nil {
		return err
	}
	in := c.In
	last := c.Head + siloSize - 1
	if err := row(aV, 1, last, layout.E); err != nil {
		return err
	}
	deadEnd(aV, last)
	// лента B: третий вход (ракетное топливо) — с торца
	if len(in) > 2 {
		if err := belt(0, bV, layout.E); err != nil {
			return err
		}
		if err := row(bV, 1, last, layout.E); err != nil {
			return err
		}
		deadEnd(bV, last)
		c.Ports = append(c.Ports, Port{T: f.tile(-1, bV), Dir: f.dir(layout.E), Item: in[2].Item, In: true})
	}
	return nil
}
