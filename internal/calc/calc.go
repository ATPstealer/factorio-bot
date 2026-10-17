package calc

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"factorio-bot/internal/knowledge"
)

// Все скорости — в единицах в секунду.

// Options — что считать доступным и чем производить.
type Options struct {
	Researched []string          // изученные техи силы игрока
	All        bool              // всё доступно (план на будущее); недостающие техи всё равно считаются
	Machines   map[string]string // категория рецепта → принудительная машина
}

// Node — один рецепт (или добыча) в плане.
type Node struct {
	Recipe   string             // имя рецепта; добыча — "mine:<ресурс>", вода — "pump:water", внешний — "source:<предмет>"
	Machine  string             // где: сущность или "character"
	Crafts   float64            // крафтов (операций) в секунду
	Machines float64            // машин (дробно)
	In, Out  map[string]float64 // потоки в секунду
	PowerW   float64            // электричество
	Fuel     float64            // уголь/с для топливных машин
}

// Plan — результат расчёта.
type Plan struct {
	Targets map[string]float64
	Nodes   []*Node            // по рецептам, от целей к сырью
	Raw     map[string]float64 // сырьё с карты: руды, нефть, вода
	Sources map[string]float64 // предметы без рецепта (дерево, рыба…) — добывать вручную
	Surplus map[string]float64 // излишки (побочные продукты)
	PowerW  float64            // электричество всех машин
	Steam   Steam              // паровая станция под PowerW
	Fuel    float64            // уголь/с: топливные машины + бойлеры
	Belt    string
	BeltCap float64
	Belts   map[string]float64 // лент на поток предмета (только предметы)
	Techs   []string           // нужные неизученные техи в порядке исследования
	Cost    knowledge.TechCost
}

// Steam — паровая электростанция: помпы, бойлеры, двигатели.
type Steam struct {
	Engines, Boilers, Pumps float64
}

// Веса сырья в целевой функции: что «дороже» добывать. Определяют выбор рецепта при
// альтернативах (переработка нефти, твёрдое топливо…), на количество не влияют.
var rawCost = map[string]float64{
	"crude-oil":   0.05,
	"water":       0.001,
	"steam":       0.01,
	"uranium-ore": 10,
}

const (
	sourceCost = 1000.0 // предмет без рецепта и ресурса — крайний случай
	craftCost  = 1e-4   // «трение» рецепта: не плодить лишние циклы
	coalJ      = 4e6    // уголь, Дж
)

type column struct {
	name     string
	recipe   string // для настоящих рецептов
	resource string // для добычи
	net      map[string]float64
	cost     float64
}

// Solve считает план для целей targets (предмет → в секунду).
func Solve(p *knowledge.Prototypes, targets map[string]float64, o Options) (*Plan, error) {
	real := p.NewAvailability(o.Researched, false)
	av := p.NewAvailability(o.Researched, o.All)
	anyAv := p.NewAvailability(o.Researched, true)
	// crafter/drill — лучшая доступная машина, а если её нет — любая (тогда в план попадёт
	// исследование, которое её откроет)
	crafter := func(cat string) (knowledge.Machine, bool) {
		if m, ok := p.BestCrafter(cat, av, o.Machines[cat]); ok {
			return m, true
		}
		return p.BestCrafter(cat, anyAv, o.Machines[cat])
	}
	drill := func(cat string) (knowledge.Machine, bool) {
		if m, ok := p.BestDrill(cat, av); ok {
			return m, true
		}
		return p.BestDrill(cat, anyAv)
	}

	// обход назад от целей: предметы и способы их получить
	var cols []*column
	items := map[string]bool{}
	queue := make([]string, 0, len(targets))
	for it := range targets {
		queue = append(queue, it)
	}
	sort.Strings(queue)
	seenRecipe := map[string]bool{}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if items[it] {
			continue
		}
		items[it] = true
		add := func(c *column) {
			cols = append(cols, c)
			for in, v := range c.net {
				if v < 0 && !items[in] {
					queue = append(queue, in)
				}
			}
		}
		found := false
		// доступные рецепты; если у предмета доступных нет — закрытые (план покажет,
		// что исследовать)
		recipes := []string{}
		for _, r := range p.RecipesFor(it) {
			if p.Usable(r) && av.Recipe(r) {
				recipes = append(recipes, r)
			}
		}
		if len(recipes) == 0 {
			for _, r := range p.RecipesFor(it) {
				if p.Usable(r) {
					recipes = append(recipes, r)
				}
			}
		}
		for _, r := range recipes {
			if seenRecipe[r] {
				continue
			}
			rec := p.Recipes[r]
			if _, ok := crafter(rec.Category); !ok {
				continue
			}
			seenRecipe[r] = true
			found = true
			net := map[string]float64{}
			for _, in := range rec.Ingredients {
				net[in.Name] -= in.Mean()
			}
			for _, pr := range rec.Products {
				net[pr.Name] += pr.Mean()
			}
			add(&column{name: r, recipe: r, net: net, cost: craftCost})
		}
		if res, ok := p.Resource(it); ok {
			e := p.Entities[res]
			net := map[string]float64{}
			for _, pr := range e.Mineable.Products {
				net[pr.Name] += pr.Mean()
			}
			if e.Mineable.RequiredFluid != "" {
				net[e.Mineable.RequiredFluid] -= e.Mineable.FluidAmount / 10
			}
			w := 1.0
			if c, ok := rawCost[it]; ok {
				w = c
			}
			add(&column{name: "mine:" + res, resource: res, net: net, cost: w * net[it]})
			found = true
		}
		if it == "water" {
			add(&column{name: "pump:water", net: map[string]float64{"water": 1}, cost: rawCost["water"]})
			found = true
		}
		if it == "steam" {
			add(&column{name: "boil:steam", net: map[string]float64{"steam": 1, "water": -1}, cost: rawCost["steam"]})
			found = true
		}
		if !found {
			add(&column{name: "source:" + it, net: map[string]float64{it: 1}, cost: sourceCost})
		}
	}

	// задача: Σ net·x ≥ цель для каждого предмета, min Σ cost·x
	// строки — все предметы, встречающиеся в способах (и побочные продукты — для излишков)
	rows := map[string]bool{}
	for it := range targets {
		rows[it] = true
	}
	for _, c := range cols {
		for it := range c.net {
			rows[it] = true
		}
	}
	names := make([]string, 0, len(rows))
	for it := range rows {
		names = append(names, it)
	}
	sort.Strings(names)
	A := make([][]float64, len(names))
	bvec := make([]float64, len(names))
	cvec := make([]float64, len(cols))
	for i, it := range names {
		A[i] = make([]float64, len(cols))
		for j, c := range cols {
			A[i][j] = c.net[it]
		}
		bvec[i] = targets[it]
	}
	for j, c := range cols {
		cvec[j] = c.cost
	}
	x, err := Minimize(cvec, A, bvec)
	if err != nil {
		return nil, err
	}

	plan := &Plan{Targets: targets, Raw: map[string]float64{}, Sources: map[string]float64{},
		Surplus: map[string]float64{}, Belts: map[string]float64{}}
	var used []string
	produced := map[string]float64{}
	for j, c := range cols {
		if x[j] < 1e-9 {
			continue
		}
		n := &Node{Recipe: c.name, Crafts: x[j], In: map[string]float64{}, Out: map[string]float64{}}
		for it, v := range c.net {
			if v > 0 {
				n.Out[it] = v * x[j]
				produced[it] += v * x[j]
			} else if v < 0 {
				n.In[it] = -v * x[j]
			}
		}
		switch {
		case c.recipe != "":
			rec := p.Recipes[c.recipe]
			m, _ := crafter(rec.Category)
			n.Machine = m.Name
			n.Machines = x[j] * rec.Energy / m.Speed
			used = append(used, c.recipe)
			if m.Name != "character" {
				used = append(used, machineRecipes(p, m.Name)...)
			}
		case c.resource != "":
			e := p.Entities[c.resource]
			for it, v := range n.Out {
				plan.Raw[it] += v
			}
			if d, ok := drill(e.ResourceCategory); ok {
				n.Machine = d.Name
				n.Machines = x[j] * e.Mineable.MiningTime / d.Speed
				used = append(used, machineRecipes(p, d.Name)...)
			}
		case c.name == "pump:water":
			plan.Raw["water"] += x[j]
			if pe, ok := p.Entities["offshore-pump"]; ok && pe.PumpingSpeed > 0 {
				n.Machine = "offshore-pump"
				n.Machines = x[j] / (pe.PumpingSpeed * 60)
			}
		case strings.HasPrefix(c.name, "source:"):
			plan.Sources[strings.TrimPrefix(c.name, "source:")] += x[j]
		}
		energy(p, n)
		plan.PowerW += n.PowerW
		plan.Fuel += n.Fuel
		plan.Nodes = append(plan.Nodes, n)
	}
	for i, it := range names {
		s := -bvec[i]
		for j := range cols {
			s += A[i][j] * x[j]
		}
		if s > 1e-6 {
			plan.Surplus[it] = s
		}
	}

	plan.Steam = steamFor(p, plan.PowerW)
	if b, ok := p.Entities["boiler"]; ok && b.Burner != nil {
		plan.Fuel += plan.Steam.Boilers * b.MaxEnergyUsage * 60 / (coalJ * b.Burner.Effectivity)
	}
	plan.Belt, plan.BeltCap = p.BestBelt(av)
	if plan.BeltCap > 0 {
		for it, v := range produced {
			if !p.IsFluid(it) {
				plan.Belts[it] = v / plan.BeltCap
			}
		}
	}

	// недостающие исследования — относительно реально изученного
	techs := p.TechsFor(used, real)
	plan.Techs, err = p.TechPath(techs, real.Researched)
	if err != nil {
		return nil, err
	}
	plan.Cost = p.Cost(plan.Techs)
	sortNodes(plan)
	return plan, nil
}

// machineRecipes — рецепты предмета, ставящего сущность (чтобы учесть техи на саму машину).
func machineRecipes(p *knowledge.Prototypes, entity string) []string {
	var res []string
	for _, it := range p.Entities[entity].ItemsToPlaceThis {
		res = append(res, p.RecipesFor(it)...)
	}
	return res
}

// energy — электричество (с простоем целых машин) или уголь узла.
func energy(p *knowledge.Prototypes, n *Node) {
	e, ok := p.Entities[n.Machine]
	if !ok || n.Machine == "character" {
		return
	}
	use := e.EnergyUsage
	if use == 0 {
		use = e.MaxEnergyUsage
	}
	switch {
	case e.Electric:
		n.PowerW = n.Machines*use*60 + math.Ceil(n.Machines-1e-9)*e.Drain*60
	case e.Burner != nil:
		n.Fuel = n.Machines * use * 60 / (coalJ * e.Burner.Effectivity)
	}
}

// steamFor — паровая станция под мощность W: двигатели по их выработке, бойлеры по
// соотношению мощностей, помпы — по расходу воды бойлера (60/с в 2.0: 1.8 МВт на нагрев
// воды 15→165 °C при теплоёмкости 0.2 кДж/°C).
func steamFor(p *knowledge.Prototypes, W float64) Steam {
	eng, b, pump := p.Entities["steam-engine"], p.Entities["boiler"], p.Entities["offshore-pump"]
	if W <= 0 || eng.MaxEnergyProduction == 0 || b.MaxEnergyUsage == 0 {
		return Steam{}
	}
	s := Steam{Engines: W / (eng.MaxEnergyProduction * 60)}
	s.Boilers = s.Engines * eng.MaxEnergyProduction / b.MaxEnergyUsage
	if pump.PumpingSpeed > 0 {
		water := b.MaxEnergyUsage * 60 / (200 * 150) // единиц воды/с на бойлер
		s.Pumps = s.Boilers * water / (pump.PumpingSpeed * 60)
	}
	return s
}

// sortNodes — порядок узлов: от целей вниз по глубине (BFS по входам).
func sortNodes(plan *Plan) {
	producers := map[string][]*Node{}
	for _, n := range plan.Nodes {
		for it := range n.Out {
			producers[it] = append(producers[it], n)
		}
	}
	depth := map[*Node]int{}
	var q []*Node
	tg := make([]string, 0, len(plan.Targets))
	for it := range plan.Targets {
		tg = append(tg, it)
	}
	sort.Strings(tg)
	for _, it := range tg {
		for _, n := range producers[it] {
			if _, ok := depth[n]; !ok {
				depth[n] = 0
				q = append(q, n)
			}
		}
	}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		ins := make([]string, 0, len(n.In))
		for it := range n.In {
			ins = append(ins, it)
		}
		sort.Strings(ins)
		for _, it := range ins {
			for _, m := range producers[it] {
				if _, ok := depth[m]; !ok {
					depth[m] = depth[n] + 1
					q = append(q, m)
				}
			}
		}
	}
	sort.SliceStable(plan.Nodes, func(i, j int) bool {
		a, b := plan.Nodes[i], plan.Nodes[j]
		if depth[a] != depth[b] {
			return depth[a] < depth[b]
		}
		return a.Recipe < b.Recipe
	})
}

// Producers — узлы, производящие предмет (для вывода дерева).
func (pl *Plan) Producers(item string) []*Node {
	var res []*Node
	for _, n := range pl.Nodes {
		if n.Out[item] > 0 {
			res = append(res, n)
		}
	}
	return res
}

// String — краткая строка узла для логов.
func (n *Node) String() string {
	return fmt.Sprintf("%s ×%.2f %s", n.Recipe, n.Machines, n.Machine)
}
