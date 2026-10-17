package layout

import (
	"image"
	"image/color"
	"image/draw"
	"strings"

	"factorio-bot/internal/world"
)

var (
	colDrill    = color.RGBA{90, 90, 100, 255}
	colFurnace  = color.RGBA{230, 130, 40, 255}
	colAsm      = color.RGBA{70, 110, 200, 255}
	colInserter = color.RGBA{240, 220, 60, 255}
	colBelt     = color.RGBA{250, 240, 170, 255}
	colArrow    = color.RGBA{60, 50, 20, 255}
	colUG       = color.RGBA{200, 160, 60, 255}
	colPole     = color.RGBA{140, 80, 30, 255}
	colPower    = color.RGBA{170, 70, 200, 255}
	colClear    = color.RGBA{255, 0, 255, 255}
)

func placementColor(p Placement) color.RGBA {
	switch {
	case strings.Contains(p.Name, "mining-drill"):
		return colDrill
	case strings.Contains(p.Name, "furnace"):
		return colFurnace
	case strings.HasPrefix(p.Name, "assembling"):
		return colAsm
	case strings.Contains(p.Name, "inserter"):
		return colInserter
	case isBelt(p.Name):
		return colBelt
	case isUnderground(p.Name):
		return colUG
	case isPole(p.Name):
		return colPole
	}
	return colPower
}

// Draw рисует раскладку поверх карты (те же опции, что у world.Render).
func (l *Layout) Draw(img *image.RGBA, o world.RenderOptions) {
	x0, y0 := o.Center.X-o.Radius, o.Center.Y-o.Radius
	in := func(t world.Tile) bool {
		return t.X >= x0 && t.X < x0+2*o.Radius && t.Y >= y0 && t.Y < y0+2*o.Radius
	}
	fill := func(t world.Tile, c color.RGBA, inset int) {
		px, py := (t.X-x0)*o.Scale, (t.Y-y0)*o.Scale
		draw.Draw(img, image.Rect(px+inset, py+inset, px+o.Scale-inset, py+o.Scale-inset), &image.Uniform{c}, image.Point{}, draw.Src)
	}
	for t := range l.Clear {
		if in(t) && o.Scale >= 3 {
			fill(t, colClear, o.Scale/2-1)
		}
	}
	for _, p := range l.List {
		c := placementColor(p)
		for _, t := range Tiles(l.P, p) {
			if in(t) {
				fill(t, c, 0)
			}
		}
		// стрелка направления у лент: тёмная точка у «носа»
		if (isBelt(p.Name) || isUnderground(p.Name)) && o.Scale >= 4 {
			t := world.TileOf(p.Pos)
			if !in(t) {
				continue
			}
			px, py := (t.X-x0)*o.Scale, (t.Y-y0)*o.Scale
			s, h := o.Scale, o.Scale/2
			var r image.Rectangle
			switch p.Dir {
			case N:
				r = image.Rect(px+h-1, py, px+h+1, py+h)
			case S:
				r = image.Rect(px+h-1, py+h, px+h+1, py+s)
			case E:
				r = image.Rect(px+h, py+h-1, px+s, py+h+1)
			case W:
				r = image.Rect(px, py+h-1, px+h, py+h+1)
			}
			draw.Draw(img, r, &image.Uniform{colArrow}, image.Point{}, draw.Src)
		}
	}
}

// Bounds — прямоугольник всех размещений (для центра и радиуса картинки).
func (l *Layout) Bounds() (lt, rb world.Tile) {
	first := true
	for _, p := range l.List {
		for _, t := range Tiles(l.P, p) {
			if first {
				lt, rb, first = t, t, false
				continue
			}
			lt.X, lt.Y = min(lt.X, t.X), min(lt.Y, t.Y)
			rb.X, rb.Y = max(rb.X, t.X), max(rb.Y, t.Y)
		}
	}
	return
}
