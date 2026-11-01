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

// Стадия 7 (stages.md): проект кластеров жёлтой науки — план стадии 6 дописывается: НПЗ‑2
// (продвинутая переработка), склады тяжёлого и лёгкого, крекинг тяжёлого, смазка, серная
// кислота и её склад, батареи, электродвигатели, рамы роботов, ЛПС, процессоры, жёлтая наука;
// трубы жидкостей — bus-1 (нефть, вода) и bus-2 (тяжёлое, лёгкое, газ, смазка, кислота);
// лаборатории — жёлтая на вторую полосу ленты C; нефть — третье месторождение; рост под всю
// науку. Всё новое — второго уровня. Стройка — стадии 49–61.

const (
	// yellowBaseFile — план стадии 6 до дописывания: перезапуск стадии — с него; стройка 42–48 —
	// по нему (LoadPurplePlan), 49–61 — по всему (LoadWholePlan)
	yellowBaseFile  = "state-plan-purple.json"
	yellowPack      = "utility-science-pack"
	yellowRate      = 0.1 // вся наука — 0.1/с (stages.md)
	yellowOilFields = 3   // месторождений нефти с жёлтой (stages.md, стадия 7: ещё качалки)
	ldsMin          = 6   // ЛПС — не меньше 6 сборщиков (решение пользователя: и на ракету)
)

// yellowMachines — машины расчёта: второй уровень; сборка с жидкостью — сборщики 2.
var yellowMachines = map[string]string{"smelting": "steel-furnace", "crafting": "assembling-machine-2",
	"advanced-crafting": "assembling-machine-2", "crafting-with-fluid": "assembling-machine-2"}

// yellowSeparate — с шины (свои кластеры), не парами внутри потребителя.
var yellowSeparate = append([]string{"battery", "electric-engine-unit", "flying-robot-frame", "low-density-structure",
	"processing-unit", "sulfuric-acid", "lubricant", "heavy-oil", "light-oil", "petroleum-gas"}, purpleSeparate...)

// yellowOrder — новые кластеры сборки: поставщики раньше потребителя (zones.md: потребитель правее).
var yellowOrder = []string{"battery", "electric-engine-unit", "flying-robot-frame", "low-density-structure", "processing-unit", yellowPack}

func init() {
	register(Stage{N: 7, Name: "проект жёлтой науки", Run: runYellowPlan, Check: checkYellowPlan, Plan: true})
}

// yellowImage — картинка плана с проектом жёлтой науки: run/stages/plan-yellow-<сид>.png.
func yellowImage() string { return image("plan-yellow") }

// yellowBase — план до дописывания (при первом запуске — копия плана стадии 6).
func yellowBase() (*ClusterPlan, error) {
	if _, err := os.Stat(runPath(yellowBaseFile)); err != nil {
		data, err := os.ReadFile(runPath(planFile))
		if err != nil {
			return nil, fmt.Errorf("нет плана кластеров (стадия 6): %w", err)
		}
		if err := os.WriteFile(runPath(yellowBaseFile), data, 0o644); err != nil {
			return nil, err
		}
	}
	return loadPlanFile(runPath(yellowBaseFile))
}

// yellowGroups — новые кластеры проекта по группам размещения (слева направо: поставщики раньше
// потребителей): переработка — НПЗ‑2, смазка и крекинг (смазка выше по трубе тяжёлого),
// кислота; снабжение — батареи (химия), электродвигатели, рамы, ЛПС, процессоры; конечный — жёлтая.
func yellowGroups(pr *knowledge.Prototypes) ([][]*cluster.Cluster, error) {
	plan, err := calc.Solve(pr, map[string]float64{yellowPack: yellowRate}, calc.Options{All: true, Machines: yellowMachines})
	if err != nil {
		return nil, err
	}
	cs, err := cluster.Split(pr, plan, planReserve, nil, yellowSeparate...)
	if err != nil {
		return nil, err
	}
	by := map[string]*cluster.Cluster{}
	for _, c := range cs {
		if c.Kind == cluster.Make {
			by[c.Product] = c
		}
	}
	for _, it := range yellowOrder {
		if by[it] == nil {
			return nil, fmt.Errorf("жёлтая наука: в расчёте нет %s", it)
		}
	}
	// жидкости — свои кластеры по темпу, не меньше 2 (кислота — 3, решение пользователя);
	// тяжёлое — на смазку и крекинг с запасом: НПЗ по потребности смазки, не меньше 2
	need := func(product string) float64 {
		n := 0.0
		for _, c := range cs {
			for _, in := range c.In {
				if in.Item == product {
					n += in.Rate
				}
			}
		}
		return n * cluster.Margin
	}
	count := func(machine, recipe string, rate float64, min int) int {
		r := pr.Recipes[recipe]
		per := pr.Entities[machine].CraftingSpeed / r.Energy * r.Products[0].Amount // продукта в секунду с машины
		return max(int(rate/per+0.999), min)
	}
	refAdv := cluster.RefineryAdv(pr, count("oil-refinery", "advanced-oil-processing", need("heavy-oil"), 2))
	lube := cluster.ChemFluid(pr, "lubricant", count("chemical-plant", "lubricant", need("lubricant"), 2))
	crack := cluster.ChemFluid(pr, "heavy-oil-cracking", 2)
	acid := cluster.ChemFluid(pr, "sulfuric-acid", max(count("chemical-plant", "sulfuric-acid", need("sulfuric-acid"), 2), 3))
	acid.HeadAbove = true // сера и железо — с bus-2 над переработкой: голова (подачи, буферы) — сверху
	battery := cluster.Chem(pr, "battery", count("chemical-plant", "battery", need("battery"), cluster.MinUnits))
	battery.HeadBuffers() // буферы железа и меди в голове (cluster.md §6)
	for _, c := range []*cluster.Cluster{by["electric-engine-unit"], by["processing-unit"]} {
		cluster.FluidMake(c)
	}
	for _, c := range []*cluster.Cluster{by["flying-robot-frame"], by["low-density-structure"], by[yellowPack]} {
		c.ByRate() // по темпу, не меньше 5 (cluster.md §2 «Размер», решение пользователя)
	}
	if lds := by["low-density-structure"]; lds.Count < ldsMin {
		lds.Count, lds.Room = ldsMin, ldsMin
	}
	for _, o := range []struct {
		c     *cluster.Cluster
		items []string
	}{
		{acid, []string{"sulfur", "iron-plate", "water"}},
		{battery, []string{"iron-plate", "copper-plate", "sulfuric-acid"}},
		{by["electric-engine-unit"], []string{"engine-unit", "electronic-circuit", "lubricant"}},
		// лента входа — сталь (снизу, bus-2) | зелёные (сверху, bus-3); лента C — электродвигатели |
		// батареи (сверху, bus-3) — stages.md, стадия 57
		{by["flying-robot-frame"], []string{"steel-plate", "electronic-circuit", "electric-engine-unit", "battery"}},
		{by["low-density-structure"], []string{"copper-plate", "plastic-bar", "steel-plate"}},
		{by["processing-unit"], []string{"electronic-circuit", "advanced-circuit", "sulfuric-acid"}},
		{by[yellowPack], []string{"flying-robot-frame", "low-density-structure", "processing-unit"}},
	} {
		if err := orderIn(o.c, o.items...); err != nil {
			return nil, err
		}
	}
	return [][]*cluster.Cluster{
		{refAdv}, {lube, crack}, {acid}, {battery},
		{by["electric-engine-unit"]}, {by["flying-robot-frame"]}, {by["low-density-structure"]}, {by["processing-unit"]},
		{by[yellowPack]},
	}, nil
}

// yellowNeeds — потребность всей базы (вся наука до жёлтой, быстрые ленты и инсертеры)
// кластерами с запасом, машины — второго уровня: по ней рост старых кластеров (cluster.md §7).
func yellowNeeds(pr *knowledge.Prototypes) ([]*cluster.Cluster, error) {
	targets := map[string]float64{redPack: redRate, greenPack: greenRate, bluePack: blueRate, purplePack: purpleRate,
		yellowPack: yellowRate, "fast-transport-belt": fastBeltRate, "fast-inserter": fastInserterRate}
	whole, err := calc.Solve(pr, targets, calc.Options{All: true, Machines: yellowMachines})
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

func runYellowPlan(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	rough, err := LoadRough()
	if err != nil {
		return err
	}
	old, err := yellowBase()
	if err != nil {
		return err
	}
	// построенное по плану — препятствие (стадия 7 — до стройки: свои постройки на карте не мешают)
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
	// стадиях 4–6)
	fails := map[string]int{}    // неудач подвода у кластера
	var wide map[world.Tile]bool // тайлы последнего запрета части зоны (откат, если места не стало)
	var wideArea cluster.Rect
	for try := 0; ; try++ {
		base, l, err := yellowTry(h, rough, built, avoid)
		if err == nil {
			return nil
		}
		ce, ok := err.(*cluster.ConnectError)
		if _, nosite := err.(*cluster.NoSiteError); (nosite || strings.Contains(err.Error(), "нет места")) && wide != nil && try < 9 {
			// под запретом части зоны места не осталось (427965634: процессоры) — запрет части
			// снять, взамен — площадь кластера с запасом
			for t := range wide {
				delete(avoid, t)
			}
			g := wideArea.Grow(15)
			for y := g.Min.Y; y < g.Max.Y; y++ {
				for x := g.Min.X; x < g.Max.X; x++ {
					avoid[world.Tile{X: x, Y: y}] = true
				}
			}
			h.Say("под запретом части зоны места нет — запрет снят, взамен %v", g)
			wide = nil
			continue
		}
		if !ok || try >= 9 {
			if base != nil && l != nil {
				planDraw(h, rough.Zones, base, l, yellowImage()) // что вышло — на картинку
				if ok {
					drawAround(h, rough.Zones, base, l, ce.Consumer, image("plan-yellow-fail"))
				}
			}
			return err
		}
		h.Say("кластер %s на %v: %v — ищу другое место", ce.Consumer.Name, ce.Consumer.Area, ce.Err)
		drawAround(h, rough.Zones, base, l, ce.Consumer, image(fmt.Sprintf("plan-yellow-fail%d", try)))
		a := ce.Consumer.Area
		// второй отказ того же кластера — вся часть зоны, где он стоит, под запрет: соседнее место
		// в той же тесной части (стопки, коридоры заняты подводами соседей) не помогает, а часть
		// зоны правее бывает пуста (158846952: рамы роботов 4 раза по 11 тайлов вправо)
		if fails[ce.Consumer.Name]++; fails[ce.Consumer.Name] >= 2 && ce.Consumer.Zone != nil {
			// других частей этажа нет — вся часть под запретом не оставит места (427965634:
			// снабжение — одна часть 316×94): тогда — площадь кластера с запасом
			parts := 0
			var kind cluster.ZoneKind
			for _, z := range rough.Zones {
				if z.Rect == *ce.Consumer.Zone {
					kind = z.Kind
				}
			}
			for _, z := range rough.Zones {
				if kind != "" && z.Kind == kind {
					parts++
				}
			}
			if parts > 1 {
				wideArea = a
				a = *ce.Consumer.Zone
				wide = map[world.Tile]bool{}
			} else {
				a = a.Grow(15)
			}
			h.Say("кластер %s: %v — под запрет", ce.Consumer.Name, a)
		}
		for y := a.Min.Y; y < a.Max.Y; y++ {
			for x := a.Min.X; x < a.Max.X; x++ {
				t := world.Tile{X: x, Y: y}
				if wide != nil && !avoid[t] {
					wide[t] = true
				}
				avoid[t] = true
			}
		}
	}
}

// yellowTry — одна попытка проекта жёлтой науки: план стадии 6 — заново из файла (прошлая
// попытка меняла кластеры), avoid — куда новые кластеры не ставить; удалось — план записан.
func yellowTry(h *act.Hands, rough *RoughPlan, built, avoid map[world.Tile]bool) (*cluster.Base, *layout.Layout, error) {
	pr := h.Protos
	old, err := loadPlanFile(runPath(yellowBaseFile))
	if err != nil {
		return nil, nil, err
	}
	// быстрые ленты стадии 6 — на время разметки обычными (разметка шин узнаёт ленты по ним),
	// в конце — назад
	var back cluster.TierBack
	old.Layout, back = cluster.Tier1Belts(old.Layout)
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
	var fresh []*cluster.Cluster
	// нефть — третье месторождение (stages.md, стадия 7); нет его на карте — с тем, что есть
	zones, oil, err := base.MoreOilN(rough.Zones, yellowOilFields)
	if err != nil {
		h.Say("нефть: %v — качаем с того, что есть", err)
	}
	if len(zones) > len(rough.Zones) {
		rough.Zones = zones
		if err := writeState(roughFile, rough); err != nil {
			return nil, nil, err
		}
	}
	fresh = append(fresh, oil...)
	// новые кластеры — в свои зоны группами слева направо
	groups, err := yellowGroups(pr)
	if err != nil {
		return nil, nil, err
	}
	var sci []*cluster.Cluster
	for _, g := range groups {
		ps, _, err := base.PlaceZoned(g, rough.Zones)
		if err != nil {
			return nil, nil, err
		}
		fresh = append(fresh, ps...)
		sci = append(sci, ps...)
	}
	// склады: тяжёлое, лёгкое (лёгкое — на всю игру, не меньше 4 цистерн) и кислота — у головы
	// bus-2 в начале своих труб (stages.md, стадии 51, 54; cluster.md §2а «Нефть»)
	var tanks []*cluster.Cluster
	for _, z := range rough.Zones {
		if z.Name == "bus-2" {
			near := world.Tile{X: z.Rect.Min.X - 15, Y: (z.Rect.Min.Y + z.Rect.Max.Y) / 2}
			for _, t := range []struct {
				fluid      string
				cols, rows int
			}{{"heavy-oil", 2, 2}, {"light-oil", 2, 2}, {"sulfuric-acid", 1, 2}} {
				tank := cluster.Tanks(t.fluid, t.cols, t.rows, near)
				tank.Name = strings.TrimSuffix(t.fluid, "-oil") + "-tank-1"
				tanks = append(tanks, tank)
			}
		}
	}
	if len(tanks) == 0 {
		return nil, nil, fmt.Errorf("нет зоны bus-2")
	}
	ps, _, err := base.PlaceZoned(tanks, rough.Zones)
	if err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, ps...)
	layoutAll := func() (*layout.Layout, error) {
		// старый план — в раскладке первым: внутренности новых кластеров его не занимают
		base.Pre = old.Layout
		l, err := base.Layout(fresh...)
		base.Pre = nil
		if err != nil {
			return l, err
		}
		for t := range built {
			delete(l.Reserved, t)
		}
		return l, nil
	}
	l, err := layoutAll()
	if err != nil {
		return base, l, err
	}
	// рост под всю науку (cluster.md §7; стоящее считается вторым уровнем)
	needs, err := yellowNeeds(pr)
	if err != nil {
		return base, l, err
	}
	g, err := growProject(h, pr, base, &l, needs, rough.Zones, &fresh, layoutAll)
	if err != nil {
		return base, l, err
	}
	// жёлтая — в лаборатории на вторую полосу ленты C (cluster.md §2а «Лаборатории»)
	extra := map[*cluster.Cluster][]cluster.Port{}
	var labs []*cluster.Cluster
	for _, c := range base.Clusters {
		if c.Kind == cluster.Lab && !slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == yellowPack }) {
			p, err := cluster.AddLaneC(l, c, yellowPack, yellowRate*cluster.Margin, cluster.Tag(c.Name))
			if err != nil {
				return base, l, err
			}
			extra[c] = []cluster.Port{p}
			labs = append(labs, c)
		}
	}
	before := map[string]bool{}
	for _, e := range l.Validate() {
		before[e] = true
	}
	if err := base.ConnectMore(l, zb, fresh, extra); err != nil {
		return base, l, err
	}
	for _, st := range g.stations {
		if err := base.ConnectStation(l, st, g.stationOf); err != nil {
			return base, l, err
		}
	}
	// нефтетрассы новых месторождений — в склад нефти
	var crudeTank *cluster.Cluster
	for _, c := range base.Clusters {
		if c.Kind == cluster.Tank && c.Product == "crude-oil" {
			crudeTank = c
		}
	}
	for _, c := range oil {
		if crudeTank == nil {
			return base, l, fmt.Errorf("нет склада нефти для %s", c.Name)
		}
		if err := base.OilTrunk(l, c, crudeTank); err != nil {
			return base, l, err
		}
		h.Say("нефтетрасса %s → %s", c.Name, crudeTank.Name)
	}
	// трубы шин: подключение всех жидкостных портов новых кластеров и складов (старые пары у
	// порта — на поверхность), потом снова под землю, где к ним ничего не подходит
	if err := base.FluidPipes(l, zb, fresh, cluster.FluidBus); err != nil {
		return base, l, err
	}
	// старые обычные трубы шин построены — на месте (конец при продлении не становится подземкой:
	// стадия 50 на бумаге, 4 карты — «подземная труба на месте трубы»)
	fixed := map[world.Tile]bool{}
	for _, pl := range old.Layout {
		if pl.Name == "pipe" && strings.HasPrefix(pl.Tag, "c/bus-") {
			fixed[world.TileOf(pl.Pos)] = true
		}
	}
	cluster.UnderPipes(l, zb, fixed)
	if err := splitPipes(h, l); err != nil {
		return base, l, err
	}
	for _, e := range l.Validate() {
		if !before[e] && !strings.Contains(e, "столбы не связаны") {
			var x, y int // чья постройка — в ошибку (разбор)
			if _, err := fmt.Sscanf(e[strings.Index(e, "{"):], "{%d %d}", &x, &y); err == nil {
				if p := l.At(world.Tile{X: x, Y: y}); p != nil {
					e += " (" + p.Tag + ")"
				}
				drawPlan(h, rough.Zones, base, l, world.Tile{X: x, Y: y}, 30, image("plan-yellow-err"))
				for _, p := range l.List { // что рядом — в журнал
					if t := world.TileOf(p.Pos); abs(t.X-x) <= 3 && abs(t.Y-y) <= 3 {
						h.Log("рядом с ошибкой: %v %s %s", t, p, p.Tag)
					}
				}
			}
			return base, l, fmt.Errorf("раскладка: %v", e)
		}
	}
	// выравниватели между лентами одного предмета — и здесь (как на стадиях 5–6)
	var busList []string
	for _, b := range buses {
		busList = append(busList, b.Name)
	}
	if n := cluster.BalancePlan(l, busList); n > 0 {
		h.Say("план жёлтой: +%d делителей между лентами одного предмета", n)
	}
	// стоящие буры старых блоков — источники своего ресурса (руда под ними в игре уже
	// вырабатывается, см. стадию 6); новые блоки — по руде с карты
	oldMine := map[string]string{}
	for _, c := range old.Clusters {
		if c.Kind == cluster.Mine {
			oldMine[cluster.Tag(c.Name)] = c.Product
		}
	}
	l.DrillOre = func(pl layout.Placement) []string {
		if r, ok := oldMine[pl.Tag]; ok {
			return []string{r}
		}
		return nil
	}
	// смешанное в старом плане (база до стадии 7 построена по нему) — не ошибка проекта
	err = flowCheckSince(h, l, base.Clusters, true, old.Layout)
	l.DrillOre = nil
	if err != nil {
		return base, l, err
	}
	for _, c := range fresh {
		h.Say("кластер %v", c)
	}
	if err := planDraw(h, rough.Zones, base, l, yellowImage()); err != nil {
		return base, l, err
	}
	// новые кластеры, склады и лаборатории крупно — для проверки глазами
	for _, c := range slices.Concat(sci, tanks, labs) {
		drawAround(h, rough.Zones, base, l, c, image("plan-yellow-"+c.Name))
	}
	// всё новое — вторым уровнем (stages.md, стадия 42: с 43-й все новые постройки — второго уровня)
	h.Say("второй уровень: %d новых построек", cluster.UpgradeNew(l, old.Layout))
	cluster.RestoreTier2(l, back)
	all := append(base.Sorted(), buses...)
	return base, l, writeState(planFile, ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: planImage()})
}

// checkYellowPlan — в плане кластеры проекта (сборка, НПЗ‑2, смазка, кислота, склады), у
// лабораторий вход жёлтой, картинка есть.
func checkYellowPlan(h *act.Hands) error {
	s, err := LoadWholePlan()
	if err != nil {
		return err
	}
	got := map[string]int{}
	for _, c := range s.Clusters {
		if c.Kind == cluster.Make || c.Kind == cluster.Tank {
			got[c.Product]++
		}
	}
	for _, it := range append(slices.Clone(yellowOrder), "heavy-oil", "lubricant", "sulfuric-acid", "light-oil") {
		if got[it] < 1 {
			return fmt.Errorf("в плане нет кластера %s", it)
		}
	}
	if !slices.ContainsFunc(s.Clusters, func(c *cluster.Cluster) bool {
		return c.Kind == cluster.Lab && slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == yellowPack })
	}) {
		return fmt.Errorf("у лабораторий нет входа %s", yellowPack)
	}
	if _, err := os.Stat(yellowImage()); err != nil {
		return fmt.Errorf("нет картинки плана %s", yellowImage())
	}
	return nil
}
