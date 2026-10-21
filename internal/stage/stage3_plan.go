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
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 3 (stages.md): план первых 11 кластеров «на бумаге» (cluster.md) — добыча угля,
// железа, меди; плавка железа и меди; станция; шина; шестерни; красная наука; лаборатории;
// хранение. Цель — красная наука s3Rate/с. Картинка — план поверх открытой карты.
// Постройки точек стадии 2 при разметке не мешают: их разберут (стадия 8).

const (
	s3Plan    = "run/state-stage3.json"
	s3Rate    = 0.5 // красная наука, шт/с
	s3Reserve = 5   // запас роста ×5 по длине
	s3LabSec  = 10  // секунд исследования на пакет (automation и т.п.)
	// нет места — разведка: до s3Explore раз, каждый раз — пока не откроется s3ExploreChunks
	s3Explore       = 20
	s3ExploreChunks = 12
)

// s3Kinds — какие кластеры должны быть в плане (имя без номера).
var s3Kinds = []string{"coal", "iron-ore", "copper-ore", "iron-plate", "copper-plate", "power",
	"bus", "gear", "red-science", "lab", "storage"}

// S3 — план кластеров карты: кластеры с местами, размещений в раскладке, картинка.
type S3 struct {
	Clusters []*cluster.Cluster `json:"clusters"`
	Placed   int                `json:"placed"` // размещений внутри кластеров и логистики
	Layout   []layout.Placement `json:"layout"` // раскладка: по ней строят следующие стадии
	Image    string             `json:"image"`
}

func init() {
	register(Stage{N: 3, Name: "план кластеров", Run: runPlan, Check: checkPlan})
}

// LoadPlan — план стадии 3 этой карты (для следующих стадий).
func LoadPlan() (*S3, error) {
	data, err := os.ReadFile(s3Plan)
	if err != nil {
		return nil, fmt.Errorf("нет плана кластеров (стадия 3): %w", err)
	}
	var s S3
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// runPlan — план; кластеру нет места на открытой карте — разведать (мир бесконечный: подошёл
// к краю — игра открывает новые чанки) и спланировать заново.
func runPlan(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	for try := 0; ; try++ {
		err := planOnce(ctx, h)
		var ns *cluster.NoSiteError
		if !errors.As(err, &ns) || try >= s3Explore {
			return err
		}
		n0 := len(h.World.Chunks)
		h.Say("%v — разведываю", err)
		if err := h.Explore(ctx, func() bool { return len(h.World.Chunks) >= n0+s3ExploreChunks }); err != nil {
			return err
		}
	}
}

func planOnce(ctx context.Context, h *act.Hands) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	pr := h.Protos
	targets := map[string]float64{"automation-science-pack": s3Rate}
	plan, err := calc.Solve(pr, targets, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return err
	}
	cs, err := cluster.Split(pr, plan, s3Reserve, nil, "iron-gear-wheel")
	if err != nil {
		return err
	}
	cs = append(cs, cluster.Labs(pr, targets, s3LabSec, s3Reserve), cluster.StorageCluster())
	if cs, err = cluster.AddPower(pr, cs, s3Reserve, nil); err != nil {
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
	base := cluster.NewBase(pr, h.World, world.TileOf(p.Position))
	base.Log = h.Log
	base.Avoid = map[world.Tile]bool{}
	// кластер, к которому ленту не провести, — в другое место (до 6 попыток)
	var list []layout.Placement
	for try := 0; ; try++ {
		base.Clusters = nil
		base.Reset()
		for _, c := range cs {
			c.Name = ""
		}
		if err := base.PlaceAll(cs); err != nil {
			return err
		}
		l, err := base.Layout()
		if err != nil {
			return err
		}
		err = base.Connect(l, nil, nil)
		if err == nil {
			if errs := l.Validate(); len(errs) > 0 {
				return fmt.Errorf("раскладка: %v", errs[0])
			}
			list = l.List
			if err := s3Draw(h, base, l, p.Position); err != nil {
				return err
			}
			break
		}
		ce, ok := err.(*cluster.ConnectError)
		if !ok || try >= 5 {
			s3Draw(h, base, l, p.Position) // что вышло — на картинку
			for _, c := range base.Sorted() {
				h.Log("кластер %v", c)
			}
			return err
		}
		h.Say("кластер %s: %v — ищу другое место", ce.Consumer.Name, ce.Err)
		a := ce.Consumer.Area
		for y := a.Min.Y; y < a.Max.Y; y++ {
			for x := a.Min.X; x < a.Max.X; x++ {
				base.Avoid[world.Tile{X: x, Y: y}] = true
			}
		}
	}
	for _, c := range base.Sorted() {
		h.Say("кластер %v", c)
	}
	data, err := json.MarshalIndent(S3{Clusters: base.Sorted(), Placed: len(list), Layout: list, Image: s3Image()}, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(s3Plan, data, 0o644)
}

// s3Image — картинка плана: run/stages/s3-<сид>.png.
func s3Image() string {
	var m struct {
		Seed int64 `json:"seed"`
	}
	if data, err := os.ReadFile("run/map.json"); err == nil {
		json.Unmarshal(data, &m)
	}
	return filepath.Join("run/stages", fmt.Sprintf("s3-%d.png", m.Seed))
}

// s3Draw — картинка: открытая карта, кластеры с коридорами и раскладка.
func s3Draw(h *act.Hands, base *cluster.Base, l *layout.Layout, at bridge.Pos) error {
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, c := range base.Clusters {
		r := c.Area.Grow(cluster.Corridor)
		minX, minY = min(minX, r.Min.X), min(minY, r.Min.Y)
		maxX, maxY = max(maxX, r.Max.X), max(maxY, r.Max.Y)
	}
	ro := world.RenderOptions{Center: world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2},
		Radius: max(maxX-minX, maxY-minY)/2 + 20, Scale: 4, Player: &at, Labels: true}
	img := h.World.Render(ro)
	base.Draw(img, ro)
	l.Draw(img, ro)
	f, err := os.Create(s3Image())
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
	for _, k := range s3Kinds {
		if have[k] == 0 {
			return fmt.Errorf("в плане нет кластера %s", k)
		}
	}
	if len(s.Clusters) != len(s3Kinds) {
		return fmt.Errorf("кластеров %d, нужно %d", len(s.Clusters), len(s3Kinds))
	}
	if st, err := os.Stat(s.Image); err != nil || st.Size() == 0 {
		return fmt.Errorf("нет картинки плана %s", s.Image)
	}
	return nil
}
