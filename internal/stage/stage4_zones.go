package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/world"
)

// Стадия 4 (stages.md): прикидочный план всей базы до запуска ракеты за roughHours часов, конечные
// машины — AM2 и стальные печи. Без раскладок: блоки (кластеры по продукту) с машинами и
// площадью, шина по уровням (ленты — красные), хватит ли ресурсов на всю игру, зоны на карте
// (центр базы — место старта), картинка. Следующие стадии строят внутри зон.

const (
	roughFile        = "state-zones.json"
	roughHours       = 20.0
	roughResearch    = 0.8  // доля времени на исследования (остальное — ракетные части)
	roughRocket      = 100  // ракетных частей на ракету
	roughBelt        = 30.0 // лента шины — красная, шт/с
	roughExplore     = 20   // нет места — разведать (сгенерировать ходьбой) и заново
	roughChunks      = 16   // за одну разведку — новых чанков
	roughReopen      = 3    // дооткрыть землю у базы и разметить заново — до стольких раз
	roughMarginTiles = 64   // у базы: рамка зон + столько тайлов — должно быть открыто
	roughMargin      = 1.0  // запас роста в прикидке: план — уже под конечный темп
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
	register(Stage{N: 4, Name: "зоны всей базы", Run: runRough, Check: checkRough, Plan: true})
}

// LoadRough — прикидка базы этой карты (для следующих стадий).
func LoadRough() (*RoughPlan, error) {
	data, err := os.ReadFile(runPath(roughFile))
	if err != nil {
		return nil, fmt.Errorf("нет прикидки базы (стадия 4): %w", err)
	}
	var r RoughPlan
	return &r, json.Unmarshal(data, &r)
}

// roughTargets — цели: пакеты науки по цене всех исследований до ракетной шахты за долю времени
// roughResearch, ракетные части — за остальное.
func roughTargets(h *act.Hands) (map[string]float64, float64, error) {
	techs, err := h.Protos.TechPath([]string{"rocket-silo"}, nil)
	if err != nil {
		return nil, 0, err
	}
	cost := h.Protos.Cost(techs)
	research := roughHours * 3600 * roughResearch
	targets := map[string]float64{"rocket-part": roughRocket / (roughHours * 3600 * (1 - roughResearch))}
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
	opened := 0 // дооткрытий у базы (stages.md, стадия 4)
	for try := 0; ; try++ {
		err := roughOnce(h)
		var ns *cluster.NoSiteError
		if err == nil {
			// у базы неоткрытая земля — открыть и разметить заново: база жмётся к краю
			// открытого (264771462, начало bus-1 у неоткрытого)
			in, ok := roughUnknown(h)
			if !ok || opened >= roughReopen {
				return nil
			}
			opened++
			h.Say("у базы неоткрытая земля — иду открыть и размечу зоны заново (%d из %d)", opened, roughReopen)
			if err := h.ExploreWhere(ctx, in, func() bool { _, more := roughUnknown(h); return !more }); err != nil && !errors.Is(err, act.ErrNothingToExplore) {
				return err
			}
			continue
		}
		if !errors.As(err, &ns) || try >= roughExplore {
			return err
		}
		n0 := len(h.World.Chunks)
		h.Say("%v — иду, чтобы игра сгенерировала карту дальше", err)
		if err := h.Explore(ctx, func() bool { return len(h.World.Chunks) >= n0+roughChunks }); err != nil {
			return err
		}
	}
}

// roughAnchor — центр базы по всей открытой карте (zones.md): у какого пятна угля, железа или
// меди меньше всего сумма расстояний до ближайших крупных (не меньше трети нужного на игру)
// пятен каждого из трёх ресурсов; к месту старта не тянется.
func roughAnchor(h *act.Hands, need map[string]float64) world.Tile {
	res := []string{"coal", "iron-ore", "copper-ore"}
	big := map[string][]world.Patch{}
	for _, p := range h.World.Patches() {
		for _, r := range res {
			if p.Name == r && float64(p.Amount) >= need[r]/3 {
				big[r] = append(big[r], p)
			}
		}
	}
	best, bd := world.Tile{}, math.Inf(1)
	for _, r := range res {
		for _, c := range big[r] {
			d := 0.0
			for _, r2 := range res {
				m := math.Inf(1)
				for _, p := range big[r2] {
					m = math.Min(m, math.Hypot(p.Center.X-c.Center.X, p.Center.Y-c.Center.Y))
				}
				d += m
			}
			if d < bd {
				best, bd = world.TileOf(c.Center), d
			}
		}
	}
	return best
}

// roughUnknown — у зон базы (без добычи; рамка + roughMarginTiles) есть неоткрытые чанки, до
// которых можно дойти (рядом — открытый); in — чанк в этой рамке.
func roughUnknown(h *act.Hands) (func(bridge.ChunkPos) bool, bool) {
	rp, err := LoadRough()
	if err != nil {
		return nil, false
	}
	var box *cluster.Rect
	for _, z := range rp.Zones {
		if z.Kind == cluster.ZoneMine {
			continue
		}
		r := z.Rect
		if box == nil {
			box = &r
			continue
		}
		box.Min.X, box.Min.Y = min(box.Min.X, r.Min.X), min(box.Min.Y, r.Min.Y)
		box.Max.X, box.Max.Y = max(box.Max.X, r.Max.X), max(box.Max.Y, r.Max.Y)
	}
	if box == nil {
		return nil, false
	}
	g := box.Grow(roughMarginTiles)
	in := func(c bridge.ChunkPos) bool {
		x, y := c.X*32, c.Y*32
		return x+32 > g.Min.X && x < g.Max.X && y+32 > g.Min.Y && y < g.Max.Y
	}
	for cy := g.Min.Y >> 5; cy <= g.Max.Y>>5; cy++ {
		for cx := g.Min.X >> 5; cx <= g.Max.X>>5; cx++ {
			c := bridge.ChunkPos{X: cx, Y: cy}
			if h.World.Chunks[c] != nil {
				continue
			}
			for _, n := range []bridge.ChunkPos{{X: cx + 1, Y: cy}, {X: cx - 1, Y: cy}, {X: cx, Y: cy + 1}, {X: cx, Y: cy - 1}} {
				if h.World.Chunks[n] != nil {
					return in, true
				}
			}
		}
	}
	return in, false
}

// roughOnce — прикидка базы и разметка зон; центр базы — по всей открытой карте (roughAnchor).
func roughOnce(h *act.Hands) error {
	pr := h.Protos
	targets, labSec, err := roughTargets(h)
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
	cs, err := cluster.Split(pr, plan, roughMargin, nil)
	if err != nil {
		return err
	}
	packs := map[string]float64{}
	for it, v := range targets {
		if strings.HasSuffix(it, "-science-pack") {
			packs[it] = v
		}
	}
	cs = append(cs, cluster.Labs(pr, packs, labSec, roughMargin), cluster.StorageCluster())
	if cs, err = cluster.AddPower(pr, cs, roughMargin, nil); err != nil {
		return err
	}
	// станция и её зона — под одну помпу (cluster.md §2а «Станция»)
	cluster.SizePower(pr, cs)
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
	cluster.BeltRate = roughBelt
	defer func() { cluster.BeltRate = saved }()
	// зоны добычи — по месторождениям: на каждый ресурс — пятна, пока запаса хватит на игру
	need := map[string]float64{}
	for it, rate := range plan.Raw {
		if it != "water" {
			need[it] = rate * roughHours * 3600
		}
	}
	origin := roughAnchor(h, need)
	h.Log("центр базы — у крупных пятен угля, железа и меди: (%d, %d)", origin.X, origin.Y)
	base := cluster.NewBase(pr, h.World, origin)
	base.Log, base.Rough = h.Log, true
	base.MaxR = 2000 // вся база: радиус поиска мест — вся сгенерированная карта
	base.MineZones(cs, need)
	lay, err := base.PlaceLayered(cs, roughBelt)
	if err != nil {
		return err
	}
	zones := base.Zones(lay)
	// ресурсы на всю игру: сколько нужно и сколько можно добыть в зонах добычи
	res := map[string]Resource{}
	for it, n := range need {
		r := Resource{Rate: n / (roughHours * 3600), Need: n}
		for _, z := range zones {
			r.Have += z.Resources[it]
		}
		r.Short = r.Have < r.Need
		res[it] = r
	}
	rp := RoughPlan{Hours: roughHours, Targets: targets, Zones: zones, Clusters: base.Sorted(), Resources: res, PowerMW: plan.PowerW / 1e6, Image: roughImage()}
	sort.Slice(rp.Zones, func(i, j int) bool { return rp.Zones[i].Name < rp.Zones[j].Name })
	for _, z := range rp.Zones {
		h.Log("зона %v", z)
	}
	for it, r := range res {
		h.Log("сырьё %s: %.2f/с, на %.0f ч — %.0f, в зонах добычи %.0f%s", it, r.Rate, roughHours, r.Need, r.Have, map[bool]string{true: " — НЕ ХВАТАЕТ"}[r.Short])
	}
	if err := roughDraw(h, rp.Zones, rp.Image); err != nil {
		return err
	}
	return writeState(roughFile, rp)
}

// roughImage — картинка зон: run/stages/zones-<сид>.png.
func roughImage() string { return image("zones") }

// roughDraw — картинка зон поверх карты.
func roughDraw(h *act.Hands, zones []cluster.Zone, image string) error {
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
