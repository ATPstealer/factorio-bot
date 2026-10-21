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
	Zones     []cluster.Zone      `json:"zones"`
	Clusters  []*cluster.Cluster  `json:"clusters"` // прикидка машин по продуктам (внутри зон)
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
	register(Stage{N: 4, Name: "зоны всей базы", Run: runRough, Check: checkRough})
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
	base.MaxR = 2000 // вся база: радиус поиска мест — вся сгенерированная карта
	lay, err := base.PlaceLayered(cs, s4Belt)
	if err != nil {
		return err
	}
	zones := base.Zones(lay)
	// ресурсы на всю игру: сколько нужно и сколько можно добыть в зонах добычи; не хватает —
	// ещё зоны на следующих месторождениях этого ресурса (ближе к центру первыми)
	res := map[string]Resource{}
	for it, rate := range plan.Raw {
		if it == "water" {
			continue
		}
		r := Resource{Rate: rate, Need: rate * s4Hours * 3600}
		for _, z := range zones {
			r.Have += z.Resources[it]
		}
		if r.Have < r.Need {
			zones = s4MoreMines(h, zones, it, r.Need-r.Have)
			r.Have = 0
			for _, z := range zones {
				r.Have += z.Resources[it]
			}
		}
		r.Short = r.Have < r.Need
		res[it] = r
	}
	rp := RoughPlan{Hours: s4Hours, Targets: targets, Zones: zones, Clusters: base.Sorted(), Resources: res, PowerMW: plan.PowerW / 1e6, Image: s4Image()}
	sort.Slice(rp.Zones, func(i, j int) bool { return rp.Zones[i].Name < rp.Zones[j].Name })
	for _, z := range rp.Zones {
		h.Log("зона %v", z)
	}
	for it, r := range res {
		h.Log("сырьё %s: %.2f/с, на %.0f ч — %.0f, в зонах добычи %.0f%s", it, r.Rate, s4Hours, r.Need, r.Have, map[bool]string{true: " — НЕ ХВАТАЕТ"}[r.Short])
	}
	if err := s4Draw(h, rp.Zones, rp.Image); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rp, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(runPath(s4Plan), data, 0o644)
}

func s4Image() string { return filepath.Join("run/stages", "s4-"+seedOf()+".png") }

// s4Draw — картинка зон поверх карты.
func s4Draw(h *act.Hands, zones []cluster.Zone, image string) error {
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, z := range zones {
		minX, minY = min(minX, z.Rect.Min.X), min(minY, z.Rect.Min.Y)
		maxX, maxY = max(maxX, z.Rect.Max.X), max(maxY, z.Rect.Max.Y)
	}
	radius := max(maxX-minX, maxY-minY)/2 + 30
	scale := max(1, min(4, 2000/(2*radius)))
	ro := world.RenderOptions{Center: world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}, Radius: radius, Scale: scale, Labels: true}
	img := h.World.Render(ro)
	cluster.DrawZones(img, ro, zones)
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

// s4MoreMines — ещё зоны добычи ресурса it на месторождениях вне зон (ближние к центру первыми),
// пока запас в них не покроет lack.
func s4MoreMines(h *act.Hands, zones []cluster.Zone, it string, lack float64) []cluster.Zone {
	pts := h.World.Patches()
	sort.Slice(pts, func(i, j int) bool {
		return math.Hypot(pts[i].Center.X, pts[i].Center.Y) < math.Hypot(pts[j].Center.X, pts[j].Center.Y)
	})
	n := 0
	for _, z := range zones {
		if z.Kind == cluster.ZoneMine {
			n++
		}
	}
	for _, pt := range pts {
		if lack <= 0 {
			break
		}
		if pt.Name != it {
			continue
		}
		r := cluster.Rect{Min: pt.Min, Max: world.Tile{X: pt.Max.X + 1, Y: pt.Max.Y + 1}}.Grow(cluster.ZoneBorder)
		taken := false
		for _, z := range zones {
			taken = taken || z.Rect.Overlaps(r)
		}
		if taken {
			continue
		}
		n++
		z := cluster.Zone{Name: fmt.Sprintf("mine-%d", n), Kind: cluster.ZoneMine, Rect: r, Resources: map[string]float64{}}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if res, ok := h.World.ResourceAt(world.Tile{X: x, Y: y}); ok {
					z.Resources[res.Name] += float64(res.Amount)
				}
			}
		}
		lack -= z.Resources[it]
		zones = append(zones, z)
	}
	return zones
}

func checkRough(h *act.Hands) error {
	rp, err := LoadRough()
	if err != nil {
		return err
	}
	if len(rp.Zones) == 0 {
		return fmt.Errorf("зон нет")
	}
	for _, z := range rp.Zones {
		if z.Rect.W() == 0 || z.Rect.H() == 0 {
			return fmt.Errorf("зона %s без прямоугольника", z.Name)
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
