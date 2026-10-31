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

// Стадия 6 (stages.md): проект кластеров фиолетовой науки — план стадии 5 дописывается:
// электропечи, модули продуктивности, рельсы (пары «стержни → рельсы»), фиолетовая наука;
// кирпич — в новую ленту bus-2; лаборатории — лента C; рост под всю науку (стоящее считается
// уже вторым уровнем); все новые постройки — второго уровня. Стройка — стадии 42–48.

const (
	// purpleBaseFile — план стадии 5 до дописывания: перезапуск стадии — с него; стройка 29–41 —
	// по нему (LoadBluePlan), 42–48 — по нему же с проектом фиолетовой (LoadPurplePlan)
	purpleBaseFile = "state-plan-blue.json"
	purplePack     = "production-science-pack"
	purpleRate     = 0.1 // вся наука — 0.1/с (stages.md)
)

// tier2Machines — машины расчёта: второй уровень (stages.md, стадия 6: стоящее считается уже
// вторым уровнем — потом замена на месте).
var tier2Machines = map[string]string{"smelting": "steel-furnace", "crafting": "assembling-machine-2", "advanced-crafting": "assembling-machine-2"}

// purpleSeparate — с шины (свои кластеры), не парами внутри потребителя.
var purpleSeparate = append([]string{"steel-plate", "stone-brick", "advanced-circuit", "iron-gear-wheel", "plastic-bar",
	"sulfur", "engine-unit", "rail", "iron-stick", "electric-furnace", "productivity-module"}, greenSeparate...)

// purpleOrder — новые кластеры: поставщики раньше потребителя (zones.md: потребитель правее).
var purpleOrder = []string{"electric-furnace", "productivity-module", "rail", purplePack}

func init() {
	register(Stage{N: 6, Name: "проект фиолетовой науки", Run: runPurplePlan, Check: checkPurplePlan, Plan: true})
}

// purpleImage — картинка плана с проектом фиолетовой науки: run/stages/plan-purple-<сид>.png.
func purpleImage() string { return image("plan-purple") }

// purpleBase — план до дописывания (при первом запуске — копия плана стадии 5).
func purpleBase() (*ClusterPlan, error) {
	if _, err := os.Stat(runPath(purpleBaseFile)); err != nil {
		data, err := os.ReadFile(runPath(planFile))
		if err != nil {
			return nil, fmt.Errorf("нет плана кластеров (стадия 5): %w", err)
		}
		if err := os.WriteFile(runPath(purpleBaseFile), data, 0o644); err != nil {
			return nil, err
		}
	}
	return loadPlanFile(runPath(purpleBaseFile))
}

// orderIn — входы кластера в порядке items (cluster.md §2а «Сборка»: первые два — одна лента
// по полосам, третий — лента C за лентой выхода).
func orderIn(c *cluster.Cluster, items ...string) error {
	var res []cluster.Flow
	for _, it := range items {
		i := slices.IndexFunc(c.In, func(f cluster.Flow) bool { return f.Item == it })
		if i < 0 {
			return fmt.Errorf("%s: нет входа %s (входы %v)", c.Product, it, c.In)
		}
		res = append(res, c.In[i])
	}
	if len(res) != len(c.In) {
		return fmt.Errorf("%s: входов %v, ожидал %v", c.Product, c.In, items)
	}
	c.In = res
	return nil
}

// purpleClusters — электропечи (сталь и кирпич снизу, красные схемы сверху — лента C), модули
// (зелёные и красные схемы сверху), рельсы (пара «стержни → рельсы»: железо и сталь снизу,
// камень — лента C), фиолетовая наука (рельсы и электропечи, модули — лента C); остальное — с
// шины (stages.md, стадия 6).
func purpleClusters(pr *knowledge.Prototypes) ([]*cluster.Cluster, error) {
	plan, err := calc.Solve(pr, map[string]float64{purplePack: purpleRate}, calc.Options{All: true, Machines: tier2Machines})
	if err != nil {
		return nil, err
	}
	cs, err := cluster.Split(pr, plan, planReserve, nil, purpleSeparate...)
	if err != nil {
		return nil, err
	}
	by := map[string]*cluster.Cluster{}
	for _, c := range cs {
		if c.Kind == cluster.Make {
			by[c.Product] = c
		}
	}
	for _, it := range []string{"electric-furnace", "productivity-module", "rail", "iron-stick", purplePack} {
		if by[it] == nil {
			return nil, fmt.Errorf("фиолетовая наука: в расчёте нет %s", it)
		}
	}
	rails := cluster.Pair(pr, by["rail"], by["iron-stick"])
	by["electric-furnace"].ByRate() // штучные — по темпу (cluster.md §2 «Размер», решение пользователя)
	by["productivity-module"].ByRate()
	for _, o := range []struct {
		c     *cluster.Cluster
		items []string
	}{
		{by["electric-furnace"], []string{"steel-plate", "stone-brick", "advanced-circuit"}},
		{by["productivity-module"], []string{"electronic-circuit", "advanced-circuit"}},
		{rails, []string{"iron-plate", "steel-plate", "stone"}},
		{by[purplePack], []string{"rail", "electric-furnace", "productivity-module"}},
	} {
		if err := orderIn(o.c, o.items...); err != nil {
			return nil, err
		}
	}
	return []*cluster.Cluster{by["electric-furnace"], by["productivity-module"], rails, by[purplePack]}, nil
}

// purpleNeeds — потребность всей базы (красная + зелёная + синяя + фиолетовая, быстрые ленты и
// инсертеры) кластерами с запасом, машины — второго уровня: по ней рост старых кластеров
// (cluster.md §7; стоящее считается вторым уровнем — stages.md, стадия 6).
func purpleNeeds(pr *knowledge.Prototypes) ([]*cluster.Cluster, error) {
	targets := map[string]float64{redPack: redRate, greenPack: greenRate, bluePack: blueRate, purplePack: purpleRate,
		"fast-transport-belt": fastBeltRate, "fast-inserter": fastInserterRate}
	whole, err := calc.Solve(pr, targets, calc.Options{All: true, Machines: tier2Machines})
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

func runPurplePlan(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	rough, err := LoadRough()
	if err != nil {
		return err
	}
	old, err := purpleBase()
	if err != nil {
		return err
	}
	// построенное по плану — препятствие (стадия 6 — до стройки: свои постройки на карте не мешают)
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
	// стадиях 4–5)
	for try := 0; ; try++ {
		base, l, err := purpleTry(h, rough, built, avoid)
		if err == nil {
			return nil
		}
		ce, ok := err.(*cluster.ConnectError)
		if !ok || try >= 5 {
			if base != nil && l != nil {
				planDraw(h, rough.Zones, base, l, purpleImage()) // что вышло — на картинку
				if ok {
					drawAround(h, rough.Zones, base, l, ce.Consumer, image("plan-purple-fail"))
				}
			}
			return err
		}
		h.Say("кластер %s на %v: %v — ищу другое место", ce.Consumer.Name, ce.Consumer.Area, ce.Err)
		drawAround(h, rough.Zones, base, l, ce.Consumer, image(fmt.Sprintf("plan-purple-fail%d", try)))
		a := ce.Consumer.Area
		for y := a.Min.Y; y < a.Max.Y; y++ {
			for x := a.Min.X; x < a.Max.X; x++ {
				avoid[world.Tile{X: x, Y: y}] = true
			}
		}
	}
}

// purpleTry — одна попытка проекта фиолетовой науки: план стадии 5 — заново из файла (прошлая
// попытка меняла кластеры), avoid — куда новые кластеры не ставить; удалось — план записан.
func purpleTry(h *act.Hands, rough *RoughPlan, built, avoid map[world.Tile]bool) (*cluster.Base, *layout.Layout, error) {
	pr := h.Protos
	old, err := loadPlanFile(runPath(purpleBaseFile))
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
	// новые кластеры — в свои зоны: электропечи, модули, рельсы — снабжение; фиолетовая — конечный
	sci, err := purpleClusters(pr)
	if err != nil {
		return nil, nil, err
	}
	fresh, _, err := base.PlaceZoned(sci, rough.Zones)
	if err != nil {
		return nil, nil, err
	}
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
	// рост под всю науку (cluster.md §7; стоящее считается вторым уровнем — needs машинами
	// второго уровня)
	needs, err := purpleNeeds(pr)
	if err != nil {
		return base, l, err
	}
	g, err := growProject(h, pr, base, &l, needs, rough.Zones, &fresh, layoutAll)
	if err != nil {
		return base, l, err
	}
	stations, stationOf := g.stations, g.stationOf
	// фиолетовая — в лаборатории лентой C (cluster.md §2а «Лаборатории»)
	extra := map[*cluster.Cluster][]cluster.Port{}
	var labs []*cluster.Cluster
	for _, c := range base.Clusters {
		if c.Kind == cluster.Lab && !slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == purplePack }) {
			p, err := cluster.AddLaneC(l, c, purplePack, purpleRate*cluster.Margin, cluster.Tag(c.Name))
			if err != nil {
				return base, l, err
			}
			extra[c] = []cluster.Port{p}
			labs = append(labs, c)
		}
	}
	// кирпич — в новую ленту bus-2 (к электропечам): выход старого кластера кирпича в неё
	// (stages.md, стадия 43; буфер на его выходе снимает стройка)
	for _, c := range base.Clusters {
		if c.Kind == cluster.Smelt && c.Product == "stone-brick" && !slices.Contains(fresh, c) {
			base.Refeed = append(base.Refeed, c)
			h.Say("кирпич: %s → лента bus-2", c.Name)
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
	for _, e := range l.Validate() {
		if !before[e] && !strings.Contains(e, "столбы не связаны") {
			var x, y int // чья постройка — в ошибку (разбор)
			if _, err := fmt.Sscanf(e[strings.Index(e, "{"):], "{%d %d}", &x, &y); err == nil {
				if p := l.At(world.Tile{X: x, Y: y}); p != nil {
					e += " (" + p.Tag + ")"
				}
				drawPlan(h, rough.Zones, base, l, world.Tile{X: x, Y: y}, 30, image("plan-purple-err"))
				for _, p := range l.List { // что рядом — в журнал
					if t := world.TileOf(p.Pos); abs(t.X-x) <= 3 && abs(t.Y-y) <= 3 {
						h.Log("рядом с ошибкой: %v %s %s", t, p, p.Tag)
					}
				}
			}
			return base, l, fmt.Errorf("раскладка: %v", e)
		}
	}
	// выравниватели между лентами одного предмета — и здесь (как на стадии 5)
	var busList []string
	for _, b := range buses {
		busList = append(busList, b.Name)
	}
	if n := cluster.BalancePlan(l, busList); n > 0 {
		h.Say("план фиолетовой: +%d делителей между лентами одного предмета", n)
	}
	// стоящие буры старых блоков — источники своего ресурса: руда под ними в игре уже
	// вырабатывается (проект — после стадии 41), это не ошибка проекта (267145981: 130 печей
	// «нет руды»); новые блоки — по руде с карты
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
	err = flowCheck(h, l, base.Clusters, true)
	l.DrillOre = nil
	if err != nil {
		return base, l, err
	}
	for _, c := range fresh {
		h.Say("кластер %v", c)
	}
	if err := planDraw(h, rough.Zones, base, l, purpleImage()); err != nil {
		return base, l, err
	}
	// новые кластеры и лаборатории крупно — для проверки глазами
	for _, c := range append(slices.Clone(sci), labs...) {
		drawAround(h, rough.Zones, base, l, c, image("plan-purple-"+c.Name))
	}
	// всё новое — вторым уровнем (stages.md, стадия 42: с 43-й все новые постройки — второго уровня)
	h.Say("второй уровень: %d новых построек", cluster.UpgradeNew(l, old.Layout))
	all := append(base.Sorted(), buses...)
	return base, l, writeState(planFile, ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: planImage()})
}

// checkPurplePlan — в плане кластеры проекта, у лабораторий вход фиолетовой, картинка есть.
func checkPurplePlan(h *act.Hands) error {
	s, err := LoadPurplePlan()
	if err != nil {
		return err
	}
	got := map[string]int{}
	for _, c := range s.Clusters {
		if c.Kind == cluster.Make {
			got[c.Product]++
		}
	}
	for _, it := range purpleOrder {
		if got[it] < 1 {
			return fmt.Errorf("в плане нет кластера %s", it)
		}
	}
	if !slices.ContainsFunc(s.Clusters, func(c *cluster.Cluster) bool {
		return c.Kind == cluster.Lab && slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == purplePack })
	}) {
		return fmt.Errorf("у лабораторий нет входа %s", purplePack)
	}
	if _, err := os.Stat(purpleImage()); err != nil {
		return fmt.Errorf("нет картинки плана %s", purpleImage())
	}
	return nil
}
