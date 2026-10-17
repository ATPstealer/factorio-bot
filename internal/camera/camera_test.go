package camera

import (
	"math"
	"testing"
)

var cam = Camera{CenterX: 10, CenterY: -5, Zoom: 1.5, Scale: 1, WinX: 32, WinY: 32, W: 1856, H: 1016}

func TestCenter(t *testing.T) {
	x, y := cam.ToScreen(10, -5)
	if x != 32+928 || y != 32+508 {
		t.Fatalf("центр = %d,%d", x, y)
	}
}

func TestScale(t *testing.T) {
	x, y := cam.ToScreen(11, -3) // +1 тайл по x, +2 по y при 48 px/тайл
	if x != 960+48 || y != 540+96 {
		t.Fatalf("= %d,%d", x, y)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, p := range [][2]float64{{0, 0}, {13.25, -7.5}, {-3, 4}} {
		x, y := cam.ToWorld(cam.ToScreen(p[0], p[1]))
		if math.Abs(x-p[0]) > 1.0/48 || math.Abs(y-p[1]) > 1.0/48 {
			t.Errorf("%v → %v,%v", p, x, y)
		}
	}
}

func TestVisible(t *testing.T) {
	if !cam.Visible(10, -5, 0) {
		t.Error("центр не виден")
	}
	if cam.Visible(10+20, -5, 0) { // 20 тайлов = 960 px > половины ширины
		t.Error("точка за краем видна")
	}
}
