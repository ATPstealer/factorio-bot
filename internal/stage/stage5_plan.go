package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 5 (stages.md): план первых кластеров «на бумаге» (cluster.md) внутри зон стадии 4
// (zones.md) — добыча угля, железа, меди; плавка железа и меди; станция; шины; шестерни;
// красная наука; лаборатории; хранение. Цель — красная наука s5Rate/с, без запаса роста
// (запасы — в зонах). Картинка — зоны и кластеры с раскладкой поверх карты.
// Постройки точек стадии 2 при разметке не мешают: их разберут (стадия 10).

const (
	s5Plan    = "state-stage5.json"
	s5Rate    = 0.5 // красная наука, шт/с
	s5Reserve = 1   // без запаса роста: запасы — в зонах (zones.md)
	s5LabSec  = 10  // секунд исследования на пакет (automation и т.п.)
	// нет места — разведка: до s5Explore раз, каждый раз — пока не откроется s5ExploreChunks
	s5Explore       = 20
	s5ExploreChunks = 12
)

// s5Kinds — какие кластеры должны быть в плане (имя без номера).
var s5Kinds = []string{"coal", "iron-ore", "copper-ore", "iron-plate", "copper-plate", "power",
	"bus", "gear", "red-science", "lab", "storage"}

// ClusterPlan — план кластеров карты: кластеры с местами, размещений в раскладке, картинка.
type ClusterPlan struct {
	Clusters []*cluster.Cluster `json:"clusters"`
	Placed   int                `json:"placed"` // размещений внутри кластеров и логистики
	Layout   []layout.Placement `json:"layout"` // раскладка: по ней строят следующие стадии
	Image    string             `json:"image"`
}

func init() {
	register(Stage{N: 5, Name: "план кластеров", Run: runPlan, Check: checkPlan})
}

// LoadPlan — план стадии 5 этой карты (для следующих стадий).
func LoadPlan() (*ClusterPlan, error) {
	data, err := os.ReadFile(runPath(s5Plan))
	if err != nil {
		return nil, fmt.Errorf("нет плана кластеров (стадия 5): %w", err)
	}
	var s ClusterPlan
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func runPlan(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	rough, err := LoadRough()
	if err != nil {
		return err
	}
	pr := h.Protos
	targets := map[string]float64{"automation-science-pack": s5Rate}
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
	// кластер, к которому ленту не провести, — в другое место в своей зоне (до 6 попыток)
	for try := 0; ; try++ {
		cs, err := cluster.Split(pr, plan, s5Reserve, nil, "iron-gear-wheel")
		if err != nil {
			return err
		}
		cs = append(cs, cluster.Labs(pr, targets, s5LabSec, s5Reserve), cluster.StorageCluster())
		if cs, err = cluster.AddPower(pr, cs, s5Reserve, nil); err != nil {
			return err
		}
		base := cluster.NewBase(pr, h.World, world.Tile{}) // центр базы — место старта
		base.Log = h.Log
		base.Avoid = avoid
		base.StorageNear, base.Keep = s5Start(own)
		buses, err := base.PlaceZoned(cs, rough.Zones)
		if err != nil {
			return err
		}
		l, err := base.Layout()
		if err != nil {
			return err
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
			for _, c := range all {
				h.Say("кластер %v", c)
			}
			if err := s5Draw(h, rough.Zones, base, l); err != nil {
				return err
			}
			data, err := json.MarshalIndent(ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: s5Image()}, "", " ")
			if err != nil {
				return err
			}
			return os.WriteFile(runPath(s5Plan), data, 0o644)
		}
		ce, ok := err.(*cluster.ConnectError)
		if !ok || try >= 5 {
			s5Draw(h, rough.Zones, base, l) // что вышло — на картинку
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

// s5Image — картинка плана: run/stages/s5-<сид>.png.
func s5Image() string {
	var m struct {
		Seed int64 `json:"seed"`
	}
	if data, err := os.ReadFile(runPath("map.json")); err == nil {
		json.Unmarshal(data, &m)
	}
	return filepath.Join("run/stages", fmt.Sprintf("s5-%d.png", m.Seed))
}

// s5Draw — картинка: карта, зоны, кластеры с бордюрами и раскладка.
func s5Draw(h *act.Hands, zones []cluster.Zone, base *cluster.Base, l *layout.Layout) error {
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
	ro := world.RenderOptions{Center: world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2},
		Radius: radius, Scale: max(1, min(4, 2400/(2*radius))), Labels: true}
	img := h.World.Render(ro)
	cluster.DrawZones(img, ro, zones)
	base.Draw(img, ro)
	if l != nil {
		l.Draw(img, ro)
	}
	f, err := os.Create(s5Image())
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
	for _, k := range s5Kinds {
		if have[k] == 0 {
			return fmt.Errorf("в плане нет кластера %s", k)
		}
	}
	if st, err := os.Stat(s.Image); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки плана %s", s.Image)
	}
	return nil
}

// s5Start — середина стартовых буров (точки стадии 2) и места всех своих построек: хранение —
// рядом с ними (носить недалеко), но не на них (zones.md).
func s5Start(own []world.Object) (*world.Tile, []cluster.Rect) {
	var keep []cluster.Rect
	var sx, sy float64
	n := 0
	for _, o := range own {
		t := world.TileOf(o.Pos)
		keep = append(keep, cluster.Rect{Min: world.Tile{X: t.X - 1, Y: t.Y - 1}, Max: world.Tile{X: t.X + 2, Y: t.Y + 2}})
		if strings.HasSuffix(o.Name, "mining-drill") {
			sx, sy, n = sx+o.Pos.X, sy+o.Pos.Y, n+1
		}
	}
	if n == 0 {
		return nil, keep
	}
	return &world.Tile{X: int(math.Round(sx / float64(n))), Y: int(math.Round(sy / float64(n)))}, keep
}
