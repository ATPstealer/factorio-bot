package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 18 (stages.md): проект кластеров зелёной науки — план стадии 5 дописывается, а не
// перестраивается: построенное — препятствие, новые кластеры (провод и схемы, инсертеры, ленты —
// шестерни внутри парами, зелёная наука) встают в свои зоны, лаборатории получают второй вход.

const (
	s18Rate = 0.1 // зелёная наука, шт/с
	// s18Base — план стадии 5 до дописывания: перезапуск стадии — с него, не с дописанного
	s18Base  = "state-stage18-base.json"
	s18Green = "logistic-science-pack"
)

// s18Separate — отдельными кластерами (не внутрь потребителя): у каждого своя лента шины.
var s18Separate = []string{"electronic-circuit", "inserter", "transport-belt"}

// s18Order — порядок новых кластеров: поставщики раньше потребителей (схемы — инсертерам с шины
// над этажом: инсертеры правее).
var s18Order = []string{"electronic-circuit", "transport-belt", "inserter", s18Green}

func init() {
	register(Stage{N: 18, Name: "проект зелёной науки", Run: runGreenPlan, Check: checkGreenPlan})
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

// s18Old — план до дописывания (при первом запуске — копия плана стадии 5).
func s18Old() (*ClusterPlan, error) {
	if _, err := os.Stat(runPath(s18Base)); err != nil {
		data, err := os.ReadFile(runPath(s5Plan))
		if err != nil {
			return nil, fmt.Errorf("нет плана кластеров (стадия 5): %w", err)
		}
		if err := os.WriteFile(runPath(s18Base), data, 0o644); err != nil {
			return nil, err
		}
	}
	return loadPlanFile(runPath(s18Base))
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
	plan, err := calc.Solve(pr, map[string]float64{s18Green: s18Rate}, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return err
	}
	old, err := s18Old()
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
	// свои постройки — не помеха (они в плане); прочие — как обычно
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
	for try := 0; ; try++ {
		old, err := loadPlanFile(runPath(s18Base)) // заново: прошлая попытка меняла кластеры
		if err != nil {
			return err
		}
		cs, err := cluster.Split(pr, plan, s5Reserve, nil, s18Separate...)
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
			return slices.Index(s18Order, a.Product) - slices.Index(s18Order, b.Product)
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
		var lab *cluster.Cluster
		for _, c := range base.Clusters {
			if c.Kind == cluster.Lab {
				lab = c
			}
		}
		extra := map[*cluster.Cluster][]cluster.Port{}
		if lab != nil && !slices.ContainsFunc(lab.In, func(f cluster.Flow) bool { return f.Item == s18Green }) {
			p, err := cluster.AddInput(l, lab, s18Green, s18Rate*cluster.Margin, cluster.Tag(lab.Name))
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
			s18Report(h, base, plan)
			if err := s5Draw(h, rough.Zones, base, l, stageImage(18)); err != nil {
				return err
			}
			all := append(base.Sorted(), buses...)
			data, err := json.MarshalIndent(ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: s5Image()}, "", " ")
			if err != nil {
				return err
			}
			return os.WriteFile(runPath(s5Plan), data, 0o644)
		}
		ce, ok := err.(*cluster.ConnectError)
		if !ok || try >= 5 {
			s5Draw(h, rough.Zones, base, l, stageImage(18)) // что вышло — на картинку
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

// s18Report — сказать, хватит ли старой плавки и станции на красную и зелёную науку вместе
// (мягко: их рост — отдельными шагами).
func s18Report(h *act.Hands, base *cluster.Base, plan *calc.Plan) {
	have, use := map[string]float64{}, map[string]float64{}
	power, gen := 0.0, 0.0
	for _, c := range base.Clusters {
		// выход — по стоящим машинам (Count, не меньше 5), а не по темпу плана
		k, m := 1.0, 0.0
		for _, u := range c.Units {
			m += u.Machines
		}
		if c.Kind == cluster.Smelt && m > 0 {
			k = float64(c.Count) / m
		}
		for _, f := range c.Out {
			have[f.Item] += f.Rate * k
		}
		for _, f := range c.In {
			use[f.Item] += f.Rate
		}
		if c.Kind == cluster.Power { // паровой двигатель — 900 кВт
			for _, u := range c.Units {
				if u.Machine == "steam-engine" {
					gen += math.Ceil(u.Machines-1e-9) * 900e3
				}
			}
		} else {
			power += c.PowerW
		}
	}
	for _, it := range []string{"iron-plate", "copper-plate"} {
		if use[it] > have[it]*1.001 {
			h.Say("проверка: %s — нужно %.2f/с, плавка даёт %.2f/с (плавку растить)", it, use[it], have[it])
		}
	}
	if power > gen*1.001 {
		h.Say("проверка: питание — нужно %.0f кВт, станция на %.0f кВт (станцию растить)", power/1e3, gen/1e3)
	}
}

// checkGreenPlan — в плане есть кластеры схем, инсертеров, лент и зелёной науки, у лабораторий —
// вход зелёной науки; картинка есть.
func checkGreenPlan(h *act.Hands) error {
	s, err := LoadPlan()
	if err != nil {
		return err
	}
	for _, p := range s18Order {
		if !slices.ContainsFunc(s.Clusters, func(c *cluster.Cluster) bool { return c.Product == p && c.Area.W() > 0 }) {
			return fmt.Errorf("в плане нет кластера %s", p)
		}
	}
	if !slices.ContainsFunc(s.Clusters, func(c *cluster.Cluster) bool {
		return c.Kind == cluster.Lab && slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == s18Green })
	}) {
		return fmt.Errorf("у лабораторий нет входа %s", s18Green)
	}
	if st, err := os.Stat(stageImage(18)); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки %s", stageImage(18))
	}
	return nil
}
