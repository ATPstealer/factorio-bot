//go:build e2e

// Стадия 1 — мост (мод + RCON) и руки (XTEST-ввод, камера).
// Критерий: бот подходит клавишами к точке, наводит курсор на дерево (selected подтверждает)
// и срубает его ПКМ.
package e2e

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
)

var (
	brOnce sync.Once
	br     *bridge.Bridge

	handsOnce sync.Once
	hands     *act.Hands
	handsErr  error
)

func bridgeT(t *testing.T) *bridge.Bridge {
	t.Helper()
	g := game(t)
	brOnce.Do(func() { br = bridge.New(g) })
	return br
}

func handsT(t *testing.T) *act.Hands {
	t.Helper()
	b := bridgeT(t)
	handsOnce.Do(func() {
		in, err := input.Open(botDisplay)
		if err != nil {
			handsErr = err
			return
		}
		hands, handsErr = act.New(b, in)
	})
	if handsErr != nil {
		t.Fatalf("ввод: %v", handsErr)
	}
	hands.Log = t.Logf
	return hands
}

func ctxT(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestStage1_Ping(t *testing.T) {
	var info struct{ Version string }
	readJSON(t, path("mod", "botbridge", "info.json"), &info)
	p, err := bridgeT(t).Ping()
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != info.Version {
		t.Errorf("версия мода в игре %q, в info.json %q — перезапусти scripts/run.sh", p.Version, info.Version)
	}
	t1, err := bridgeT(t).Tick()
	if err != nil || t1 < p.Tick {
		t.Errorf("tick = %d (%v), ping.tick = %d", t1, err, p.Tick)
	}
}

func TestStage1_UnknownMethod(t *testing.T) {
	err := bridgeT(t).Call("no_such_method", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("err = %v", err)
	}
}

func TestStage1_Player(t *testing.T) {
	p, err := bridgeT(t).Player()
	if err != nil {
		t.Fatal(err)
	}
	if p.Zoom <= 0 || p.Scale != 1 {
		t.Errorf("zoom %v, scale %v", p.Zoom, p.Scale)
	}
	if p.Character == nil || p.Character.Reach <= 0 {
		t.Fatalf("character = %+v", p.Character)
	}
	if len(p.Quickbar) != 10 {
		t.Errorf("quickbar: %d слотов", len(p.Quickbar))
	}
	h := handsT(t)
	if h.Win.W != p.Resolution.Width || h.Win.H != p.Resolution.Height {
		t.Errorf("окно %dx%d, разрешение игры %dx%d", h.Win.W, h.Win.H, p.Resolution.Width, p.Resolution.Height)
	}
}

func TestStage1_FindNearestSorted(t *testing.T) {
	es, err := bridgeT(t).FindNearest(bridge.FindArgs{Type: "tree", Radius: 100, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(es) == 0 {
		t.Skip("нет деревьев в радиусе 100")
	}
	for i := range es {
		if es[i].SelectionBox == nil {
			t.Errorf("%s без selection_box", es[i].Name)
		}
		if i > 0 && es[i].Distance < es[i-1].Distance {
			t.Errorf("не отсортировано: %v < %v", es[i].Distance, es[i-1].Distance)
		}
	}
}

// Ходьба туда и обратно с точностью 0.5 тайла.
func TestStage1_Walk(t *testing.T) {
	h := handsT(t)
	start, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t, 30*time.Second)
	for _, target := range []bridge.Pos{
		{X: start.Position.X + 6, Y: start.Position.Y + 3},
		start.Position,
	} {
		if err := h.WalkTo(ctx, target, 0.5); err != nil {
			t.Fatal(err)
		}
		p, err := h.B.Player()
		if err != nil {
			t.Fatal(err)
		}
		d := dist(p.Position, target)
		t.Logf("цель (%.2f, %.2f), стою (%.2f, %.2f), ошибка %.2f", target.X, target.Y, p.Position.X, p.Position.Y, d)
		if d > 0.5 {
			t.Errorf("до цели %.2f > 0.5", d)
		}
		if p.Character.Walking {
			t.Error("персонаж не остановился")
		}
	}
}

// Критерий стадии: дойти до дерева, навести курсор, срубить; событие mined_entity в буфере.
func TestStage1_ChopTree(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 90*time.Second)
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	trees, err := h.B.FindNearest(bridge.FindArgs{Type: "tree", Radius: 60, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) == 0 {
		t.Skip("нет деревьев в радиусе 60")
	}
	var chopped *bridge.Entity
	for _, tr := range trees {
		if err := h.WalkTo(ctx, tr.Position, 2.5); err != nil {
			t.Fatal(err)
		}
		err := h.Mine(ctx, tr, 10*time.Second)
		if errors.Is(err, act.ErrHover) {
			t.Log(err)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		chopped = &tr
		break
	}
	if chopped == nil {
		t.Fatal("ни одно из 5 ближайших деревьев не срублено")
	}
	if e, _ := h.B.EntityAt(chopped.Name, chopped.Position); e != nil {
		t.Error("дерево всё ещё на месте")
	}
	after, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	if after.Inventory["wood"] <= before.Inventory["wood"] {
		t.Errorf("wood %d → %d", before.Inventory["wood"], after.Inventory["wood"])
	}
	evs, _, err := h.B.PollEvents(seq0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Type == "mined_entity" && e.Entity != nil && e.Entity.Name == chopped.Name &&
			dist(e.Entity.Position, chopped.Position) < 0.01 {
			found = true
		}
	}
	if !found {
		t.Errorf("нет события mined_entity для %s; события: %+v", chopped.Name, evs)
	}
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }
