package bridge

import "testing"

// Утёс-угол: рамка повёрнута на 45° по часовой — лежит по диагонали «\».
func TestBoxOrientation(t *testing.T) {
	b := Box{LeftTop: Pos{X: -2.12, Y: -1.06}, RightBottom: Pos{X: 2.12, Y: 1.06}, Orientation: 0.125}
	if !b.Contains(Pos{X: 1, Y: 1}, 0) || !b.Contains(Pos{X: -1, Y: -1}, 0) {
		t.Error("диагональ «\\» не внутри")
	}
	if b.Contains(Pos{X: 2, Y: 0}, 0) || b.Contains(Pos{X: 1, Y: -1}, 0) {
		t.Error("вне диагонали — внутри")
	}
	if bb := b.Bounds(); bb.RightBottom.X < 2.2 || bb.RightBottom.Y < 2.2 {
		t.Errorf("охват %v", bb)
	}
}
