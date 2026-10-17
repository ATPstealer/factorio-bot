// Package camera — проекция мир ↔ экран для окна клиента.
// Камера центрирована на игроке; 1 тайл = 32 × zoom × display_scale пикселей.
package camera

import "math"

const tilePx = 32

type Camera struct {
	CenterX, CenterY float64 // позиция игрока (тайлы)
	Zoom             float64
	Scale            float64 // display_scale
	WinX, WinY       int     // левый верхний угол окна клиента на дисплее
	W, H             int     // display_resolution
}

func (c Camera) ppt() float64 {
	s := c.Scale
	if s == 0 {
		s = 1
	}
	return tilePx * c.Zoom * s
}

// ToScreen — мировые координаты → пиксели дисплея (для Input.Move).
func (c Camera) ToScreen(x, y float64) (int, int) {
	p := c.ppt()
	sx := float64(c.WinX) + float64(c.W)/2 + (x-c.CenterX)*p
	sy := float64(c.WinY) + float64(c.H)/2 + (y-c.CenterY)*p
	return int(math.Round(sx)), int(math.Round(sy))
}

// ToWorld — пиксели дисплея → мировые координаты.
func (c Camera) ToWorld(sx, sy int) (float64, float64) {
	p := c.ppt()
	x := c.CenterX + (float64(sx-c.WinX)-float64(c.W)/2)/p
	y := c.CenterY + (float64(sy-c.WinY)-float64(c.H)/2)/p
	return x, y
}

// Visible — попадает ли точка мира в окно с отступом margin пикселей от краёв.
func (c Camera) Visible(x, y float64, margin int) bool {
	sx, sy := c.ToScreen(x, y)
	return sx >= c.WinX+margin && sx < c.WinX+c.W-margin && sy >= c.WinY+margin && sy < c.WinY+c.H-margin
}
