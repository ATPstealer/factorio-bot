package cluster

import (
	"image"
	"image/color"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"factorio-bot/internal/world"
)

var (
	colCorridor = color.RGBA{230, 200, 40, 255}  // коридор вокруг кластера
	colReserve  = color.RGBA{60, 150, 230, 255}  // запас роста
	colBuilt    = color.RGBA{20, 80, 200, 255}   // занято сейчас
	colMine     = color.RGBA{230, 90, 60, 255}   // добыча
	colLabel    = color.RGBA{255, 255, 255, 255} // имена
)

// Draw — кластеры поверх карты (опции world.Render): Area — одним цветом (начало ряда
// темнее), коридор — другим, имена.
func (b *Base) Draw(img *image.RGBA, o world.RenderOptions) {
	x0, y0 := o.Center.X-o.Radius, o.Center.Y-o.Radius
	blend := func(x, y int, c color.RGBA, a float64) {
		px, py := (x-x0)*o.Scale, (y-y0)*o.Scale
		for dy := 0; dy < o.Scale; dy++ {
			for dx := 0; dx < o.Scale; dx++ {
				p := image.Point{X: px + dx, Y: py + dy}
				if !p.In(img.Rect) {
					continue
				}
				q := img.RGBAAt(p.X, p.Y)
				img.SetRGBA(p.X, p.Y, color.RGBA{
					uint8(float64(q.R)*(1-a) + float64(c.R)*a),
					uint8(float64(q.G)*(1-a) + float64(c.G)*a),
					uint8(float64(q.B)*(1-a) + float64(c.B)*a), 255})
			}
		}
	}
	fill := func(r Rect, c color.RGBA, a float64, skip *Rect) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if skip != nil && x >= skip.Min.X && x < skip.Max.X && y >= skip.Min.Y && y < skip.Max.Y {
					continue
				}
				blend(x, y, c, a)
			}
		}
	}
	for _, c := range b.Clusters {
		area := c.Area
		fill(area.Grow(Corridor), colCorridor, 0.35, &area)
		reserve := colReserve
		if c.Kind == Mine {
			reserve = colMine
		}
		fill(area, reserve, 0.45, nil)
		fill(c.Rect, colBuilt, 0.6, nil)
	}
	face := basicfont.Face7x13
	for _, c := range b.Clusters {
		x, y := c.Area.Center()
		px := int((x-float64(x0))*float64(o.Scale)) - len(c.Name)*7/2
		py := int((y-float64(y0))*float64(o.Scale)) + 4
		d := &font.Drawer{Dst: img, Src: image.NewUniform(colLabel), Face: face, Dot: fixed.P(px, py)}
		d.DrawString(c.Name)
	}
}

// zoneColors — цвет зоны по виду.
var zoneColors = map[ZoneKind]color.RGBA{
	ZoneBus:        {60, 120, 230, 255},
	ZoneMine:       {150, 90, 40, 255},
	ZoneProcessing: {220, 120, 40, 255},
	ZoneSupply:     {60, 180, 90, 255},
	ZoneFinal:      {170, 70, 200, 255},
	ZoneLab:        {70, 170, 200, 255},
	ZonePower:      {230, 220, 60, 255},
}

// DrawZones — зоны поверх карты (опции world.Render): прямоугольник цветом вида, рамка, имя.
func DrawZones(img *image.RGBA, o world.RenderOptions, zones []Zone) {
	x0, y0 := o.Center.X-o.Radius, o.Center.Y-o.Radius
	for _, z := range zones {
		c := zoneColors[z.Kind]
		for y := z.Rect.Min.Y; y < z.Rect.Max.Y; y++ {
			for x := z.Rect.Min.X; x < z.Rect.Max.X; x++ {
				a := 0.35
				if x == z.Rect.Min.X || y == z.Rect.Min.Y || x == z.Rect.Max.X-1 || y == z.Rect.Max.Y-1 {
					a = 0.9
				}
				for dy := 0; dy < o.Scale; dy++ {
					for dx := 0; dx < o.Scale; dx++ {
						p := image.Point{X: (x-x0)*o.Scale + dx, Y: (y-y0)*o.Scale + dy}
						if !p.In(img.Rect) {
							continue
						}
						q := img.RGBAAt(p.X, p.Y)
						img.SetRGBA(p.X, p.Y, color.RGBA{uint8(float64(q.R)*(1-a) + float64(c.R)*a),
							uint8(float64(q.G)*(1-a) + float64(c.G)*a), uint8(float64(q.B)*(1-a) + float64(c.B)*a), 255})
					}
				}
			}
		}
	}
	face := basicfont.Face7x13
	for _, z := range zones {
		px := (z.Rect.Min.X-x0)*o.Scale + 3
		py := (z.Rect.Min.Y-y0)*o.Scale + 13
		d := &font.Drawer{Dst: img, Src: image.NewUniform(colLabel), Face: face, Dot: fixed.P(px, py)}
		d.DrawString(z.Name)
	}
}

// DrawBlocked — где этажам нельзя (разбор: база не встаёт): зоны месторождений — красным,
// неровности толще maxGap (в режиме с утёсами — только вода) — фиолетовым.
func (b *Base) DrawBlocked(img *image.RGBA, o world.RenderOptions) {
	g := b.grid()
	thick := g.thick
	if b.CliffZones {
		thick = g.thickWet
	}
	x0, y0 := o.Center.X-o.Radius, o.Center.Y-o.Radius
	for y := y0; y < o.Center.Y+o.Radius; y++ {
		for x := x0; x < o.Center.X+o.Radius; x++ {
			r := Rect{world.Tile{X: x, Y: y}, world.Tile{X: x + 1, Y: y + 1}}
			var c color.RGBA
			if n, _ := g.count(g.zone, r); n > 0 {
				c = color.RGBA{220, 40, 40, 255}
			} else if n, _ := g.count(thick, r); n > 0 {
				c = color.RGBA{170, 60, 220, 255}
			} else {
				continue
			}
			for dy := 0; dy < o.Scale; dy++ {
				for dx := 0; dx < o.Scale; dx++ {
					p := image.Point{X: (x-x0)*o.Scale + dx, Y: (y-y0)*o.Scale + dy}
					if !p.In(img.Rect) {
						continue
					}
					q := img.RGBAAt(p.X, p.Y)
					img.SetRGBA(p.X, p.Y, color.RGBA{uint8((int(q.R) + int(c.R)) / 2), uint8((int(q.G) + int(c.G)) / 2), uint8((int(q.B) + int(c.B)) / 2), 255})
				}
			}
		}
	}
}
