package stage

import (
	"context"
	"fmt"
	"os"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
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
	pr := h.Protos
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
	base := cluster.NewBase(pr, h.World, world.Tile{})
	base.Log = h.Log
	base.Avoid = built
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
		return err
	}
	cluster.FillLanes(zb, old.Layout)
	// добыча до 2M (stages.md, стадия 7): месторождения целиком, ближние к голове bus-1 первыми;
	// зон не хватает — новые зоны на дальних пятнах
	var fresh []*cluster.Cluster
	for _, it := range blueOres {
		var like *cluster.Cluster
		for _, c := range base.Clusters {
			if c.Kind == cluster.Mine && c.Product == it {
				like = c
			}
		}
		if like == nil {
			return fmt.Errorf("в плане нет добычи %s", it)
		}
		have := oreUnder(base.Clusters, it)
		zones := base.MoreMineZones(rough.Zones, it, oreTarget*1.3)
		if len(zones) > len(rough.Zones) {
			rough.Zones = zones
			if err := writeState(roughFile, rough); err != nil {
				return err
			}
		}
		ms, err := base.MoreMines(like, rough.Zones, have, oreTarget)
		if err != nil {
			return err
		}
		h.Say("добыча %s: под бурами %d, новых блоков %d", it, have, len(ms))
		fresh = append(fresh, ms...)
	}
	// сталь — из руды, сразу на 30 пар печей (cluster.md §2а «Сталь»)
	steel := cluster.Steel(pr, planReserve)
	if _, err := base.PlaceZoned([]*cluster.Cluster{steel}, rough.Zones); err != nil {
		return err
	}
	fresh = append(fresh, steel)
	// быстрые ленты и инсертеры — на замену всего стоящего за ~2 ч (stages.md, стадия 7):
	// шестерни — внутри парами; ленты, инсертеры, схемы — с шины (их кластеры уже есть)
	fast, err := calc.Solve(pr, map[string]float64{"fast-transport-belt": fastBeltRate, "fast-inserter": fastInserterRate},
		calc.Options{All: true, Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return err
	}
	cs, err := cluster.Split(pr, fast, planReserve, nil, greenSeparate...)
	if err != nil {
		return err
	}
	var helpers []*cluster.Cluster
	for _, c := range cs {
		if c.Kind == cluster.Make && (c.Product == "fast-transport-belt" || c.Product == "fast-inserter") {
			helpers = append(helpers, c)
		}
	}
	if _, err := base.PlaceZoned(helpers, rough.Zones); err != nil {
		return err
	}
	fresh = append(fresh, helpers...)
	l, err := base.Layout(fresh...)
	if err != nil {
		return err
	}
	for t := range built {
		delete(l.Reserved, t)
	}
	for _, pl := range old.Layout {
		l.Add(pl)
	}
	before := map[string]bool{}
	for _, e := range l.Validate() {
		before[e] = true
	}
	if err := base.ConnectMore(l, zb, fresh, nil); err != nil {
		planDraw(h, rough.Zones, base, l, blueImage())
		return err
	}
	for _, e := range l.Validate() {
		if !before[e] && !strings.Contains(e, "столбы не связаны") {
			planDraw(h, rough.Zones, base, l, blueImage())
			return fmt.Errorf("раскладка: %v", e)
		}
	}
	for _, it := range blueOres {
		h.Say("добыча %s: под бурами %d", it, oreUnder(base.Clusters, it))
	}
	if err := planDraw(h, rough.Zones, base, l, blueImage()); err != nil {
		return err
	}
	all := append(base.Sorted(), buses...)
	return writeState(planFile, ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: planImage()})
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
