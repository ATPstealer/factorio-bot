package act

import (
	"reflect"
	"testing"

	"factorio-bot/internal/bridge"
)

func TestWalkKeys(t *testing.T) {
	o := bridge.Pos{}
	cases := []struct {
		to   bridge.Pos
		want []string
	}{
		{bridge.Pos{X: 5}, []string{"d"}},
		{bridge.Pos{X: -5, Y: -5}, []string{"a", "w"}},
		{bridge.Pos{X: 0.2, Y: 3}, []string{"s"}},
		{bridge.Pos{X: 0.1, Y: -0.1}, nil},
	}
	for _, c := range cases {
		got := walkKeys(o, c.to)
		want := map[string]bool{}
		for _, k := range c.want {
			want[k] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("walkKeys(%v) = %v, ждали %v", c.to, got, want)
		}
	}
}

func TestSidestepPerpendicular(t *testing.T) {
	if s := sidestep(map[string]bool{"d": true}, false); !s["w"] || len(s) != 1 {
		t.Errorf("вправо → %v", s)
	}
	if s := sidestep(map[string]bool{"s": true}, true); !s["d"] || len(s) != 1 {
		t.Errorf("вниз → %v", s)
	}
}

func TestHoverPoints(t *testing.T) {
	e := bridge.Entity{Position: bridge.Pos{X: 5, Y: 5}}
	if pts := hoverPoints(e); len(pts) != 1 || pts[0] != e.Position {
		t.Fatalf("без рамки: %v", pts)
	}
	e.SelectionBox = &bridge.Box{LeftTop: bridge.Pos{X: 4, Y: 3}, RightBottom: bridge.Pos{X: 6, Y: 5}}
	pts := hoverPoints(e)
	if pts[0] != (bridge.Pos{X: 5, Y: 4}) {
		t.Errorf("первая точка — центр рамки, а не %v", pts[0])
	}
	for i, p := range pts {
		if p.X <= 4 || p.X >= 6 || p.Y <= 3 || p.Y >= 5 {
			t.Errorf("точка %v вне рамки", p)
		}
		if i > 1 && dist(p, pts[0]) < dist(pts[i-1], pts[0]) {
			t.Errorf("точки не по удалению от центра: %v", pts)
			break
		}
	}
}
