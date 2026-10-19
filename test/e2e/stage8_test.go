//go:build e2e

// Базовые функции стадии 8: трубы. Прохождение игры — game_test.go.
package e2e

import (
	"testing"
	"time"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Труба от помпы через «стену» (зарезервированные тайлы) — подземной парой; вода доходит до конца.
func TestBase_Pipe(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	var route []layout.Placement
	_, err := l.WaterPump(world.TileOf(p.Position), "pipe", func(pump layout.Placement) bool {
		cs := layout.Connections(h.Protos, pump)
		if len(cs) == 0 || !l.CanPlace(pump, layout.OffOre) {
			return false
		}
		l.Add(pump)
		from := cs[0]
		d := from.Dir
		at := func(along, across int) world.Tile {
			t := from.To
			for i := 0; i < along; i++ {
				t = layout.Step(t, d)
			}
			side := (d + 4) % 16
			if across < 0 {
				side, across = (side+8)%16, -across
			}
			for i := 0; i < across; i++ {
				t = layout.Step(t, side)
			}
			return t
		}
		// стена поперёк пути: 3 тайла вдоль, 9 поперёк — только подземкой
		wall := map[world.Tile]bool{}
		for a := 5; a < 8; a++ {
			for c := -4; c <= 4; c++ {
				wall[at(a, c)] = true
			}
		}
		for w := range wall {
			l.Reserved[w] = true
		}
		goal := at(12, 0)
		res, err := l.RoutePipe(from, layout.Conn{At: layout.Step(goal, d), To: goal, Dir: (d + 8) % 16}, "pipe")
		for w := range wall {
			delete(l.Reserved, w)
		}
		if err != nil {
			return false
		}
		route = res
		return true
	})
	if err != nil {
		t.Skip(err)
	}
	if c := l.Count(); c["pipe-to-ground"] < 2 {
		t.Fatalf("без подземки: %v", c)
	}
	buildT(t, h, l, "pipe", 15*time.Minute)
	end := route[0] // RoutePipe отдаёт с конца
	start := time.Now()
	for time.Since(start) < 30*time.Second {
		if st, _ := h.B.GetEntity(end.Name, end.Pos); st != nil && st.Fluids["water"] > 0 {
			t.Logf("вода дошла до конца трубы (%d построек): %v", len(route), l.Count())
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("вода не дошла до %s за 30 с", end)
}
