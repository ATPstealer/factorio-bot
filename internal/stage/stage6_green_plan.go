package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 6 (stages.md): проект кластеров зелёной науки — план стадии 5 дописывается, а не
// перестраивается: построенное — препятствие, новые кластеры (провод и схемы, инсертеры, ленты —
// шестерни внутри парами, зелёная наука) встают в свои зоны, лаборатории получают второй вход.

const (
	greenRate = 0.1 // зелёная наука, шт/с
	// greenBaseFile — план стадии 5 до дописывания: перезапуск стадии — с него, не с дописанного
	greenBaseFile = "state-plan-base.json"
	greenPack     = "logistic-science-pack"
)

// greenSeparate — отдельными кластерами (не внутрь потребителя): у каждого своя лента шины.
var greenSeparate = []string{"electronic-circuit", "inserter", "transport-belt"}

// greenOrder — порядок новых кластеров: поставщики раньше потребителей (схемы — инсертерам с шины
// над этажом: инсертеры правее).
var greenOrder = []string{"electronic-circuit", "transport-belt", "inserter", greenPack}

func init() {
	register(Stage{N: 6, Name: "проект зелёной науки", Run: runGreenPlan, Check: checkGreenPlan, Plan: true})
}

// loadPlanFile — план кластеров из файла path.
func loadPlanFile(path string) (*ClusterPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s ClusterPlan
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// greenImage — картинка плана с проектом зелёной науки: run/stages/plan-green-<сид>.png.
func greenImage() string { return image("plan-green") }

// greenBase — план до дописывания (при первом запуске — копия плана стадии 5).
func greenBase() (*ClusterPlan, error) {
	if _, err := os.Stat(runPath(greenBaseFile)); err != nil {
		data, err := os.ReadFile(runPath(planFile))
		if err != nil {
			return nil, fmt.Errorf("нет плана кластеров (стадия 5): %w", err)
		}
		if err := os.WriteFile(runPath(greenBaseFile), data, 0o644); err != nil {
			return nil, err
		}
	}
	return loadPlanFile(runPath(greenBaseFile))
}

func runGreenPlan(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	rough, err := LoadRough()
	if err != nil {
		return err
	}
	pr := h.Protos
	plan, err := calc.Solve(pr, map[string]float64{greenPack: greenRate}, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return err
	}
	old, err := greenBase()
	if err != nil {
		return err
	}
	// построенное по плану вне площадей кластеров (ленты шин, подводы, отводы) — новым кластерам
	// препятствие
	inArea := func(t world.Tile) bool {
		for _, c := range old.Clusters {
			if c.Kind != cluster.Bus && t.X >= c.Area.Min.X && t.X < c.Area.Max.X && t.Y >= c.Area.Min.Y && t.Y < c.Area.Max.Y {
				return true
			}
		}
		return false
	}
	built := map[world.Tile]bool{}
	for _, pl := range old.Layout {
		for _, t := range layout.Tiles(pr, pl) {
			if !inArea(t) {
				built[t] = true
			}
		}
	}
	// стадия 6 — сразу после плана стадии 5, стройки ещё не было: свои постройки на карте (точки
	// стадии 2) к стройке разобраны — не помеха; план стадии 5 считается построенным
	var own []world.Object
	for _, o := range h.World.Objects() {
		if o.Own {
			own = append(own, o)
		}
	}
	restore := h.World.Hide(own)
	defer restore()
	avoid := map[world.Tile]bool{}
	for t := range built {
		avoid[t] = true
	}
	// потребность всей базы — красная и зелёная наука вместе (с запасом): по ней рост старых
	// кластеров
	targets := map[string]float64{redPack: redRate, greenPack: greenRate}
	whole, err := calc.Solve(pr, targets, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return err
	}
	needs, err := cluster.Split(pr, whole, planReserve, nil, append([]string{"iron-gear-wheel"}, greenSeparate...)...)
	if err != nil {
		return err
	}
	needs = append(needs, cluster.Labs(pr, targets, planLabSec, planReserve))
	if needs, err = cluster.AddPower(pr, needs, planReserve, nil); err != nil {
		return err
	}
	for try := 0; ; try++ {
		old, err := loadPlanFile(runPath(greenBaseFile)) // заново: прошлая попытка меняла кластеры
		if err != nil {
			return err
		}
		cs, err := cluster.Split(pr, plan, planReserve, nil, greenSeparate...)
		if err != nil {
			return err
		}
		var fresh []*cluster.Cluster
		for _, c := range cs {
			if c.Kind == cluster.Make { // добыча и плавка — уже есть (их рост — потом)
				fresh = append(fresh, c)
			}
		}
		slices.SortStableFunc(fresh, func(a, b *cluster.Cluster) int {
			return slices.Index(greenOrder, a.Product) - slices.Index(greenOrder, b.Product)
		})
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
		zb, err := base.PlaceZoned(fresh, rough.Zones)
		if err != nil {
			return err
		}
		cluster.FillLanes(zb, old.Layout)
		layoutAll := func() (*layout.Layout, error) {
			l, err := base.Layout(fresh...)
			if err != nil {
				return nil, err
			}
			for t := range built {
				delete(l.Reserved, t)
			}
			for _, pl := range old.Layout {
				l.Add(pl)
			}
			return l, nil
		}
		// рост старых кластеров (cluster.md §7): вдоль ряда, не влез — новый кластер
		l, err := layoutAll()
		if err != nil {
			return err
		}
		grow := map[*cluster.Cluster]int{}
		var extras []*cluster.Cluster
		for _, c := range slices.Clone(base.Clusters) {
			if c.Kind != cluster.Mine && c.Kind != cluster.Smelt && c.Kind != cluster.Power || slices.Contains(fresh, c) {
				continue
			}
			n, have := cluster.Need(needs, c), cluster.Built(old.Layout, c)
			if n <= have {
				continue
			}
			if err := base.GrowRow(l, c, n); err == nil {
				h.Say("рост: %s — %s %d → %d вдоль ряда", c.Name, c.MainMachine(), have, n)
				grow[c] = n
				continue
			} else {
				h.Log("рост %s: %v", c.Name, err)
			}
			x, err := cluster.Extra(pr, needs, c, have, n)
			if err != nil {
				return fmt.Errorf("рост %s (%d → %d): %w", c.Name, have, n, err)
			}
			extras = append(extras, x)
		}
		if len(extras) > 0 {
			if _, err := base.PlaceZoned(extras, rough.Zones); err != nil {
				return err
			}
			for _, x := range extras {
				h.Say("рост: новый кластер %s", x.Name)
			}
			fresh = append(fresh, extras...)
		}
		if l, err = layoutAll(); err != nil {
			return err
		}
		for c, n := range grow {
			if err := base.GrowRow(l, c, n); err != nil {
				return err
			}
		}
		before := map[string]bool{}
		for _, e := range l.Validate() {
			before[e] = true
		}
		var lab *cluster.Cluster
		for _, c := range base.Clusters {
			if c.Kind == cluster.Lab {
				lab = c
			}
		}
		extra := map[*cluster.Cluster][]cluster.Port{}
		if lab != nil && !slices.ContainsFunc(lab.In, func(f cluster.Flow) bool { return f.Item == greenPack }) {
			p, err := cluster.AddInput(l, lab, greenPack, greenRate*cluster.Margin, cluster.Tag(lab.Name))
			if err != nil {
				return err
			}
			extra[lab] = []cluster.Port{p}
		}
		err = base.ConnectMore(l, zb, fresh, extra)
		if err == nil {
			for _, e := range l.Validate() {
				if !before[e] && !strings.Contains(e, "столбы не связаны") {
					return fmt.Errorf("раскладка: %v", e)
				}
			}
			for _, c := range fresh {
				h.Say("кластер %v", c)
			}
			if err := planDraw(h, rough.Zones, base, l, greenImage()); err != nil {
				return err
			}
			all := append(base.Sorted(), buses...)
			return writeState(planFile, ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: planImage()})
		}
		ce, ok := err.(*cluster.ConnectError)
		if !ok || try >= 5 {
			planDraw(h, rough.Zones, base, l, greenImage()) // что вышло — на картинку
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

// checkGreenPlan — в плане есть кластеры схем, инсертеров, лент и зелёной науки, у лабораторий —
// вход зелёной науки; картинка есть.
func checkGreenPlan(h *act.Hands) error {
	s, err := LoadPlan()
	if err != nil {
		return err
	}
	for _, p := range greenOrder {
		if !slices.ContainsFunc(s.Clusters, func(c *cluster.Cluster) bool { return c.Product == p && c.Area.W() > 0 }) {
			return fmt.Errorf("в плане нет кластера %s", p)
		}
	}
	if !slices.ContainsFunc(s.Clusters, func(c *cluster.Cluster) bool {
		return c.Kind == cluster.Lab && slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == greenPack })
	}) {
		return fmt.Errorf("у лабораторий нет входа %s", greenPack)
	}
	if st, err := os.Stat(greenImage()); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки %s", greenImage())
	}
	return nil
}
