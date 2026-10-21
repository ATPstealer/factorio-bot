package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/world"
)

// Стадия 4 (stages.md): прикидочный план всей базы до запуска ракеты за s4Hours часов, конечные
// машины — AM2 и стальные печи. Без раскладок: блоки (кластеры по продукту) с машинами и
// площадью, шина по уровням (ленты — красные), хватит ли ресурсов на всю игру, зоны на карте
// (центр базы — место старта), картинка. Следующие стадии строят внутри зон.

const (
	s4Plan     = "state-stage4.json"
	s4Hours    = 20.0
	s4Research = 0.8  // доля времени на исследования (остальное — ракетные части)
	s4Rocket   = 100  // ракетных частей на ракету
	s4Belt     = 30.0 // лента шины — красная, шт/с
	s4Explore  = 20   // нет места — разведать (сгенерировать ходьбой) и заново
	s4Chunks   = 16   // за одну разведку — новых чанков
	s4Margin   = 1.0  // запас роста в прикидке: план — уже под конечный темп
)

// RoughPlan — прикидка базы: цели, блоки с зонами, ленты шины, ресурсы.
type RoughPlan struct {
	Hours     float64             `json:"hours"`
	Targets   map[string]float64  `json:"targets"`
	Clusters  []*cluster.Cluster  `json:"clusters"`
	Lanes     []cluster.Lane      `json:"lanes"`
	Resources map[string]Resource `json:"resources"`
	PowerMW   float64             `json:"power_mw"`
	Image     string              `json:"image"`
}

// Resource — сырьё на всю игру: нужно и есть в месторождениях сгенерированной карты.
type Resource struct {
	Need  float64 `json:"need"`
	Have  float64 `json:"have"`
	Rate  float64 `json:"rate"`
	Short bool    `json:"short,omitempty"`
}

func init() {
	register(Stage{N: 4, Name: "прикидочный план всей базы", Run: runRough, Check: checkRough})
}

// LoadRough — прикидка базы этой карты (для следующих стадий).
func LoadRough() (*RoughPlan, error) {
	data, err := os.ReadFile(runPath(s4Plan))
	if err != nil {
		return nil, fmt.Errorf("нет прикидки базы (стадия 4): %w", err)
	}
	var r RoughPlan
	return &r, json.Unmarshal(data, &r)
}

// s4Targets — цели: пакеты науки по цене всех исследований до ракетной шахты за долю времени
// s4Research, ракетные части — за остальное.
func s4Targets(h *act.Hands) (map[string]float64, float64, error) {
	techs, err := h.Protos.TechPath([]string{"rocket-silo"}, nil)
	if err != nil {
		return nil, 0, err
	}
	cost := h.Protos.Cost(techs)
	research := s4Hours * 3600 * s4Research
	targets := map[string]float64{"rocket-part": s4Rocket / (s4Hours * 3600 * (1 - s4Research))}
	units := 0.0
	for pack, n := range cost.Packs {
		targets[pack] = n / research
		units = math.Max(units, n)
	}
	if units == 0 {
		return nil, 0, fmt.Errorf("цена исследований до ракетной шахты — ноль")
	}
	return targets, cost.Seconds / units, nil
}

func runRough(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	for try := 0; ; try++ {
		err := roughOnce(h)
		var ns *cluster.NoSiteError
		if !errors.As(err, &ns) || try >= s4Explore {
			return err
		}
		n0 := len(h.World.Chunks)
		h.Say("%v — иду, чтобы игра сгенерировала карту дальше", err)
		if err := h.Explore(ctx, func() bool { return len(h.World.Chunks) >= n0+s4Chunks }); err != nil {
			return err
		}
	}
}

func roughOnce(h *act.Hands) error {
	pr := h.Protos
	targets, labSec, err := s4Targets(h)
	if err != nil {
		return err
	}
	var names []string
	for it := range targets {
		names = append(names, it)
	}
	sort.Strings(names)
	for _, it := range names {
		h.Log("цель: %s %.3f/с", it, targets[it])
	}
	plan, err := calc.Solve(pr, targets, calc.Options{All: true,
		Machines: map[string]string{"smelting": "steel-furnace", "crafting": "assembling-machine-2"}})
	if err != nil {
		return err
	}
	cs, err := cluster.Split(pr, plan, s4Margin, nil)
	if err != nil {
		return err
	}
	packs := map[string]float64{}
	for it, v := range targets {
		if strings.HasSuffix(it, "-science-pack") {
			packs[it] = v
		}
	}
	cs = append(cs, cluster.Labs(pr, packs, labSec, s4Margin), cluster.StorageCluster())
	if cs, err = cluster.AddPower(pr, cs, s4Margin, nil); err != nil {
		return err
	}
	// свои постройки (точки стадии 2) — не помеха: их разберут
	var own []world.Object
	for _, o := range h.World.Objects() {
		if o.Own {
			own = append(own, o)
		}
	}
	restore := h.World.Hide(own)
	defer restore()
	saved := cluster.BeltRate
	cluster.BeltRate = s4Belt
	defer func() { cluster.BeltRate = saved }()
	base := cluster.NewBase(pr, h.World, world.Tile{}) // центр базы — место старта (0, 0)
	base.Log, base.Rough = h.Log, true
	if err := base.PlaceAll(cs); err != nil {
		return err
	}
	// ресурсы на всю игру
	res := map[string]Resource{}
	have := map[string]float64{}
	for _, pt := range h.World.Patches() {
		have[pt.Name] += float64(pt.Amount)
	}
	for it, rate := range plan.Raw {
		if it == "water" {
			continue
		}
		r := Resource{Rate: rate, Need: rate * s4Hours * 3600, Have: have[it]}
		r.Short = r.Have < r.Need
		res[it] = r
	}
	rp := RoughPlan{Hours: s4Hours, Targets: targets, Clusters: base.Sorted(), Resources: res, PowerMW: plan.PowerW / 1e6, Image: s4Image()}
	if bus := base.Bus(); bus != nil {
		rp.Lanes = bus.Bus.Lanes
	}
	for _, c := range rp.Clusters {
		h.Log("блок %v", c)
	}
	for it, r := range res {
		h.Log("сырьё %s: %.2f/с, на %0.f ч — %.0f, в месторождениях %.0f%s", it, r.Rate, s4Hours, r.Need, r.Have, map[bool]string{true: " — НЕ ХВАТАЕТ"}[r.Short])
	}
	if err := s4Draw(h, base, rp.Image); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rp, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(runPath(s4Plan), data, 0o644)
}

func s4Image() string { return filepath.Join("run/stages", "s4-"+seedOf()+".png") }

// s4Draw — картинка зон: открытая карта и кластеры с коридорами (без раскладок).
func s4Draw(h *act.Hands, base *cluster.Base, image string) error {
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, c := range base.Clusters {
		r := c.Area.Grow(cluster.Corridor)
		minX, minY = min(minX, r.Min.X), min(minY, r.Min.Y)
		maxX, maxY = max(maxX, r.Max.X), max(maxY, r.Max.Y)
	}
	radius := max(maxX-minX, maxY-minY)/2 + 20
	scale := max(1, min(4, 2000/(2*radius)))
	ro := world.RenderOptions{Center: world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}, Radius: radius, Scale: scale, Labels: true}
	img := h.World.Render(ro)
	base.Draw(img, ro)
	f, err := os.Create(image)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func checkRough(h *act.Hands) error {
	rp, err := LoadRough()
	if err != nil {
		return err
	}
	for _, c := range rp.Clusters {
		if c.Area.W() == 0 || c.Area.H() == 0 {
			return fmt.Errorf("блок %s без зоны", c.Name)
		}
	}
	for it, r := range rp.Resources {
		if r.Short {
			return fmt.Errorf("%s не хватит: нужно %.0f, в месторождениях %.0f", it, r.Need, r.Have)
		}
	}
	if st, err := os.Stat(rp.Image); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки прикидки %s", rp.Image)
	}
	return nil
}
