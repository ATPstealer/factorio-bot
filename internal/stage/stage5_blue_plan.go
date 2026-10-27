package stage

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 5 (stages.md): проект кластеров синей науки — план стадии 4 дописывается: добыча до 2M
// руды под бурами (месторождения целиком), дальше — сталь, быстрые ленты и инсертеры, нефть,
// двигатели, красные схемы, синяя наука (по шагам).

const (
	// blueBaseFile — план стадии 4 до дописывания: перезапуск стадии — с него
	blueBaseFile = "state-plan-green.json"
	oreTarget    = 2_000_000 // руды под бурами на ресурс (железо, медь, уголь)
	stoneTarget  = 500_000   // камня под бурами (кирпич; stages.md, стадия 5)
	// быстрые ленты и инсертеры, шт/с: ~10k лент и ~500 инсертеров за 2 ч (stages.md, стадия 5)
	fastBeltRate     = 1.4
	fastInserterRate = 0.07
	// refineries — НПЗ на простой переработке: газа с большим запасом (на синюю ~4/с, один НПЗ
	// даёт 9/с)
	refineries = 5
	chemPlants = 5 // химзаводов пластика и серы — не меньше MinUnits (cluster.md §2 «Размер»)
)

// blueOres — ресурсы, добыча которых растёт до oreTarget.
var blueOres = []string{"iron-ore", "copper-ore", "coal"}

func init() {
	register(Stage{N: 5, Name: "проект синей науки", Run: runBluePlan, Check: checkBluePlan, Plan: true})
}

// blueImage — картинка плана с проектом синей науки: run/stages/plan-blue-<сид>.png.
func blueImage() string { return image("plan-blue") }

// blueBase — план до дописывания (при первом запуске — копия плана стадии 4).
func blueBase() (*ClusterPlan, error) {
	if _, err := os.Stat(runPath(blueBaseFile)); err != nil {
		data, err := os.ReadFile(runPath(planFile))
		if err != nil {
			return nil, fmt.Errorf("нет плана кластеров (стадия 4): %w", err)
		}
		if err := os.WriteFile(runPath(blueBaseFile), data, 0o644); err != nil {
			return nil, err
		}
	}
	return loadPlanFile(runPath(blueBaseFile))
}

// bluePack — синяя наука; blueRate — её темп (вся наука — 0.1/с, stages.md).
const (
	bluePack = "chemical-science-pack"
	blueRate = 0.1
)

// blueClusters — двигатели (пары «трубы → двигатель»), красные схемы (пары «провод → схема»),
// синяя наука; остальное (сталь, пластик, сера, схемы, шестерни) — с шины.
func blueClusters(pr *knowledge.Prototypes) ([]*cluster.Cluster, error) {
	plan, err := calc.Solve(pr, map[string]float64{bluePack: blueRate}, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1", "advanced-crafting": "assembling-machine-2"}})
	if err != nil {
		return nil, err
	}
	cs, err := cluster.Split(pr, plan, planReserve, nil, append(slices.Clone(greenSeparate),
		"steel-plate", "plastic-bar", "sulfur", "advanced-circuit", "engine-unit", "iron-gear-wheel")...)
	if err != nil {
		return nil, err
	}
	by := map[string]*cluster.Cluster{}
	for _, c := range cs {
		if c.Kind == cluster.Make {
			by[c.Product] = c
		}
	}
	for _, it := range []string{"engine-unit", "pipe", "advanced-circuit", "copper-cable", bluePack} {
		if by[it] == nil {
			return nil, fmt.Errorf("синяя наука: в расчёте нет %s", it)
		}
	}
	return []*cluster.Cluster{cluster.Pair(pr, by["engine-unit"], by["pipe"]),
		cluster.Pair(pr, by["advanced-circuit"], by["copper-cable"]), by[bluePack]}, nil
}

// blueNeeds — потребность всей базы (красная + зелёная + синяя) кластерами с запасом — по ней
// рост старых кластеров (cluster.md §7).
func blueNeeds(pr *knowledge.Prototypes) ([]*cluster.Cluster, error) {
	targets := map[string]float64{redPack: redRate, greenPack: greenRate, bluePack: blueRate,
		"fast-transport-belt": fastBeltRate, "fast-inserter": fastInserterRate} // быстрые — их входы (жёлтые ленты 1.4/с) растут
	whole, err := calc.Solve(pr, targets, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1", "advanced-crafting": "assembling-machine-2"}})
	if err != nil {
		return nil, err
	}
	needs, err := cluster.Split(pr, whole, planReserve, nil, append([]string{"iron-gear-wheel"}, greenSeparate...)...)
	if err != nil {
		return nil, err
	}
	needs = append(needs, cluster.Labs(pr, targets, planLabSec, planReserve))
	return cluster.AddPower(pr, needs, planReserve, nil)
}

// oreUnder — руды под бурами ресурса it в кластерах плана.
func oreUnder(cs []*cluster.Cluster, it string) int {
	n := 0
	for _, c := range cs {
		if c.Kind == cluster.Mine && c.Product == it {
			n += c.OreUnder
		}
	}
	return n
}

func runBluePlan(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	rough, err := LoadRough()
	if err != nil {
		return err
	}
	old, err := blueBase()
	if err != nil {
		return err
	}
	// построенное по плану — препятствие (стадия 5 — до стройки: свои постройки на карте не мешают)
	var own []world.Object
	for _, o := range h.World.Objects() {
		if o.Own {
			own = append(own, o)
		}
	}
	restore := h.World.Hide(own)
	defer restore()
	built := builtOutside(h, old)
	avoid := map[world.Tile]bool{}
	for t := range built {
		avoid[t] = true
	}
	// кластер, к которому ленту не провести, — в другое место в своей зоне (до 6 попыток, как на
	// стадии 4)
	for try := 0; ; try++ {
		base, l, err := blueTry(h, rough, built, avoid)
		if err == nil {
			return nil
		}
		ce, ok := err.(*cluster.ConnectError)
		if !ok || try >= 5 {
			if base != nil && l != nil {
				planDraw(h, rough.Zones, base, l, blueImage()) // что вышло — на картинку
				if ok {
					drawAround(h, rough.Zones, base, l, ce.Consumer, image("plan-blue-fail"))
				}
			}
			return err
		}
		h.Say("кластер %s на %v: %v — ищу другое место", ce.Consumer.Name, ce.Consumer.Area, ce.Err)
		drawAround(h, rough.Zones, base, l, ce.Consumer, image(fmt.Sprintf("plan-blue-fail%d", try)))
		for _, z := range rough.Zones {
			if z.Name == "bus-1" && ce.Consumer.Kind == cluster.Mine { // подвод — у головы bus-1
				drawPlan(h, rough.Zones, base, l, z.Rect.Min, 60, image(fmt.Sprintf("plan-blue-head%d", try)))
			}
		}
		a := ce.Consumer.Area
		for y := a.Min.Y; y < a.Max.Y; y++ {
			for x := a.Min.X; x < a.Max.X; x++ {
				avoid[world.Tile{X: x, Y: y}] = true
			}
		}
	}
}

// blueTry — одна попытка проекта синей науки: план стадии 4 — заново из файла (прошлая попытка
// меняла кластеры), avoid — куда новые кластеры не ставить; удалось — план записан.
func blueTry(h *act.Hands, rough *RoughPlan, built, avoid map[world.Tile]bool) (*cluster.Base, *layout.Layout, error) {
	pr := h.Protos
	old, err := loadPlanFile(runPath(blueBaseFile))
	if err != nil {
		return nil, nil, err
	}
	base := cluster.NewBase(pr, h.World, world.Tile{})
	base.Log = h.Log
	base.Avoid = avoid
	var buses []*cluster.Cluster
	for _, c := range old.Clusters {
		if c.Kind == cluster.Bus {
			buses = append(buses, c)
		} else {
			base.Clusters = append(base.Clusters, c)
		}
	}
	_, zb, err := base.PlaceZoned(nil, rough.Zones)
	if err != nil {
		return nil, nil, err
	}
	cluster.FillLanes(zb, old.Layout, cluster.LaterNames(old.Clusters))
	// добыча до 2M, камень, кирпич — уже в плане стадии 3 (zones.md: ленты bus-1 — все сразу)
	var fresh []*cluster.Cluster
	// нефть — до конца игры: 2 месторождения, ближние к голове bus-1 (cluster.md §2а «Нефть»)
	zones, oil, err := base.MoreOil(rough.Zones)
	if err != nil {
		return nil, nil, err
	}
	if len(zones) > len(rough.Zones) {
		rough.Zones = zones
		if err := writeState(roughFile, rough); err != nil {
			return nil, nil, err
		}
	}
	fresh = append(fresh, oil...)
	// склад нефти — 6×5 цистерн под bus-1 у её головы, левее хранения (cluster.md §2а «Нефть»)
	var tank *cluster.Cluster
	for _, z := range rough.Zones {
		if z.Name == "bus-1" {
			tank = cluster.Tanks("crude-oil", 6, 5, world.Tile{X: z.Rect.Min.X + 10, Y: z.Rect.Max.Y + 12})
			tank.Name = "oil-tank-1"
		}
	}
	if tank == nil {
		return nil, nil, fmt.Errorf("нет зоны bus-1")
	}
	ps, _, err := base.PlaceZoned([]*cluster.Cluster{tank}, rough.Zones)
	if err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	// сталь — из руды, сразу на 30 пар печей (cluster.md §2а «Сталь»); длиннее глубины — кусками
	steel := cluster.Steel(pr, planReserve)
	if ps, _, err = base.PlaceZoned([]*cluster.Cluster{steel}, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	// НПЗ — простая переработка, только газ, с большим запасом (stages.md, стадия 5)
	refinery := cluster.Refinery(pr, refineries)
	if ps, _, err = base.PlaceZoned([]*cluster.Cluster{refinery}, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	// химия: пластик (газ + уголь), сера (газ + вода) — правее НПЗ на этаже переработки
	chems := []*cluster.Cluster{cluster.Chem(pr, "plastic-bar", chemPlants), cluster.Chem(pr, "sulfur", chemPlants)}
	if ps, _, err = base.PlaceZoned(chems, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	// быстрые ленты и инсертеры — на замену всего стоящего за ~2 ч (stages.md, стадия 5):
	// шестерни — внутри парами; ленты, инсертеры, схемы — с шины (их кластеры уже есть)
	fast, err := calc.Solve(pr, map[string]float64{"fast-transport-belt": fastBeltRate, "fast-inserter": fastInserterRate},
		calc.Options{All: true, Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return nil, nil, err
	}
	cs, err := cluster.Split(pr, fast, planReserve, nil, greenSeparate...)
	if err != nil {
		return nil, nil, err
	}
	var helpers []*cluster.Cluster
	for _, c := range cs {
		if c.Kind == cluster.Make && (c.Product == "fast-transport-belt" || c.Product == "fast-inserter") {
			helpers = append(helpers, c)
		}
	}
	if ps, _, err = base.PlaceZoned(helpers, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	// синяя наука: двигатели (трубы внутри, шестерни — с шины), красные схемы (провод внутри),
	// сама наука (stages.md, стадия 5)
	sci, err := blueClusters(pr)
	if err != nil {
		return nil, nil, err
	}
	if ps, _, err = base.PlaceZoned(sci, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	// склад газа — 5×4 у головы bus-2, на свободном месте (cluster.md §2а «Нефть»)
	var gasTank *cluster.Cluster
	for _, z := range rough.Zones {
		if z.Name == "bus-2" {
			gasTank = cluster.Tanks("petroleum-gas", 5, 4, world.Tile{X: z.Rect.Min.X - 15, Y: (z.Rect.Min.Y + z.Rect.Max.Y) / 2})
			gasTank.Name = "gas-tank-1"
		}
	}
	if gasTank == nil {
		return nil, nil, fmt.Errorf("нет зоны bus-2")
	}
	if ps, _, err = base.PlaceZoned([]*cluster.Cluster{gasTank}, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	layoutAll := func() (*layout.Layout, error) {
		l, err := base.Layout(fresh...)
		if err != nil {
			return l, err
		}
		for t := range built {
			delete(l.Reserved, t)
		}
		for _, pl := range old.Layout {
			l.Add(pl)
		}
		return l, nil
	}
	// рост под красную + зелёную + синюю (cluster.md §7): станция — первым делом (решение
	// пользователя), потом плавка; вдоль ряда, не влезло — кластер-2 того же продукта
	l, err := layoutAll()
	if err != nil {
		return base, l, err
	}
	needs, err := blueNeeds(pr)
	if err != nil {
		return base, l, err
	}
	grow := map[*cluster.Cluster]int{}
	var extras []*cluster.Cluster
	var stationOf *cluster.Cluster
	var stations []*cluster.Cluster
	order := slices.Clone(base.Clusters)
	slices.SortStableFunc(order, func(a, b *cluster.Cluster) int {
		rank := func(c *cluster.Cluster) int {
			if c.Kind == cluster.Power {
				return 0
			}
			return 1
		}
		return rank(a) - rank(b)
	})
	for _, c := range order {
		belts := c.Kind == cluster.Make && c.Product == "transport-belt" // жёлтые ленты — под быстрые (1.4/с)
		if c.Kind != cluster.Smelt && c.Kind != cluster.Power && !belts || slices.Contains(fresh, c) {
			continue
		}
		n, have := cluster.Need(needs, c), cluster.Built(l.List, c)
		if c.Kind == cluster.Power { // по тому, что реально стоит в плане (буры на 2M, НПЗ, химия)
			var w float64
			n, w = cluster.PowerNeed(pr, l)
			h.Say("энергия плана: %.1f МВт — бойлеров %d (стоит %d)", w/1e6, n, have)
		}
		// плавка железа под быстрые ленты — по расчёту (blueNeeds считает их входы), как у всех:
		// вдоль ряда, не влезло — ещё кластер (было удвоение старой — заплатка; stages.md, 30)
		h.Log("рост: %s — нужно %d, стоит %d", c.Name, n, have)
		if n <= have {
			continue
		}
		// вдоль ряда — до предела ряда (GrowCap: 30 машин, станция — бойлеров на одну помпу),
		// остальное — новыми кластерами (решение пользователя 26.10)
		m := min(n, base.GrowCap(c))
		if err := base.GrowRow(l, c, m); err == nil {
			h.Say("рост: %s — %s %d → %d вдоль ряда", c.Name, c.MainMachine(), have, m)
			grow[c] = m
		} else {
			h.Log("рост %s: %v", c.Name, err)
			m = have
			if c.Kind == cluster.Power { // вдоль ряда — сколько влезет
				for k := min(n, base.GrowCap(c)) - 1; k > have; k-- {
					if err := base.GrowRow(l, c, k); err == nil {
						grow[c], m = k, k
						break
					} else if k <= c.Room {
						h.Log("рост %s до %d: %v", c.Name, k, err)
					}
				}
			}
		}
		if m >= n {
			continue
		}
		if c.Kind == cluster.Power {
			// остальное — вторая станция у воды (своя помпа, уголь — отводом с ленты первой;
			// cluster.md §2а «Станция»)
			stationOf = c
			for rest := n - m; rest > 0; { // станции — не больше, чем кормит одна помпа
				k := min(rest, cluster.PumpBoilers(pr))
				st := cluster.Station(pr, k)
				if st == nil {
					return base, l, fmt.Errorf("станция на %d бойлеров не считается", k)
				}
				st.Name = base.Name("power", stations)
				x, y := c.Area.Center()
				near := world.Tile{X: int(x), Y: int(y)}
				st.Near = &near
				stations = append(stations, st)
				extras = append(extras, st)
				h.Say("рост: %s — %d → %d вдоль ряда, ещё станция %s на %d бойлеров", c.Name, have, m, st.Name, k)
				rest -= k
			}
			continue
		}
		xs, err := cluster.Extras(pr, needs, c, m, n, base.GrowCap(c))
		if err != nil {
			return base, l, fmt.Errorf("рост %s (%d → %d): %w", c.Name, m, n, err)
		}
		extras = append(extras, xs...)
	}
	if len(extras) > 0 {
		var err error
		if extras, _, err = base.PlaceZoned(extras, rough.Zones); err != nil {
			return base, l, err
		}
		for _, x := range extras {
			h.Say("рост: новый кластер %s", x.Name)
		}
		fresh = append(fresh, extras...)
	}
	if l, err = layoutAll(); err != nil {
		return base, l, err
	}
	for c, n := range grow {
		if err := base.GrowRow(l, c, n); err != nil {
			return base, l, err
		}
	}
	// синий — в лаборатории лентой B (cluster.md §2а «Лаборатории»)
	extra := map[*cluster.Cluster][]cluster.Port{}
	for _, c := range base.Clusters {
		if c.Kind == cluster.Lab && !slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == bluePack }) {
			p, err := cluster.AddLaneB(l, c, bluePack, blueRate*cluster.Margin, cluster.Tag(c.Name))
			if err != nil {
				return base, l, err
			}
			extra[c] = []cluster.Port{p}
		}
	}
	before := map[string]bool{}
	for _, e := range l.Validate() {
		before[e] = true
	}
	if err := base.ConnectMore(l, zb, fresh, extra); err != nil {
		return base, l, err
	}
	for _, st := range stations {
		if err := base.ConnectStation(l, st, stationOf); err != nil {
			return base, l, err
		}
	}
	// нефтетрассы: от каждого месторождения — в склад нефти
	for _, c := range base.Clusters {
		if c.IsOil() {
			if err := base.OilTrunk(l, c, tank); err != nil {
				return base, l, err
			}
			h.Say("нефтетрасса %s → %s", c.Name, tank.Name)
		}
	}
	// трубы шин: нефть — bus-1 (склад → НПЗ), газ — bus-2 (НПЗ → потребители)
	gasEnd := refinery.Area.Max.X + 20
	for _, c := range chems {
		gasEnd = max(gasEnd, c.Area.Max.X+10)
	}
	gas, err := base.RefineryPipes(l, zb, refinery, tank, gasEnd)
	if err != nil {
		return base, l, err
	}
	if err := base.ChemPipes(l, zb, chems, gas); err != nil {
		return base, l, err
	}
	if err := cluster.TankJoin(l, gasTank, gas); err != nil {
		return base, l, err
	}
	// трубы шин — подземные везде, где к ним ничего не подходит (решение пользователя)
	cluster.UnderPipes(l, zb)
	for _, e := range l.Validate() {
		if !before[e] && !strings.Contains(e, "столбы не связаны") {
			var x, y int // чья постройка — в ошибку (разбор)
			if _, err := fmt.Sscanf(e[strings.Index(e, "{"):], "{%d %d}", &x, &y); err == nil {
				if p := l.At(world.Tile{X: x, Y: y}); p != nil {
					e += " (" + p.Tag + ")"
				}
				drawPlan(h, rough.Zones, base, l, world.Tile{X: x, Y: y}, 30, image("plan-blue-err"))
				for _, p := range l.List { // что рядом — в журнал
					if t := world.TileOf(p.Pos); abs(t.X-x) <= 3 && abs(t.Y-y) <= 3 {
						h.Log("рядом с ошибкой: %v %s %s", t, p, p.Tag)
					}
				}
			}
			return base, l, fmt.Errorf("раскладка: %v", e)
		}
	}
	for _, it := range blueOres {
		h.Say("добыча %s: под бурами %d", it, oreUnder(base.Clusters, it))
	}
	if err := flowCheck(h, l, base.Clusters, true); err != nil { // синяя строится после стадии 30: будущие блоки уже есть
		return base, l, err
	}
	for _, c := range fresh {
		h.Say("кластер %v", c)
	}
	if err := planDraw(h, rough.Zones, base, l, blueImage()); err != nil {
		return base, l, err
	}
	// нефть крупно — для проверки глазами: НПЗ, химия, склад
	for _, c := range append([]*cluster.Cluster{refinery, tank}, chems...) {
		drawAround(h, rough.Zones, base, l, c, image("plan-blue-"+c.Name))
	}
	all := append(base.Sorted(), buses...)
	return base, l, writeState(planFile, ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: planImage()})
}

// checkBluePlan — под бурами каждого ресурса не меньше oreTarget; картинка есть.
func checkBluePlan(h *act.Hands) error {
	s, err := LoadBluePlan()
	if err != nil {
		return err
	}
	for _, it := range blueOres {
		if n := oreUnder(s.Clusters, it); n < oreTarget {
			return fmt.Errorf("добыча %s: под бурами %d, нужно %d", it, n, oreTarget)
		}
	}
	// кластеры проекта: продукт → сколько нужно
	want := map[string]int{"steel-plate": 1, "fast-transport-belt": 1, "fast-inserter": 1, "crude-oil": cluster.OilFields,
		"petroleum-gas": 1, "plastic-bar": 1, "sulfur": 1, "engine-unit": 1, "advanced-circuit": 1, bluePack: 1}
	got := map[string]int{}
	tanks := map[string]bool{}
	for _, c := range s.Clusters {
		if c.Kind == cluster.Tank {
			tanks[c.Product] = true
			continue
		}
		if c.Kind == cluster.Mine && c.Product != "crude-oil" {
			continue
		}
		got[c.Product]++
	}
	for it, n := range want {
		if got[it] < n {
			return fmt.Errorf("в плане %s: %d из %d", it, got[it], n)
		}
	}
	for _, fl := range []string{"crude-oil", "petroleum-gas"} {
		if !tanks[fl] {
			return fmt.Errorf("в плане нет склада %s", fl)
		}
	}
	if !slices.ContainsFunc(s.Clusters, func(c *cluster.Cluster) bool {
		return c.Kind == cluster.Lab && slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == bluePack })
	}) {
		return fmt.Errorf("у лабораторий нет входа %s", bluePack)
	}
	if st, err := os.Stat(blueImage()); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки %s", blueImage())
	}
	return nil
}
