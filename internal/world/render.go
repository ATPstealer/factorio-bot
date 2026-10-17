package world

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"factorio-bot/internal/bridge"
)

// Палитра отладочной карты.
var (
	ColorUnknown = color.RGBA{16, 16, 16, 255}
	ColorLand    = color.RGBA{58, 58, 38, 255}
	ColorWater   = color.RGBA{31, 79, 127, 255}
	ColorShallow = color.RGBA{63, 111, 143, 255}
	ColorOutMap  = color.RGBA{0, 0, 0, 255}
	ColorGrid    = color.RGBA{70, 70, 52, 255}
	ColorTree    = color.RGBA{28, 90, 28, 255}
	ColorRock    = color.RGBA{138, 138, 138, 255}
	ColorWreck   = color.RGBA{200, 200, 200, 255}
	ColorOwn     = color.RGBA{255, 255, 255, 255}
	ColorOther   = color.RGBA{150, 120, 90, 255}
	ColorPlayer  = color.RGBA{255, 32, 32, 255}
	ColorLabel   = color.RGBA{255, 255, 200, 255}

	ResourceColors = map[string]color.RGBA{
		"iron-ore":    {104, 137, 168, 255},
		"copper-ore":  {200, 105, 58, 255},
		"coal":        {8, 8, 8, 255},
		"stone":       {176, 152, 104, 255},
		"uranium-ore": {110, 196, 58, 255},
		"crude-oil":   {162, 58, 200, 255},
	}
	colorResourceOther = color.RGBA{220, 0, 220, 255}
)

// RenderOptions — область и масштаб: квадрат со стороной 2·Radius тайлов вокруг Center.
type RenderOptions struct {
	Center Tile
	Radius int
	Scale  int         // пикселей на тайл
	Player *bridge.Pos // отметка игрока
	Labels bool        // подписи месторождений
}

// Render рисует открытую карту; неоткрытое — тёмным.
func (w *World) Render(o RenderOptions) *image.RGBA {
	if o.Scale <= 0 {
		o.Scale = 2
	}
	size := 2 * o.Radius * o.Scale
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	x0, y0 := o.Center.X-o.Radius, o.Center.Y-o.Radius

	fill := func(t Tile, c color.RGBA) {
		px, py := (t.X-x0)*o.Scale, (t.Y-y0)*o.Scale
		draw.Draw(img, image.Rect(px, py, px+o.Scale, py+o.Scale), &image.Uniform{c}, image.Point{}, draw.Src)
	}

	for y := y0; y < y0+2*o.Radius; y++ {
		for x := x0; x < x0+2*o.Radius; x++ {
			t := Tile{x, y}
			cl := ColorUnknown
			switch w.TileAt(t) {
			case Land:
				cl = ColorLand
				if o.Scale >= 3 && (floorMod(x, ChunkSize) == 0 || floorMod(y, ChunkSize) == 0) {
					cl = ColorGrid
				}
			case Water:
				cl = ColorWater
			case Shallow:
				cl = ColorShallow
			case OutMap:
				cl = ColorOutMap
			}
			if r, ok := w.ResourceAt(t); ok {
				cl = resourceColor(r.Name)
			}
			fill(t, cl)
		}
	}

	for _, ob := range w.Objects() {
		cl, ok := objectColor(ob)
		if !ok {
			continue
		}
		tiles := BoxTiles(w.CollisionBox(ob))
		if len(tiles) == 0 {
			tiles = []Tile{TileOf(ob.Pos)}
		}
		for _, t := range tiles {
			if t.X >= x0 && t.X < x0+2*o.Radius && t.Y >= y0 && t.Y < y0+2*o.Radius {
				fill(t, cl)
			}
		}
	}

	if o.Labels {
		face := basicfont.Face7x13
		for _, p := range w.Patches() {
			if p.Name == "" || len(p.Tiles) < 4 {
				continue
			}
			px := int((p.Center.X - float64(x0)) * float64(o.Scale))
			py := int((p.Center.Y - float64(y0)) * float64(o.Scale))
			label := fmt.Sprintf("%s %s", strings.TrimSuffix(p.Name, "-ore"), human(p.Amount))
			drawText(img, face, px-len(label)*7/2, py+4, label)
		}
	}

	if o.Player != nil {
		pt := TileOf(*o.Player)
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				t := Tile{pt.X + dx, pt.Y + dy}
				if t.X >= x0 && t.X < x0+2*o.Radius && t.Y >= y0 && t.Y < y0+2*o.Radius {
					fill(t, ColorPlayer)
				}
			}
		}
	}
	return img
}

func resourceColor(name string) color.RGBA {
	if c, ok := ResourceColors[name]; ok {
		return c
	}
	return colorResourceOther
}

func objectColor(o Object) (color.RGBA, bool) {
	switch {
	case o.Own:
		return ColorOwn, true
	case o.Type == "tree":
		return ColorTree, true
	case strings.HasPrefix(o.Name, "crash-site-"):
		return ColorWreck, true
	case o.Type == "simple-entity":
		return ColorRock, true
	case noCollision[o.Type]:
		return color.RGBA{}, false
	}
	return ColorOther, true
}

func floorMod(a, b int) int { return a - floorDiv(a, b)*b }

func human(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// drawText — подпись с тёмной обводкой, чтобы читалась на любом фоне.
func drawText(img *image.RGBA, face font.Face, x, y int, s string) {
	for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		(&font.Drawer{Dst: img, Src: image.NewUniform(color.Black), Face: face,
			Dot: fixed.P(x+d[0], y+d[1])}).DrawString(s)
	}
	(&font.Drawer{Dst: img, Src: image.NewUniform(ColorLabel), Face: face, Dot: fixed.P(x, y)}).DrawString(s)
}
