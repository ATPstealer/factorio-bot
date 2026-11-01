package stage

import (
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// План всей базы (стадия 2, plan_base.go) в состоянии карты и его картинки. План считается один
// раз и не дописывается: каждая стадия стройки берёт из него своё.

const planFile = "state-plan.json"

// Пакеты науки до ракеты (stages.md: каждый — 0.1/с).
const (
	redPack    = "automation-science-pack"
	greenPack  = "logistic-science-pack"
	bluePack   = "chemical-science-pack"
	purplePack = "production-science-pack"
	yellowPack = "utility-science-pack"
)

// ClusterPlan — план кластеров карты: кластеры с местами, размещений в раскладке, картинка.
type ClusterPlan struct {
	Clusters []*cluster.Cluster `json:"clusters"`
	Placed   int                `json:"placed"` // размещений внутри кластеров и логистики
	Layout   []layout.Placement `json:"layout"` // раскладка: по ней строят стадии
	Image    string             `json:"image"`
}

// LoadPlan — план всей базы этой карты (стадия 2).
func LoadPlan() (*ClusterPlan, error) {
	s, err := loadPlanFile(runPath(planFile))
	if err != nil {
		return nil, fmt.Errorf("нет плана всей базы (стадия 2): %w", err)
	}
	return s, nil
}

// LoadPlanFile — план кластеров из файла состояния (bin/bot flow — разбор течения по плану).
func LoadPlanFile(path string) (*ClusterPlan, error) { return loadPlanFile(path) }

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

// planImage — картинка плана: run/stages/plan-<сид>.png.
func planImage() string { return image("plan") }

// image — картинка name карты: run/stages/<name>-<сид>.png (по смыслу, без номера стадии).
func image(name string) string {
	if roughOffline { // без игры (bin/bot rough) — рядом с состоянием, не мешать прогонам
		return runPath(name + "-" + seedOf() + ".png")
	}
	return filepath.Join("run/stages", name+"-"+seedOf()+".png")
}

// planDraw — картинка в файл path: карта, зоны, кластеры с бордюрами и раскладка.
func planDraw(h *act.Hands, zones []cluster.Zone, base *cluster.Base, l *layout.Layout, path string) error {
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
	return drawPlan(h, zones, base, l, world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}, radius, path)
}

// drawAround — картинка плана вокруг кластера c (разбор неудачной логистики): радиус 60, крупно.
func drawAround(h *act.Hands, zones []cluster.Zone, base *cluster.Base, l *layout.Layout, c *cluster.Cluster, path string) error {
	x, y := c.Area.Center()
	return drawPlan(h, zones, base, l, world.Tile{X: int(x), Y: int(y)}, 60, path)
}

// drawPlan — картинка плана с центром center и радиусом radius в файл path.
func drawPlan(h *act.Hands, zones []cluster.Zone, base *cluster.Base, l *layout.Layout, center world.Tile, radius int, path string) error {
	ro := world.RenderOptions{Center: center, Radius: radius, Scale: imageScale(radius), Labels: true}
	img := h.World.Render(ro)
	cluster.DrawZones(img, ro, zones)
	base.Draw(img, ro)
	if l != nil {
		l.Draw(img, ro)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// imageSide — сторона картинки плана и зон, px: в максимальном разрешении (пользователь), но не
// больше того, что открывают просмотрщики (16384).
const imageSide = 16000

// imageScale — пикселей на тайл для картинки радиусом radius тайлов: как можно крупнее (до 12),
// сторона — не больше imageSide.
func imageScale(radius int) int { return max(3, min(12, imageSide/(2*radius))) }

// planStart — места всех своих построек (точки стадии 10): кластеры без зоны их не задевают
// (до разборки на стадии 15).
func planStart(own []world.Object) []cluster.Rect {
	var keep []cluster.Rect
	for _, o := range own {
		t := world.TileOf(o.Pos)
		keep = append(keep, cluster.Rect{Min: world.Tile{X: t.X - 1, Y: t.Y - 1}, Max: world.Tile{X: t.X + 2, Y: t.Y + 2}})
	}
	return keep
}
