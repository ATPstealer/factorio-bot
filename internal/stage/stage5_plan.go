package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 5 (stages.md): план первых кластеров «на бумаге» (cluster.md) внутри зон стадии 4
// (zones.md) — добыча угля, железа, меди; плавка железа и меди; станция; шины; шестерни;
// красная наука; лаборатории; хранение. Цель — красная наука redRate/с, без запаса роста
// (запасы — в зонах). Картинка — зоны и кластеры с раскладкой поверх карты.
// Постройки точек стадии 2 при разметке не мешают: их разберут (стадия 15).

const (
	planFile    = "state-plan.json"
	redRate     = 0.1 // красная наука, шт/с (вся наука до ракеты — 0.1/с, stages.md)
	planReserve = 1   // без запаса роста: запасы — в зонах (zones.md)
	planLabSec  = 10  // секунд исследования на пакет (automation и т.п.)
)

// planKinds — какие кластеры должны быть в плане (имя без номера).
var planKinds = []string{"coal", "iron-ore", "copper-ore", "iron-plate", "copper-plate", "power",
	"bus", "gear", "red-science", "lab", "storage"}

// ClusterPlan — план кластеров карты: кластеры с местами, размещений в раскладке, картинка.
type ClusterPlan struct {
	Clusters []*cluster.Cluster `json:"clusters"`
	Placed   int                `json:"placed"` // размещений внутри кластеров и логистики
	Layout   []layout.Placement `json:"layout"` // раскладка: по ней строят следующие стадии
	Image    string             `json:"image"`
}

func init() {
	register(Stage{N: 5, Name: "план кластеров", Run: runPlan, Check: checkPlan, Plan: true})
}

// LoadPlan — план кластеров этой карты для стройки стадий 11–28: стадий 5–6 (дописанное
// стадией 7 — синяя наука — строится потом, его план — LoadBluePlan).
func LoadPlan() (*ClusterPlan, error) {
	data, err := os.ReadFile(runPath(planNowFile()))
	if err != nil {
		return nil, fmt.Errorf("нет плана кластеров (стадия 5): %w", err)
	}
	var s ClusterPlan
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// planNowFile — файл плана стройки стадий 11–28: план до стадии 7, если она уже дописала
// свой (иначе — общий).
func planNowFile() string {
	if _, err := os.Stat(runPath(blueBaseFile)); err == nil {
		return blueBaseFile
	}
	return planFile
}

// LoadBluePlan — весь план с проектом синей науки (стадия 7).
func LoadBluePlan() (*ClusterPlan, error) { return loadPlanFile(runPath(planFile)) }

func runPlan(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	rough, err := LoadRough()
	if err != nil {
		return err
	}
	pr := h.Protos
	targets := map[string]float64{"automation-science-pack": redRate}
	plan, err := calc.Solve(pr, targets, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return err
	}
	// свои постройки (точки стадии 2) — не помеха
	var own []world.Object
	for _, o := range h.World.Objects() {
		if o.Own {
			own = append(own, o)
		}
	}
	restore := h.World.Hide(own)
	defer restore()
	avoid := map[world.Tile]bool{}
	boost := map[string]float64{} // будущая добыча: цель × (не сошлась по факту — больше)
	short := 0
	// кластер, к которому ленту не провести, — в другое место в своей зоне (до 6 попыток)
	for try := 0; ; try++ {
		cs, err := cluster.Split(pr, plan, planReserve, nil, "iron-gear-wheel")
		if err != nil {
			return err
		}
		// хранение — одно, у начала базы: под bus-1 и правее её головы, подальше от развязки
		// подводов с добычи слева (cluster.md §2а «Хранение»; место — свободное вне зон, Place)
		store := cluster.StorageCluster()
		for _, z := range rough.Zones {
			if z.Name == "bus-1" {
				store.Near = &world.Tile{X: z.Rect.Min.X + 40, Y: z.Rect.Max.Y + 15}
			}
		}
		cs = append(cs, cluster.Labs(pr, targets, planLabSec, planReserve), store)
		if cs, err = cluster.AddPower(pr, cs, planReserve, nil); err != nil {
			return err
		}
		base := cluster.NewBase(pr, h.World, world.Tile{}) // центр базы — место старта
		base.Log = h.Log
		base.Avoid = avoid
		base.Keep = planStart(own) // стартовые точки не задевать; хранение — у головы bus-1 (PlaceZoned)
		_, buses, err := base.PlaceZoned(cs, rough.Zones)
		if err != nil {
			return err
		}
		// будущая добыча до синей науки — сразу в план (zones.md: ленты bus-1 — все и по
		// порядку подводов), строится на стадии 30
		later, err := futureMines(h, base, rough, boost)
		if err != nil {
			return err
		}
		l, err := base.Layout()
		if err != nil {
			return err
		}
		// блоки первой итерации — на всё месторождение (буры сверх 500k — на потом)
		for _, c := range base.Clusters {
			if c.Kind == cluster.Mine && !c.IsOil() && !c.Later {
				if err := base.FillMine(l, c, cluster.LaterTag(c.Name)); err != nil {
					return fmt.Errorf("добыча %s на всё пятно: %w", c.Name, err)
				}
			}
		}
		h.Log("будущая добыча: %d кластеров", len(later))
		// под бурами — по факту раскладки: меньше цели (оценка не сошлась: 954176679, уголь
		// 1.79M) — ещё месторождений и заново
		if it, got, want := shortOre(base.Clusters); it != "" && short < 3 {
			short++
			boost[it] = max(boost[it], 1) * float64(want) / float64(max(got, 1)) * 1.05
			h.Say("добыча %s: под бурами %d из %d — беру ещё месторождений", it, got, want)
			continue
		}
		err = base.ConnectZoned(l, buses)
		all := base.Sorted()
		for _, bus := range buses { // шины — в план кластерами (теги лент c/<шина>)
			all = append(all, &cluster.Cluster{Name: bus.Name, Kind: cluster.Bus, Product: "bus", Area: bus.Rect, Rect: bus.Rect})
		}
		if err == nil {
			// связность столбов — не здесь: питание каждая стадия тянет сама (connectPower)
			for _, e := range l.Validate() {
				if !strings.Contains(e, "столбы не связаны") {
					return fmt.Errorf("раскладка: %v", e)
				}
			}
			if err := flowCheck(h, l, all); err != nil {
				return err
			}
			for _, c := range all {
				h.Say("кластер %v", c)
			}
			if err := planDraw(h, rough.Zones, base, l, planImage()); err != nil {
				return err
			}
			for _, z := range rough.Zones { // подводы у головы bus-1 — для проверки глазами
				if z.Name == "bus-1" {
					drawPlan(h, rough.Zones, base, l, z.Rect.Min, 70, image("plan-head"))
				}
			}
			return writeState(planFile, ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: planImage()})
		}
		ce, ok := err.(*cluster.ConnectError)
		if !ok || try >= 5 {
			planDraw(h, rough.Zones, base, l, planImage()) // что вышло — на картинку
			for _, z := range rough.Zones {
				if z.Name == "bus-1" { // подводы с добычи — у головы bus-1
					drawPlan(h, rough.Zones, base, l, z.Rect.Min, 60, image("plan-head"))
				}
			}
			for _, c := range base.Clusters { // кластер из ошибки — крупно
				if strings.Contains(err.Error(), c.Name+" ") {
					drawAround(h, rough.Zones, base, l, c, image("plan-fail"))
				}
			}
			return err
		}
		h.Say("кластер %s: %v — ищу другое место", ce.Consumer.Name, ce.Err)
		a := ce.Consumer.Area
		for y := a.Min.Y; y < a.Max.Y; y++ {
			for x := a.Min.X; x < a.Max.X; x++ {
				avoid[world.Tile{X: x, Y: y}] = true
			}
		}
	}
}

// futureMines — будущая добыча до синей науки (Later: в плане сразу, строится на стадии 30):
// железо, медь, уголь — месторождения целиком до oreTarget под бурами (блоки первой итерации
// тоже дополнятся — в счёт), камень — до stoneTarget, и кластер плавки кирпича.
func futureMines(h *act.Hands, base *cluster.Base, rough *RoughPlan, boost map[string]float64) ([]*cluster.Cluster, error) {
	scaled := func(it string, want int) int { return int(float64(want) * max(boost[it], 1)) }
	var res []*cluster.Cluster
	more := func(like *cluster.Cluster, have, want int) error {
		if zones := base.MoreMineZones(rough.Zones, like.Product, float64(want)*1.3); len(zones) > len(rough.Zones) {
			rough.Zones = zones
			if err := writeState(roughFile, rough); err != nil {
				return err
			}
		}
		ms, err := base.MoreMines(like, rough.Zones, have, want)
		if err != nil {
			return err
		}
		for _, m := range ms {
			m.Later = true
		}
		res = append(res, ms...)
		return nil
	}
	for _, it := range blueOres {
		var like *cluster.Cluster
		have := 0
		for _, c := range base.Clusters {
			if c.Kind == cluster.Mine && c.Product == it {
				like = c
				have += int(float64(base.OreIn(c)) * cluster.MineCover) // дополнится на всё пятно
			}
		}
		if like == nil {
			return nil, fmt.Errorf("в плане нет добычи %s", it)
		}
		if err := more(like, have, scaled(it, oreTarget)); err != nil {
			return nil, err
		}
	}
	stone := &cluster.Cluster{Kind: cluster.Mine, Product: "stone", Reserve: planReserve,
		Units: []cluster.Unit{{Recipe: "mine:stone", Machine: "electric-mining-drill", Machines: 1}}}
	if err := more(stone, 0, scaled("stone", stoneTarget)); err != nil {
		return nil, err
	}
	bricks, err := cluster.SmeltOf(h.Protos, "stone-brick", planReserve)
	if err != nil {
		return nil, err
	}
	bricks.Later = true
	ps, _, err := base.PlaceZoned([]*cluster.Cluster{bricks}, rough.Zones)
	if err != nil {
		return nil, err
	}
	return append(res, ps...), nil
}

// shortOre — ресурс, под бурами которого (по раскладке) меньше цели: имя, сколько, цель; "" — все
// в порядке.
func shortOre(cs []*cluster.Cluster) (string, int, int) {
	targets := map[string]int{"stone": stoneTarget}
	for _, it := range blueOres {
		targets[it] = oreTarget
	}
	for _, it := range append(slices.Clone(blueOres), "stone") {
		if got := oreUnder(cs, it); got < targets[it] {
			return it, got, targets[it]
		}
	}
	return "", 0, 0
}

// planImage — картинка плана кластеров: run/stages/plan-<сид>.png.
func planImage() string { return image("plan") }

// image — картинка name карты: run/stages/<name>-<сид>.png (по смыслу, без номера стадии).
func image(name string) string {
	if roughOffline { // без игры (bin/bot rough) — рядом с состоянием, не мешать прогонам
		return runPath(name + "-" + seedOf() + ".png")
	}
	return filepath.Join("run/stages", name+"-"+seedOf()+".png")
}

// planDraw — картинка в файл path: карта, зоны, кластеры с бордюрами и раскладка.
func planDraw(h *act.Hands, zones []cluster.Zone, base *cluster.Base, l *layout.Layout, path string) error {
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, c := range base.Clusters {
		r := c.Area.Grow(cluster.Corridor)
		minX, minY = min(minX, r.Min.X), min(minY, r.Min.Y)
		maxX, maxY = max(maxX, r.Max.X), max(maxY, r.Max.Y)
	}
	for _, z := range zones {
		if z.Kind != cluster.ZoneMine {
			minX, minY = min(minX, z.Rect.Min.X), min(minY, z.Rect.Min.Y)
			maxX, maxY = max(maxX, z.Rect.Max.X), max(maxY, z.Rect.Max.Y)
		}
	}
	radius := max(maxX-minX, maxY-minY)/2 + 20
	return drawPlan(h, zones, base, l, world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}, radius, path)
}

// drawAround — картинка плана вокруг кластера c (разбор неудачной логистики): радиус 60, крупно.
func drawAround(h *act.Hands, zones []cluster.Zone, base *cluster.Base, l *layout.Layout, c *cluster.Cluster, path string) error {
	x, y := c.Area.Center()
	return drawPlan(h, zones, base, l, world.Tile{X: int(x), Y: int(y)}, 60, path)
}

// drawPlan — картинка плана с центром center и радиусом radius в файл path.
func drawPlan(h *act.Hands, zones []cluster.Zone, base *cluster.Base, l *layout.Layout, center world.Tile, radius int, path string) error {
	// крупно: ~8000 px на сторону, не меньше 3 px на тайл (пользователь: большие картинки в
	// хорошем качестве)
	ro := world.RenderOptions{Center: center, Radius: radius, Scale: max(3, min(12, 8000/(2*radius))), Labels: true}
	img := h.World.Render(ro)
	cluster.DrawZones(img, ro, zones)
	base.Draw(img, ro)
	if l != nil {
		l.Draw(img, ro)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func checkPlan(h *act.Hands) error {
	s, err := LoadPlan()
	if err != nil {
		return err
	}
	have := map[string]int{}
	for _, c := range s.Clusters {
		k := c.Name
		if i := strings.LastIndex(k, "-"); i > 0 {
			k = k[:i]
		}
		have[k]++
		if c.Area.W() == 0 || c.Area.H() == 0 {
			return fmt.Errorf("кластер %s без места", c.Name)
		}
	}
	for _, k := range planKinds {
		if have[k] == 0 {
			return fmt.Errorf("в плане нет кластера %s", k)
		}
	}
	if st, err := os.Stat(s.Image); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки плана %s", s.Image)
	}
	return nil
}

// planStart — места всех своих построек (точки стадии 2): кластеры без зоны их не задевают
// (до разборки на стадии 15).
func planStart(own []world.Object) []cluster.Rect {
	var keep []cluster.Rect
	for _, o := range own {
		t := world.TileOf(o.Pos)
		keep = append(keep, cluster.Rect{Min: world.Tile{X: t.X - 1, Y: t.Y - 1}, Max: world.Tile{X: t.X + 2, Y: t.Y + 2}})
	}
	return keep
}
