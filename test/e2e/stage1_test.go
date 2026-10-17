//go:build e2e

// Стадия 1 — мост (мод + RCON) и руки (XTEST-ввод, камера).
// Критерий: бот подходит клавишами к точке, наводит курсор на дерево (selected подтверждает)
// и срубает его ПКМ.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
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
		if handsErr = in.Reset(); handsErr != nil {
			return
		}
		hands, handsErr = act.New(b, in)
		if handsErr != nil {
			return
		}
		hands.DebugDir = path("run", "debug")
		hands.Console = true
		pong, err := b.Ping()
		if err != nil {
			handsErr = err
			return
		}
		if hands.Protos, handsErr = knowledge.Fetch(b, pong.GameVersion); handsErr != nil {
			return
		}
		hands.World = world.New(hands.Protos)
		if _, handsErr = hands.World.Sync(b, true); handsErr != nil {
			return
		}
		handsErr = hands.Clean(context.Background())
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

// Добыча: дойти до дерева, навести курсор, срубить; событие mined_entity в буфере.
func TestStage1_Mine(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 2*time.Minute)
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	trees, err := h.B.FindNearest(bridge.FindArgs{Type: "tree", Radius: 300, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) == 0 {
		t.Skip("нет деревьев в радиусе 300")
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

// Честность: поиск сущностей не видит неоткрытые (не charted) чанки.
func TestStage1_OnlyCharted(t *testing.T) {
	b := bridgeT(t)
	for _, find := range []func() ([]bridge.Entity, error){
		func() ([]bridge.Entity, error) {
			return b.FindNearest(bridge.FindArgs{Type: "tree", Radius: 2000, Limit: 100000})
		},
		func() ([]bridge.Entity, error) { return b.FindMinable("any", 2000, 100000) },
	} {
		es, err := find()
		if err != nil {
			t.Fatal(err)
		}
		hidden := 0
		// результаты отсортированы по расстоянию — проверяем 300 самых дальних, они у края открытой зоны
		far := es[max(0, len(es)-300):]
		for _, e := range far {
			var charted bool
			lua(t, fmt.Sprintf("p.force.is_chunk_charted(s, {%d, %d})",
				int(math.Floor(e.Position.X/32)), int(math.Floor(e.Position.Y/32))), &charted)
			if !charted {
				hidden++
			}
		}
		if hidden > 0 {
			t.Errorf("найдено %d сущностей в неоткрытых чанках", hidden)
		}
		t.Logf("найдено %d, в неоткрытых чанках: %d", len(es), hidden)
	}
	// контроль: в сгенерированных, но не открытых чанках деревья есть — значит фильтр реально отсекает
	var hiddenTrees int
	lua(t, `(function() local n=0 for ch in s.get_chunks() do if not p.force.is_chunk_charted(s,{ch.x,ch.y}) then
		n=n+s.count_entities_filtered{area=ch.area,type="tree"} end end return n end)()`, &hiddenTrees)
	if hiddenTrees == 0 {
		t.Log("в неоткрытых чанках деревьев нет — проверка фильтра неинформативна")
	}

	// скан карты: список открытых чанков совпадает с игрой, неоткрытый чанк — пустой
	charted, err := b.ListCharted()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	lua(t, `(function() local n=0 for ch in s.get_chunks() do if p.force.is_chunk_charted(s,{ch.x,ch.y}) then n=n+1 end end return n end)()`, &n)
	if len(charted) != n || n == 0 {
		t.Errorf("открытых чанков: мод %d, игра %d", len(charted), n)
	}
	var hidden *bridge.ChunkPos
	lua(t, `(function() for ch in s.get_chunks() do if not p.force.is_chunk_charted(s,{ch.x,ch.y}) then return {x=ch.x,y=ch.y} end end end)()`, &hidden)
	if hidden == nil {
		t.Log("все сгенерированные чанки открыты")
		return
	}
	scans, err := b.ScanChunks([]bridge.ChunkPos{*hidden})
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 1 || scans[0].Charted || scans[0].Tiles != "" || len(scans[0].Entities)+len(scans[0].Resources) != 0 {
		t.Errorf("неоткрытый чанк %v отдал данные: %+v", *hidden, scans)
	}
}
