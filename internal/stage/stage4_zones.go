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
	saveWorld(h)
	opened := 0 // дооткрытий у базы (stages.md, стадия 4)
	roughRegion, roughCliffs = nil, false
	var regions []cluster.Rect // соседние квадраты (места в нынешнем нет)
	for {
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
		if !errors.As(err, &ns) {
			return err
		}
		// места нет — другой квадрат 2048×2048 в 2000 тайлах (один из 8 соседних, ближние
		// первыми): сходить, открыть, разметить только в нём (stages.md, решение пользователя)
		if regions == nil {
			need, err := roughNeed(h)
			if err != nil {
				return err
			}
			regions = roughRegions(h, roughAnchor(h, need))
		}
		if len(regions) == 0 {
			if roughCliffs {
				return fmt.Errorf("%w; и в 8 соседних квадратах, и базой с утёсами тоже", err)
			}
			// последнее — база с утёсами по всей открытой карте (zones.md, решение пользователя)
			h.Say("%v — и в 8 соседних квадратах нет: размечаю базу с утёсами в зонах (зоны шире)", err)
			roughCliffs, roughRegion = true, nil
			continue
		}
		r := regions[0]
		regions = regions[1:]
		roughRegion = &r
		cx, cy := r.Center()
		h.Say("%v — перехожу в другой квадрат: центр (%.0f, %.0f), открываю его", err, cx, cy)
		in := func(c bridge.ChunkPos) bool {
			return c.X*32+32 > r.Min.X && c.X*32 < r.Max.X && c.Y*32+32 > r.Min.Y && c.Y*32 < r.Max.Y
		}
		more := func() bool {
			for c := range h.World.Chunks {
				for _, d := range []bridge.ChunkPos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
					u := bridge.ChunkPos{X: c.X + d.X, Y: c.Y + d.Y}
					if h.World.Chunks[u] == nil && in(u) {
						return true
					}
				}
			}
			return false
		}
		// туда — коридором от игрока к центру квадрата (разведка берёт только край открытого:
		// дальний квадрат с ним не граничит — «открывался» за секунду, 1028292478)
		if p, err := h.B.Player(); err == nil {
			ax, ay := p.Position.X, p.Position.Y
			goal := world.ChunkOf(world.Tile{X: int(cx), Y: int(cy)})
			corridor := func(c bridge.ChunkPos) bool {
				// расстояние от чанка до отрезка игрок — центр, в чанках
				px, py := float64(c.X*32+16), float64(c.Y*32+16)
				dx, dy := cx-ax, cy-ay
				l2 := dx*dx + dy*dy
				t := 0.0
				if l2 > 0 {
					t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/l2))
				}
				return math.Hypot(px-ax-t*dx, py-ay-t*dy) <= 96
			}
			err := h.ExploreWhere(ctx, corridor, func() bool { return h.World.Chunks[goal] != nil })
			if err != nil && !errors.Is(err, act.ErrNothingToExplore) {
				return err
			}
		}
		if err := h.ExploreWhere(ctx, in, func() bool { return !more() }); err != nil && !errors.Is(err, act.ErrNothingToExplore) {
			return err
		}
		opened = 0
	}
}

// roughCliffs — база с утёсами (zones.md): обычным способом места нигде не нашлось.
var roughCliffs bool

// roughRegion — квадрат, в котором размечается база (nil — вся открытая карта): места не
// нашлось — следующий из соседних (runRough).
var roughRegion *cluster.Rect

// roughRegions — 8 квадратов 2048×2048 со сдвигом на 2000 тайлов от center, ближние к игроку
// первыми.
// WorldFile — открытая карта перед разметкой зон: run/worlds/<сид>.gob (разметка без игры —
// bin/bot rough).
func WorldFile() string { return filepath.Join("run/worlds", seedOf()+".gob") }

func saveWorld(h *act.Hands) {
	os.MkdirAll(filepath.Dir(WorldFile()), 0o755)
	if err := h.World.Save(WorldFile()); err != nil {
		h.Log("карта в файл: %v", err)
	}
}

// RoughOffline — разметка зон без игры по сохранённой карте (h.World загружен, h.B нет): одна
// попытка в открытом, без дооткрытий и переходов в другие квадраты.
func RoughOffline(h *act.Hands) error {
	roughRegion, roughCliffs = nil, false
	return roughOnce(h)
}

func roughRegions(h *act.Hands, center world.Tile) []cluster.Rect {
	var res []cluster.Rect
	for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		c := world.Tile{X: center.X + d[0]*2000, Y: center.Y + d[1]*2000}
		res = append(res, cluster.Rect{Min: world.Tile{X: c.X - 1024, Y: c.Y - 1024}, Max: world.Tile{X: c.X + 1024, Y: c.Y + 1024}})
	}
	if p, err := h.B.Player(); err == nil {
		sort.SliceStable(res, func(i, j int) bool {
			xi, yi := res[i].Center()
			xj, yj := res[j].Center()
			return math.Hypot(xi-p.Position.X, yi-p.Position.Y) < math.Hypot(xj-p.Position.X, yj-p.Position.Y)
		})
	}
	return res
}

// roughAnchor — центр базы по всей открытой карте (zones.md): у какого пятна угля, железа или
// меди меньше всего сумма расстояний до ближайших крупных (не меньше трети нужного на игру)
// пятен каждого из трёх ресурсов; к месту старта не тянется.
func roughAnchor(h *act.Hands, need map[string]float64) world.Tile {
	res := []string{"coal", "iron-ore", "copper-ore"}
	big := map[string][]world.Patch{}
	for _, p := range h.World.Patches() {
		if g := roughRegion; g != nil && !(int(p.Center.X) >= g.Min.X && int(p.Center.X) < g.Max.X && int(p.Center.Y) >= g.Min.Y && int(p.Center.Y) < g.Max.Y) {
			continue // только в квадрате разметки
		}
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
	if math.IsInf(bd, 1) && roughRegion != nil { // в квадрате крупных пятен нет — его центр
		x, y := roughRegion.Center()
		return world.Tile{X: int(x), Y: int(y)}
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
// roughOnce — разметка зон: центр базы — по всей открытой карте (roughAnchor); база встала —
// пятна добычи заново от головы её bus-1 и разметка ещё раз (zones.md: добыча — ближняя ко
// входу bus-1; от центра шина вставала в 300 тайлах от зон добычи — 238178543).
func roughOnce(h *act.Hands) error {
	head, err := roughPass(h, nil)
	if err != nil || head == nil {
		return err
	}
	h.Log("зоны добычи — заново от головы bus-1 (%d, %d)", head.X, head.Y)
	if _, err := roughPass(h, head); err != nil {
		h.Log("разметка от головы bus-1 не встала (%v) — остаётся от центра", err)
		_, err = roughPass(h, nil)
		return err
	}
	return nil
}

// roughPass — одна разметка; origin — от чего ближние пятна добычи и база (nil — центр у крупных
// пятен по всей карте). Возвращает голову bus-1 (левый край, середина по высоте).
func roughPass(h *act.Hands, from *world.Tile) (*world.Tile, error) {
	pr := h.Protos
	targets, labSec, err := roughTargets(h)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	cs, err := cluster.Split(pr, plan, roughMargin, nil)
	if err != nil {
		return nil, err
	}
	packs := map[string]float64{}
	for it, v := range targets {
		if strings.HasSuffix(it, "-science-pack") {
			packs[it] = v
		}
	}
	cs = append(cs, cluster.Labs(pr, packs, labSec, roughMargin), cluster.StorageCluster())
	if cs, err = cluster.AddPower(pr, cs, roughMargin, nil); err != nil {
		return nil, err
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
	if from != nil {
		origin = *from
	}
	h.Log("центр базы (%d, %d)", origin.X, origin.Y)
	base := cluster.NewBase(pr, h.World, origin)
	base.Log, base.Rough, base.CliffZones = h.Log, true, roughCliffs
	base.MaxR = 2000 // вся база: радиус поиска мест — вся сгенерированная карта
	base.MineZones(cs, need)
	lay, err := base.PlaceLayered(cs, roughBelt)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	var head *world.Tile
	for _, z := range rp.Zones {
		if z.Name == "bus-1" {
			_, y := z.Rect.Center()
			head = &world.Tile{X: z.Rect.Min.X, Y: int(y)}
		}
	}
	return head, writeState(roughFile, rp)
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
	scale := max(3, min(12, 8000/(2*radius))) // крупно (пользователь: большие картинки)
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
