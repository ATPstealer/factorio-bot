//go:build e2e

// Базовые функции стадии 7: построить раскладку, лента везёт. Прохождение игры — game_test.go.
package e2e

import (
	"os"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/exec"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// buildT — построить раскладку планировщиком (материалы, расчистка, топливо) и проверить,
// что всё стоит с нужными направлениями.
func buildT(t *testing.T, h *act.Hands, l *layout.Layout, tag string, limit time.Duration) {
	t.Helper()
	if errs := l.Validate(); len(errs) > 0 {
		t.Fatalf("раскладка с ошибками: %v", errs)
	}
	state := path("run", "state-test.json")
	os.Remove(state)
	defer os.Remove(state)
	os.Remove(path("run", "bot-test.log"))
	r := newRunner(t, h, state, []exec.GoalSpec{{Kind: "build", Item: tag, Layout: l.List}})
	defer r.Close()
	start := time.Now()
	if err := r.Run(ctxT(t, limit)); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s построено за %.0f с", tag, time.Since(start).Seconds())
	for _, pl := range l.List {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			t.Errorf("нет %s (%v)", pl, err)
			continue
		}
		if st.Direction != nil && *st.Direction != pl.Dir {
			t.Errorf("%s: направление в игре %d", pl, *st.Direction)
		}
	}
}

// Модуль плавки (2 печи, burner-инсертеры, ленты) у игрока: материалы набирает планировщик,
// место расчищается, всё стоит с нужными направлениями.
func TestBase_Build(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	lt, ok := l.FindArea(world.TileOf(p.Position), 6, 4, 1, 40, layout.OffOre)
	if !ok {
		t.Skip("нет места 6×4 рядом")
	}
	l.SmeltingColumn(lt, 2, "stone-furnace", "burner-inserter", "smelt")
	t.Logf("колонна в %v: %s", lt, l.Summary())

	buildT(t, h, l, "col", 15*time.Minute)
	clean(t, h)
}

// Лента-змейка (6 рядов по 5 тайлов, 10 изгибов): построить, положить пластину в начало —
// она доезжает до конца.
func TestBase_Belt(t *testing.T) {
	h := handsT(t)
	have(t, h, "transport-belt", 30)
	have(t, h, "iron-plate", 1)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	const rows, length = 6, 5
	lt, ok := l.FindArea(world.TileOf(p.Position), length, rows, 1, 30, layout.OffOre)
	if !ok {
		t.Skip("нет места под змейку")
	}
	// ряды по очереди на восток и на запад; в конце ряда — вниз (2 изгиба на переход)
	var tiles []world.Tile
	for r := 0; r < rows; r++ {
		for k := 0; k < length; k++ {
			x := lt.X + k
			if r%2 == 1 {
				x = lt.X + length - 1 - k
			}
			tiles = append(tiles, world.Tile{X: x, Y: lt.Y + r})
		}
	}
	bends := 0
	for i, tl := range tiles {
		d := layout.S
		if i+1 < len(tiles) {
			n := tiles[i+1]
			switch {
			case n.X > tl.X:
				d = layout.E
			case n.X < tl.X:
				d = layout.W
			}
		} else {
			d = l.List[len(l.List)-1].Dir
		}
		if i > 0 && d != l.List[len(l.List)-1].Dir {
			bends++
		}
		l.Add(layout.Placement{Name: "transport-belt", Pos: tl.Center(), Dir: d, Tag: "snake"})
	}
	last := l.List[len(l.List)-1]
	l.AllowEnd(layout.Step(world.TileOf(last.Pos), last.Dir))
	if bends < 10 {
		t.Fatalf("изгибов %d < 10", bends)
	}
	buildT(t, h, l, "snake", 10*time.Minute)

	first, err := h.B.EntityAt("transport-belt", l.List[0].Pos)
	if err != nil || first == nil {
		t.Fatalf("нет начала ленты: %v", err)
	}
	if err := h.Insert(ctxT(t, time.Minute), *first, "iron-plate", 1); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for time.Since(start) < 40*time.Second {
		if st, _ := h.B.GetEntity(last.Name, last.Pos); st != nil && st.Belt["iron-plate"] > 0 {
			t.Logf("пластина доехала за %.0f с, изгибов %d", time.Since(start).Seconds(), bends)
			clean(t, h)
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("пластина не доехала до конца ленты за 40 с")
}
