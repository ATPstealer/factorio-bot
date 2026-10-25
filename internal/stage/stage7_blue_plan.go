package stage

import (
	"context"
	"fmt"
	"os"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 7 (stages.md): проект кластеров синей науки — план стадии 6 дописывается: добыча до 2M
// руды под бурами (месторождения целиком), дальше — сталь, быстрые ленты и инсертеры, нефть,
// двигатели, красные схемы, синяя наука (по шагам).

const (
	// blueBaseFile — план стадии 6 до дописывания: перезапуск стадии — с него
	blueBaseFile = "state-plan-green.json"
	oreTarget    = 2_000_000 // руды под бурами на ресурс (железо, медь, уголь)
	// быстрые ленты и инсертеры, шт/с: ~10k лент и ~500 инсертеров за 2 ч (stages.md, стадия 7)
	fastBeltRate     = 1.4
	fastInserterRate = 0.07
	// refineries — НПЗ на простой переработке: газа с большим запасом (на синюю ~4/с, один НПЗ
	// даёт 9/с)
	refineries = 5
)

// blueOres — ресурсы, добыча которых растёт до oreTarget.
var blueOres = []string{"iron-ore", "copper-ore", "coal"}

func init() {
	register(Stage{N: 7, Name: "проект синей науки", Run: runBluePlan, Check: checkBluePlan, Plan: true})
}

// blueImage — картинка плана с проектом синей науки: run/stages/plan-blue-<сид>.png.
func blueImage() string { return image("plan-blue") }

// blueBase — план до дописывания (при первом запуске — копия плана стадии 6).
func blueBase() (*ClusterPlan, error) {
	if _, err := os.Stat(runPath(blueBaseFile)); err != nil {
		data, err := os.ReadFile(runPath(planFile))
		if err != nil {
			return nil, fmt.Errorf("нет плана кластеров (стадия 6): %w", err)
		}
		if err := os.WriteFile(runPath(blueBaseFile), data, 0o644); err != nil {
			return nil, err
		}
	}
	return loadPlanFile(runPath(blueBaseFile))
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
	// стадии 6)
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

// blueTry — одна попытка проекта синей науки: план стадии 6 — заново из файла (прошлая попытка
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
	zb, err := base.PlaceZoned(nil, rough.Zones)
	if err != nil {
		return nil, nil, err
	}
	cluster.FillLanes(zb, old.Layout)
	// добыча до 2M (stages.md, стадия 7): месторождения целиком, ближние к голове bus-1 первыми;
	// зон не хватает — новые зоны на дальних пятнах
	var fresh, fill []*cluster.Cluster
	for _, it := range blueOres {
		var like *cluster.Cluster
		have := 0
		// стоящие блоки (первая итерация — до 500k) — дописать на всё пятно в своей площади
		for _, c := range base.Clusters {
			if c.Kind == cluster.Mine && c.Product == it {
				like = c
				fill = append(fill, c)
				have += max(c.OreUnder, base.OreIn(c))
			}
		}
		if like == nil {
			return nil, nil, fmt.Errorf("в плане нет добычи %s", it)
		}
		zones := base.MoreMineZones(rough.Zones, it, oreTarget*1.3)
		if len(zones) > len(rough.Zones) {
			rough.Zones = zones
			if err := writeState(roughFile, rough); err != nil {
				return nil, nil, err
			}
		}
		ms, err := base.MoreMines(like, rough.Zones, have, oreTarget)
		if err != nil {
			return nil, nil, err
		}
		h.Say("добыча %s: под бурами %d, новых блоков %d", it, have, len(ms))
		fresh = append(fresh, ms...)
	}
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
	if _, err := base.PlaceZoned([]*cluster.Cluster{tank}, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, tank)
	// сталь — из руды, сразу на 30 пар печей (cluster.md §2а «Сталь»)
	steel := cluster.Steel(pr, planReserve)
	if _, err := base.PlaceZoned([]*cluster.Cluster{steel}, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, steel)
	// НПЗ — простая переработка, только газ, с большим запасом (stages.md, стадия 7)
	refinery := cluster.Refinery(pr, refineries)
	if _, err := base.PlaceZoned([]*cluster.Cluster{refinery}, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, refinery)
	// быстрые ленты и инсертеры — на замену всего стоящего за ~2 ч (stages.md, стадия 7):
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
	if _, err := base.PlaceZoned(helpers, rough.Zones); err != nil {
		return nil, nil, err
	}
	fresh = append(fresh, helpers...)
	l, err := base.Layout(fresh...)
	if err != nil {
		return base, l, err
	}
	for t := range built {
		delete(l.Reserved, t)
	}
	for _, pl := range old.Layout {
		l.Add(pl)
	}
	for _, c := range fill {
		if err := base.FillMine(l, c); err != nil {
			return base, l, fmt.Errorf("добыча %s на всё пятно: %w", c.Name, err)
		}
	}
	before := map[string]bool{}
	for _, e := range l.Validate() {
		before[e] = true
	}
	if err := base.ConnectMore(l, zb, fresh, nil); err != nil {
		return base, l, err
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
	if err := base.RefineryPipes(l, zb, refinery, tank, refinery.Area.Max.X+60); err != nil {
		return base, l, err
	}
	for _, e := range l.Validate() {
		if !before[e] && !strings.Contains(e, "столбы не связаны") {
			return base, l, fmt.Errorf("раскладка: %v", e)
		}
	}
	for _, it := range blueOres {
		h.Say("добыча %s: под бурами %d", it, oreUnder(base.Clusters, it))
	}
	for _, c := range fresh {
		h.Say("кластер %v", c)
	}
	if err := planDraw(h, rough.Zones, base, l, blueImage()); err != nil {
		return base, l, err
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
	if st, err := os.Stat(blueImage()); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки %s", blueImage())
	}
	return nil
}
