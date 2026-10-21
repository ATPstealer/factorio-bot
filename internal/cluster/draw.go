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
