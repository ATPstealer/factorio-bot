//go:build e2e

// Руки: добыча, крафт, постройка, подбор.
package e2e

import (
	"context"
	"errors"
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
	"fmt"
	"math"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCraft(t *testing.T) {
	h := handsT(t)
	have(t, h, "wood", 2)
	p0, _ := h.B.Player()
	if err := h.CraftWait(ctxT(t, 30*time.Second), "wooden-chest", 1); err != nil {
		t.Fatal(err)
	}
	p1, _ := h.B.Player()
	if d := p1.Inventory["wooden-chest"] - p0.Inventory["wooden-chest"]; d != 1 {
		t.Errorf("wooden-chest +%d", d)
	}
	if d := p0.Inventory["wood"] - p1.Inventory["wood"]; d != 2 {
		t.Errorf("wood −%d, ждали −2", d)
	}
	clean(t, h)
}

// Крафт за очередью с полуфабрикатами (стадия 2 на 859088572: «крафт stone-furnace не
// закончился (есть 0, ждали 1)»): в очереди буры, печи для них — полуфабрикатами; печь,
// поставленная в очередь после них, должна оказаться в инвентаре.
func TestCraftBehindQueue(t *testing.T) {
	h := handsT(t)
	have(t, h, "iron-plate", 18)
	have(t, h, "stone", 15)
	ctx := ctxT(t, 2*time.Minute)
	p0, _ := h.B.Player()
	if err := h.Craft(ctx, "burner-mining-drill", 2); err != nil {
		t.Fatal(err)
	}
	if err := h.Craft(ctx, "stone-furnace", 1); err != nil {
		t.Fatal(err)
	}
	var p1 bridge.Player
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if p1, _ = h.B.Player(); len(p1.CraftingQueue) == 0 {
			break
		}
	}
	if len(p1.CraftingQueue) > 0 {
		t.Fatalf("очередь не закончилась: %+v", p1.CraftingQueue)
	}
	f0 := p0.Inventory["stone-furnace"]
	want := f0 - min(f0, 2) + 1 // печи из инвентаря уходят в буры, своя — +1
	if got := p1.Inventory["stone-furnace"]; got != want {
		t.Errorf("stone-furnace: %d, ждали %d (было %d)", got, want, f0)
	}
	if d := p1.Inventory["burner-mining-drill"] - p0.Inventory["burner-mining-drill"]; d != 2 {
		t.Errorf("burner-mining-drill +%d, ждали +2", d)
	}
}

// Сценарий: добыть камень → скрафтить печь → поставить → уголь и руда → забрать пластины.
func TestFurnaceScenario(t *testing.T) {
	h := handsT(t)
	have(t, h, "stone", 5)
	have(t, h, "coal", 3)
	have(t, h, "iron-ore", 5)
	ctx := ctxT(t, 3*time.Minute)

	p0, _ := h.B.Player()
	if err := h.CraftWait(ctx, "stone-furnace", 1); err != nil {
		t.Fatal(err)
	}
	spot := freeArea(t, h, 2)
	f, err := h.Place(ctx, "stone-furnace", spot, act.North)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Insert(ctx, f, "coal", 2); err != nil {
		t.Fatal(err)
	}
	if err := h.Insert(ctx, f, "iron-ore", 5); err != nil {
		t.Fatal(err)
	}
	// 5 × 3.2 с плавки при скорости 1
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := h.B.GetEntity(f.Name, f.Position)
		if err != nil {
			t.Fatal(err)
		}
		if st.Inventories["crafter_output"]["iron-plate"] >= 5 {
			break
		}
		time.Sleep(time.Second)
	}
	got, err := h.Take(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got["iron-plate"] < 5 {
		t.Errorf("забрано %v, ждали iron-plate ≥5", got)
	}
	p1, _ := h.B.Player()
	if d := p1.Inventory["iron-plate"] - p0.Inventory["iron-plate"]; d < 5 {
		t.Errorf("iron-plate +%d", d)
	}
	t.Logf("печь в %v, забрано %v", spot, got)
	clean(t, h)
}

// Поставить рядом с собой: сундук — на клетку, где стоит персонаж (отойти и поставить),
// печь 2×2 — вплотную к нему.
func TestPlaceNextToSelf(t *testing.T) {
	h := handsT(t)
	have(t, h, "wooden-chest", 1)
	have(t, h, "stone-furnace", 1)
	c := freeArea(t, h, 6)
	ctx := ctxT(t, 2*time.Minute)
	if err := h.WalkTo(ctx, c, 0.5); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	here := world.TileOf(p.Position).Center()
	chest, err := h.Place(ctx, "wooden-chest", here, act.North)
	if errors.Is(err, act.ErrOccupied) {
		t.Skipf("сундук под собой: %v", err)
	}
	if err != nil {
		t.Fatalf("сундук под собой: %v", err)
	}
	p, _ = h.B.Player()
	next := bridge.Pos{X: math.Floor(p.Position.X) + 2, Y: math.Floor(p.Position.Y)} // центр 2×2 — на стыке тайлов
	furnace, err := h.Place(ctx, "stone-furnace", next, act.North)
	if errors.Is(err, act.ErrOccupied) {
		t.Skipf("печь рядом: %v", err) // своё не трогаем; как проверять нужное — решим позже
	}
	if err != nil {
		t.Fatalf("печь рядом: %v", err)
	}
	for _, e := range []bridge.Entity{chest, furnace} {
		if got, err := h.B.EntityAt(e.Name, e.Position); err != nil || got == nil {
			t.Errorf("%s не стоит (%v)", e.Name, err)
		}
	}
	clean(t, h)
}

// Поставить среди лент: в квадрате 12×12 ленты на случайной половине клеток со случайными
// направлениями (место печи 2×2 в середине свободно); персонаж выходит из зоны, потом —
// команда поставить печь. Зерно — в логе (SEED_BELTS=… — повторить).
func TestPlaceAmongBelts(t *testing.T) {
	h := handsT(t)
	have(t, h, "transport-belt", 72)
	have(t, h, "stone-furnace", 1)
	c := freeArea(t, h, 16)
	ctx := ctxT(t, 6*time.Minute)
	seed := time.Now().UnixNano()
	if v := os.Getenv("SEED_BELTS"); v != "" {
		seed, _ = strconv.ParseInt(v, 10, 64)
	}
	t.Logf("SEED_BELTS=%d", seed)
	rnd := rand.New(rand.NewSource(seed))
	x0, y0 := math.Floor(c.X)-6, math.Floor(c.Y)-6
	// печь — в середине (центр на стыке тайлов 5–6 × 5–6), её клетки свободны
	target := bridge.Pos{X: x0 + 6, Y: y0 + 6}
	keep := func(col, row int) bool { return (col == 5 || col == 6) && (row == 5 || row == 6) }
	var cells [][2]int
	for row := 0; row < 12; row++ {
		for col := 0; col < 12; col++ {
			if !keep(col, row) {
				cells = append(cells, [2]int{col, row})
			}
		}
	}
	rnd.Shuffle(len(cells), func(i, j int) { cells[i], cells[j] = cells[j], cells[i] })
	byDir := map[int][]bridge.Pos{}
	dirs := []int{act.North, act.East, act.South, act.West}
	for _, cl := range cells[:72] {
		d := dirs[rnd.Intn(4)]
		byDir[d] = append(byDir[d], bridge.Pos{X: x0 + float64(cl[0]) + 0.5, Y: y0 + float64(cl[1]) + 0.5})
	}
	for _, d := range dirs {
		if len(byDir[d]) == 0 {
			continue
		}
		if _, err := h.PlaceAll(ctx, "transport-belt", byDir[d], d); err != nil {
			t.Fatalf("ленты: %v", err)
		}
	}
	// выйти из зоны
	if err := h.WalkTo(ctx, bridge.Pos{X: x0 - 5, Y: y0 + 6}, 1); err != nil {
		t.Fatalf("выйти из зоны: %v", err)
	}
	f, err := h.Place(ctx, "stone-furnace", target, act.North)
	if err != nil {
		t.Fatalf("печь среди лент: %v", err)
	}
	if got, err := h.B.EntityAt(f.Name, f.Position); err != nil || got == nil {
		t.Errorf("печь не стоит (%v)", err)
	}
	clean(t, h)
}

// Помпа на кромке воды и труба на её выходе (стадия 4: на картах, где помпа смотрит на запад
// или юг, труба не ставилась — «место занято помпой»). Помпа — по раскладке (layout.WaterPump)
// у ближайшей воды, лучше на запад или юг.
func TestPumpPipe(t *testing.T) {
	h := handsT(t)
	have(t, h, "offshore-pump", 1)
	have(t, h, "pipe", 1)
	p, _ := h.B.Player()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	var pump layout.Placement
	var pipe bridge.Pos
	for _, dirs := range [][]int{{layout.W, layout.S}, {layout.N, layout.E, layout.W, layout.S}} {
		l := layout.New(h.Protos, h.World)
		pl, err := l.WaterPump(world.TileOf(p.Position), "test", func(pm layout.Placement) bool {
			cs := layout.Connections(h.Protos, pm)
			if !slices.Contains(dirs, pm.Dir) || len(cs) == 0 || !l.CanPlace(pm, layout.AnyLand) {
				return false
			}
			return l.CanPlace(layout.Placement{Name: "pipe", Pos: cs[0].To.Center()}, layout.AnyLand)
		})
		if err == nil {
			pump, pipe = pl, layout.Connections(h.Protos, pl)[0].To.Center()
			break
		}
	}
	if pump.Name == "" {
		t.Skip("нет кромки воды под помпу на открытой карте")
	}
	t.Logf("помпа %v, труба (%.1f, %.1f)", pump, pipe.X, pipe.Y)
	ctx := ctxT(t, 4*time.Minute)
	if _, err := h.Place(ctx, "offshore-pump", pump.Pos, pump.Dir); err != nil {
		t.Fatalf("помпа: %v", err)
	}
	if _, err := h.Place(ctx, "pipe", pipe, act.North); err != nil {
		t.Fatalf("труба на выходе помпы: %v", err)
	}
	st, err := h.B.GetEntity("offshore-pump", pump.Pos)
	if err != nil || st == nil {
		t.Fatalf("помпа не стоит (%v)", err)
	}
	clean(t, h)
}

// Инсертер целиком под кроной дерева — поставить и задать фильтр (стадия 10: курсор выделял
// крону, «не удалось навести курсор: inserter»). Место — свободный тайл внутри рамки
// выделения ближайшего подходящего дерева, вне его рамки коллизии.
func TestFilterUnderCrown(t *testing.T) {
	h := handsT(t)
	have(t, h, "inserter", 1)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	trees := h.World.ObjectsNear(p.Position, 60)
	sort.Slice(trees, func(i, j int) bool {
		return math.Hypot(trees[i].Pos.X-p.Position.X, trees[i].Pos.Y-p.Position.Y) < math.Hypot(trees[j].Pos.X-p.Position.X, trees[j].Pos.Y-p.Position.Y)
	})
	var at bridge.Pos
	found := false
	for _, o := range trees {
		if o.Type != "tree" || found {
			continue
		}
		sb, cb := h.World.SelectionBox(o), h.World.CollisionBox(o)
		for y := math.Floor(sb.LeftTop.Y); y+1 <= sb.RightBottom.Y && !found; y++ {
			for x := math.Floor(sb.LeftTop.X); x+1 <= sb.RightBottom.X; x++ {
				if x < sb.LeftTop.X || y < sb.LeftTop.Y {
					continue
				}
				// тайл целиком в кроне и не задевает ствол
				if x+1 > cb.LeftTop.X && x < cb.RightBottom.X && y+1 > cb.LeftTop.Y && y < cb.RightBottom.Y {
					continue
				}
				tl := world.TileOf(bridge.Pos{X: x + 0.5, Y: y + 0.5})
				if ok, clear := h.World.Buildable(tl); !ok || clear || !l.Free(tl, layout.AnyLand) {
					continue
				}
				at, found = tl.Center(), true
				break
			}
		}
	}
	if !found {
		t.Skip("нет дерева с кроной над свободным тайлом рядом")
	}
	ctx := ctxT(t, 3*time.Minute)
	e, err := h.Place(ctx, "inserter", at, act.North)
	if err != nil {
		t.Fatalf("инсертер под кроной (%.1f, %.1f): %v", at.X, at.Y, err)
	}
	if err := h.SetFilter(ctx, e, "coal"); err != nil {
		t.Fatalf("фильтр инсертера под кроной: %v", err)
	}
	clean(t, h)
}

// Дерево, чья рамка выделения под кроной соседнего, — срубить (стадия 10: dead-tree-desert
// под tree-08-brown — курсор перескакивал на соседа, «не добыл за 20s»).
func TestMineUnderCrown(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	objs := h.World.ObjectsNear(p.Position, 80)
	var target world.Object
	bd := math.Inf(1)
	for _, a := range objs {
		if a.Type != "tree" {
			continue
		}
		sa := h.World.SelectionBox(a)
		ca := bridge.Pos{X: (sa.LeftTop.X + sa.RightBottom.X) / 2, Y: (sa.LeftTop.Y + sa.RightBottom.Y) / 2}
		for _, b := range objs {
			if b.Type != "tree" || b.Pos == a.Pos {
				continue
			}
			sb := h.World.SelectionBox(b)
			if ca.X > sb.LeftTop.X && ca.X < sb.RightBottom.X && ca.Y > sb.LeftTop.Y && ca.Y < sb.RightBottom.Y {
				if d := math.Hypot(a.Pos.X-p.Position.X, a.Pos.Y-p.Position.Y); d < bd {
					target, bd = a, d
				}
			}
		}
	}
	if target.Name == "" {
		t.Skip("нет дерева под кроной соседнего рядом")
	}
	t.Logf("дерево %s (%.1f, %.1f)", target.Name, target.Pos.X, target.Pos.Y)
	ctx := ctxT(t, 3*time.Minute)
	e, err := h.B.EntityAt(target.Name, target.Pos)
	if err != nil || e == nil {
		t.Fatalf("дерева нет: %v", err)
	}
	if err := h.WalkToMine(ctx, *e); err != nil && !errors.Is(err, act.ErrNotReached) {
		t.Fatal(err)
	}
	if err := h.Mine(ctx, *e, 20*time.Second); err != nil {
		t.Fatalf("срубить %s под кроной: %v", target.Name, err)
	}
	if got, _ := h.B.EntityAt(target.Name, target.Pos); got != nil {
		t.Errorf("%s стоит", target.Name)
	}
}

// Линия столбов вниз с шагом 7 (стадия 5: столбы по коридору ровной линией — шестой не
// встал, курсор с призраком столба ушёл в сторону от места). Каждый — Place по очереди.
func TestPoleLine(t *testing.T) {
	h := handsT(t)
	const n, step = 7, 7
	have(t, h, "small-electric-pole", n)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	free := func(x, y0 int) bool {
		for y := y0; y <= y0+(n-1)*step; y++ {
			for dx := -1; dx <= 1; dx++ {
				if tl := (world.Tile{X: x + dx, Y: y}); !l.Free(tl, layout.AnyLand) {
					return false
				}
				if ok, clear := h.World.Buildable(world.Tile{X: x + dx, Y: y}); !ok || clear {
					return false
				}
			}
		}
		return true
	}
	pt := world.TileOf(p.Position)
	x, y0, found := 0, 0, false
	for r := 3; r < 60 && !found; r++ {
		for _, c := range [][2]int{{pt.X + r, pt.Y - 3}, {pt.X - r, pt.Y - 3}, {pt.X + r, pt.Y - 3 - (n-1)*step}, {pt.X - r, pt.Y - 3 - (n-1)*step}} {
			if free(c[0], c[1]) {
				x, y0, found = c[0], c[1], true
				break
			}
		}
	}
	if !found {
		t.Skip("нет свободной полосы под линию столбов")
	}
	ctx := ctxT(t, 5*time.Minute)
	for k := 0; k < n; k++ {
		pos := world.Tile{X: x, Y: y0 + k*step}.Center()
		if _, err := h.Place(ctx, "small-electric-pole", pos, act.North); err != nil {
			t.Fatalf("столб %d в (%.1f, %.1f): %v", k+1, pos.X, pos.Y, err)
		}
	}
	for k := 0; k < n; k++ {
		pos := world.Tile{X: x, Y: y0 + k*step}.Center()
		if e, _ := h.B.EntityAt("small-electric-pole", pos); e == nil {
			t.Errorf("столба %d в (%.1f, %.1f) нет", k+1, pos.X, pos.Y)
		}
	}
	clean(t, h)
}

// Угольные пары (стадия 2, 794457638): две пары буров выходами друг в друга, каждому по 5
// угля; поработали — забрать топливо у всех, затем каждому по 5 назад подряд. В игре
// третьему легло 4 из 5 («положено coal ×4 из 5 (полон?)»), угля у игрока было много.
func TestRefuelCoalPairs(t *testing.T) {
	h := handsT(t)
	have(t, h, "burner-mining-drill", 4)
	have(t, h, "coal", 60)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	var patch []world.Tile
	bd := math.Inf(1)
	for _, pt := range h.World.Patches() {
		if d := math.Hypot(pt.Center.X-p.Position.X, pt.Center.Y-p.Position.Y); pt.Name == "coal" && len(pt.Tiles) >= 60 && d < bd {
			patch, bd = pt.Tiles, d
		}
	}
	if patch == nil {
		t.Skip("нет месторождения угля на открытой карте")
	}
	l := layout.New(h.Protos, h.World)
	for i := 0; i < 2; i++ {
		if err := l.CoalPair(patch, "coal", "burner-mining-drill", world.TileOf(p.Position), "test"); err != nil {
			t.Skipf("пара %d: %v", i+1, err)
		}
	}
	ctx := ctxT(t, 5*time.Minute)
	var ds []bridge.Entity
	for _, pl := range l.List {
		e, err := h.Place(ctx, pl.Name, pl.Pos, pl.Dir)
		if err != nil {
			t.Fatalf("бур в (%.1f, %.1f): %v", pl.Pos.X, pl.Pos.Y, err)
		}
		if err := h.Insert(ctx, e, "coal", 5); err != nil {
			t.Fatalf("первая заправка (%.1f, %.1f): %v", pl.Pos.X, pl.Pos.Y, err)
		}
		ds = append(ds, e)
	}
	time.Sleep(15 * time.Second) // поработали: выход каждого — в топливо соседа
	for _, e := range ds {
		if _, err := h.Take(ctx, e); err != nil {
			t.Fatalf("забрать из (%.1f, %.1f): %v", e.Position.X, e.Position.Y, err)
		}
	}
	for i, e := range ds {
		if err := h.Insert(ctx, e, "coal", 5); err != nil {
			t.Errorf("бур %d (%.1f, %.1f): %v", i+1, e.Position.X, e.Position.Y, err)
		}
	}
	clean(t, h)
}

// Пять угольных пар вплотную, угля у игрока много (стадия 2 на 794457638, 22.10: пятой/шестой
// закладке легло 4 из 5 при 300+ угля; три игры и три бота шли разом): поставить пары, каждому
// по 5, поработали — забрать топливо у всех, затем каждому по 5 подряд.
func TestRefuelFiveCoalPairs(t *testing.T) {
	h := handsT(t)
	have(t, h, "burner-mining-drill", 10)
	have(t, h, "coal", 200)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	var patch []world.Tile
	bd := math.Inf(1)
	for _, pt := range h.World.Patches() {
		if d := math.Hypot(pt.Center.X-p.Position.X, pt.Center.Y-p.Position.Y); pt.Name == "coal" && len(pt.Tiles) >= 100 && d < bd {
			patch, bd = pt.Tiles, d
		}
	}
	if patch == nil {
		t.Skip("нет месторождения угля на открытой карте")
	}
	l := layout.New(h.Protos, h.World)
	for i := 0; i < 5; i++ {
		if err := l.CoalPair(patch, "coal", "burner-mining-drill", world.TileOf(p.Position), "test"); err != nil {
			t.Skipf("пара %d: %v", i+1, err)
		}
	}
	ctx := ctxT(t, 8*time.Minute)
	var ds []bridge.Entity
	for _, pl := range l.List {
		e, err := h.Place(ctx, pl.Name, pl.Pos, pl.Dir)
		if err != nil {
			t.Fatalf("бур в (%.1f, %.1f): %v", pl.Pos.X, pl.Pos.Y, err)
		}
		if err := h.Insert(ctx, e, "coal", 5); err != nil {
			t.Fatalf("первая заправка (%.1f, %.1f): %v", pl.Pos.X, pl.Pos.Y, err)
		}
		ds = append(ds, e)
	}
	time.Sleep(15 * time.Second)
	for _, e := range ds {
		if _, err := h.Take(ctx, e); err != nil {
			t.Fatalf("забрать из (%.1f, %.1f): %v", e.Position.X, e.Position.Y, err)
		}
	}
	for i, e := range ds {
		if err := h.Insert(ctx, e, "coal", 5); err != nil {
			t.Errorf("бур %d (%.1f, %.1f): %v", i+1, e.Position.X, e.Position.Y, err)
		}
	}
	clean(t, h)
}

// Трасса лент вплотную одним вызовом (стадия 13: каждую ленту брали заново, «взятая смотрит
// на север» — вставали не так, потом R по наведению поворачивала соседнюю: «не повернулся в
// 4»): змейка из 20 лент с поворотами — все встали своим направлением, поворачивать после
// пришлось не больше одной (первую, пока рука не сверена); потом средняя лента ряда — на
// север и обратно на восток, соседние не тронуты.
// Ряд лент, часть мест которого уже занята своими такими же лентами (стадия 17 на 46082668:
// сбитый клик — персонажа несла лента — поставил ленту не на то место, следующее место
// оказалось уже со своей лентой; клик по ней ничего не строит — «не удалось поставить»).
func TestPlaceOverOwnBelt(t *testing.T) {
	h := handsT(t)
	have(t, h, "transport-belt", 8)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X - 25, Y: p0.Position.Y + 25}, 10, 4, 60)
	if !ok {
		t.Skip("нет свободной площадки 10×4")
	}
	x0, y0 := math.Floor(c.X)-2.5, math.Floor(c.Y)+0.5
	var pos []bridge.Pos
	var dirs []int
	for k := 0; k < 6; k++ {
		pos = append(pos, bridge.Pos{X: x0 + float64(k), Y: y0})
		dirs = append(dirs, layout.E)
	}
	ctx := ctxT(t, 3*time.Minute)
	if _, err := h.PlaceDirs(ctx, "transport-belt", []bridge.Pos{pos[2], pos[3]}, []int{layout.E, layout.E}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PlaceDirs(ctx, "transport-belt", pos, dirs); err != nil {
		t.Fatal(err)
	}
	for i, p := range pos {
		if st, err := h.B.GetEntity("transport-belt", p); err != nil || st == nil || st.Direction == nil || *st.Direction != layout.E {
			t.Errorf("лента %d в (%.1f, %.1f): %+v (%v)", i+1, p.X, p.Y, st, err)
		}
	}
	clean(t, h)
}

// Единственный котёл встал не тем поворотом — переставить (стадия 29 на 427965634: котёл
// поднят, взят заново — рука смотрит не туда, куда смотрел поднятый; перестановка повернула
// её мимо — «Steam engine is in the way», стадия упала). Котлов в инвентаре больше одного —
// пропуск (ситуация — последний предмет).
func TestReplaceLastBoiler(t *testing.T) {
	h := handsT(t)
	have(t, h, "boiler", 1)
	if p, _ := h.B.Player(); p.Inventory["boiler"] != 1 {
		t.Skipf("котлов в инвентаре %d, нужен ровно один", p.Inventory["boiler"])
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X + 25, Y: p0.Position.Y + 25}, 8, 8, 60)
	if !ok {
		t.Skip("нет свободной площадки 8×8")
	}
	pos := bridge.Pos{X: math.Floor(c.X) + 0.5, Y: math.Floor(c.Y)}
	ctx := ctxT(t, 3*time.Minute)
	if _, err := h.PlaceDirs(ctx, "boiler", []bridge.Pos{pos}, []int{layout.E}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PlaceDirs(ctx, "boiler", []bridge.Pos{pos}, []int{layout.N}); err != nil {
		t.Fatal(err)
	}
	if st, err := h.B.GetEntity("boiler", pos); err != nil || st == nil || st.Direction == nil || *st.Direction != layout.N {
		t.Errorf("котёл в (%.1f, %.1f): %+v (%v)", pos.X, pos.Y, st, err)
	}
	clean(t, h)
}

func TestPlaceBeltRun(t *testing.T) {
	h := handsT(t)
	have(t, h, "transport-belt", 20)
	// площадка — в стороне (тесты не убирают за собой: ленты у персонажа мешали TestWalk)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X + 25, Y: p0.Position.Y - 25}, 12, 12, 60)
	if !ok {
		t.Skip("нет свободной площадки 12×12")
	}
	x0, y0 := math.Floor(c.X)-2.5, math.Floor(c.Y)-2.5
	var pos []bridge.Pos
	var dirs []int
	for row := 0; row < 4; row++ {
		for k := 0; k < 5; k++ {
			x := x0 + float64(k)
			d := layout.E
			if row%2 == 1 {
				x, d = x0+float64(4-k), layout.W
			}
			if k == 4 {
				d = layout.S // конец ряда — вниз, к следующему
			}
			pos = append(pos, bridge.Pos{X: x, Y: y0 + float64(row)})
			dirs = append(dirs, d)
		}
	}
	rotations := 0
	log := h.Log
	h.Log = func(format string, args ...any) {
		if msg := fmt.Sprintf(format, args...); strings.HasPrefix(msg, "поворачиваю") {
			rotations++
		}
		log(format, args...)
	}
	defer func() { h.Log = log }()
	ctx := ctxT(t, 4*time.Minute)
	if _, err := h.PlaceDirs(ctx, "transport-belt", pos, dirs); err != nil {
		t.Fatal(err)
	}
	for i, p := range pos {
		st, err := h.B.GetEntity("transport-belt", p)
		if err != nil || st == nil {
			t.Fatalf("лента %d в (%.1f, %.1f) не стоит (%v)", i+1, p.X, p.Y, err)
		}
		if st.Direction == nil || *st.Direction != dirs[i] {
			t.Errorf("лента %d в (%.1f, %.1f): направление %v, ждали %d", i+1, p.X, p.Y, st.Direction, dirs[i])
		}
	}
	if rotations > 1 {
		t.Errorf("после постановки поворачивали %d лент (рука не помнит поворот)", rotations)
	}
	mid, err := h.B.EntityAt("transport-belt", pos[2])
	if err != nil || mid == nil {
		t.Fatalf("средней ленты нет (%v)", err)
	}
	for _, d := range []int{layout.N, layout.E} {
		if err := h.Rotate(ctx, *mid, d); err != nil {
			t.Fatalf("поворот средней в %d: %v", d, err)
		}
	}
	for _, i := range []int{1, 3} {
		if st, _ := h.B.GetEntity("transport-belt", pos[i]); st == nil || st.Direction == nil || *st.Direction != dirs[i] {
			t.Errorf("соседняя лента %d повёрнута: %+v", i+1, st)
		}
	}
	t.Logf("поворачивали после постановки: %d", rotations)
	clean(t, h)
}

// Подземки после ленты другим поворотом (стадия 13 на 794457638: «underground-belt не встал
// направлением 0» — рука помнила поворот прошлой ленты, а перестановка считала, что взятый
// предмет смотрит на север): лента на восток, затем две пары подземок на север (вход, через
// 2 тайла выход) — все встали своим направлением и спарились.
func TestPlaceUndergroundAfterTurn(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "underground-belt") {
		t.Skip("подземки не открыты (logistics)")
	}
	have(t, h, "underground-belt", 4) // до ленты: крафт подземок ест ленты
	have(t, h, "transport-belt", 1)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X - 25, Y: p0.Position.Y + 25}, 10, 10, 60)
	if !ok {
		t.Skip("нет свободной площадки 10×10")
	}
	x, y := math.Floor(c.X)+0.5, math.Floor(c.Y)+0.5
	ctx := ctxT(t, 4*time.Minute)
	if _, err := h.Place(ctx, "transport-belt", bridge.Pos{X: x - 3, Y: y}, layout.E); err != nil {
		t.Fatal(err)
	}
	pos := []bridge.Pos{{X: x, Y: y + 2}, {X: x, Y: y - 1}, {X: x + 2, Y: y + 2}, {X: x + 2, Y: y - 1}}
	dirs := []int{layout.N, layout.N, layout.N, layout.N}
	if _, err := h.PlaceDirs(ctx, "underground-belt", pos, dirs); err != nil {
		t.Fatal(err)
	}
	for i, p := range pos {
		st, err := h.B.GetEntity("underground-belt", p)
		if err != nil || st == nil {
			t.Fatalf("подземка %d в (%.1f, %.1f) не стоит (%v)", i+1, p.X, p.Y, err)
		}
		if st.Direction == nil || *st.Direction%8 != layout.N%8 {
			t.Errorf("подземка %d: направление %v, ждали север", i+1, st.Direction)
		}
		if !st.UGPaired {
			t.Errorf("подземка %d не спарилась", i+1)
		}
	}
	clean(t, h)
}

// TestPickupSplitterKeepsNeighbors — подбор делителя (переставить не тем направлением вставший)
// не уносит соседние ленты: зажатая ПКМ добыла ленту отвода, оказавшуюся под курсором после
// делителя (стадия 21 на 859088572: «не стоит transport-belt»).
func TestPickupSplitterKeepsNeighbors(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "splitter") {
		t.Skip("делители не открыты (logistics)")
	}
	have(t, h, "splitter", 1) // делитель крафтится из лент — сначала он, потом ленты на постановку
	have(t, h, "transport-belt", 8)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X + 25, Y: p0.Position.Y + 25}, 10, 10, 60)
	if !ok {
		t.Skip("нет свободной площадки 10×10")
	}
	x, y := math.Floor(c.X)+0.5, math.Floor(c.Y)+0.5
	ctx := ctxT(t, 4*time.Minute)
	// лента на восток, отвод: делитель на ней, со второго выхода — лента вниз и на восток
	var pos []bridge.Pos
	var dirs []int
	for dx := -2.0; dx <= 2; dx++ {
		pos, dirs = append(pos, bridge.Pos{X: x + dx, Y: y}), append(dirs, layout.E)
	}
	pos, dirs = append(pos, bridge.Pos{X: x + 1, Y: y + 1}, bridge.Pos{X: x + 1, Y: y + 2}), append(dirs, layout.S, layout.E)
	if _, err := h.PlaceDirs(ctx, "transport-belt", pos, dirs); err != nil {
		t.Fatal(err)
	}
	sp, err := h.Place(ctx, "splitter", bridge.Pos{X: x, Y: y + 0.5}, layout.E)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Pickup(ctx, sp); err != nil {
		t.Fatal(err)
	}
	for i, p := range pos {
		if p.X == x {
			continue // под делителем
		}
		if st, err := h.B.GetEntity("transport-belt", p); err != nil || st == nil {
			t.Errorf("лента %d в (%.1f, %.1f) пропала после подбора делителя (%v)", i+1, p.X, p.Y, err)
		}
	}
	clean(t, h)
}

// TestPlaceUndergroundHandFromLastRun — поворот руки с подземкой игра помнит и между запусками
// бота: прошлый запуск ставил подземку на восток, новый (руки заново, память пуста) ставит на
// север — подземка встаёт на север (стадия 23 на 794457638: «не встал направлением 0»).
func TestPlaceUndergroundHandFromLastRun(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "underground-belt") {
		t.Skip("подземки не открыты (logistics)")
	}
	have(t, h, "underground-belt", 4)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X - 25, Y: p0.Position.Y - 25}, 10, 10, 60)
	if !ok {
		t.Skip("нет свободной площадки 10×10")
	}
	x, y := math.Floor(c.X)+0.5, math.Floor(c.Y)+0.5
	ctx := ctxT(t, 4*time.Minute)
	// прошлый запуск: подземка на восток
	if _, err := h.Place(ctx, "underground-belt", bridge.Pos{X: x - 3, Y: y - 3}, layout.E); err != nil {
		t.Fatal(err)
	}
	// новый запуск: руки заново — память руки пуста (ввод, справочник и карта те же)
	h2, err := act.New(h.B, h.In)
	if err != nil {
		t.Fatal(err)
	}
	h2.Protos, h2.World, h2.Log, h2.DebugDir, h2.Console = h.Protos, h.World, h.Log, h.DebugDir, h.Console
	pos := []bridge.Pos{{X: x, Y: y + 2}, {X: x, Y: y - 1}}
	if _, err := h2.PlaceDirs(ctx, "underground-belt", pos, []int{layout.N, layout.N}); err != nil {
		t.Fatal(err)
	}
	for i, p := range pos {
		st, err := h2.B.GetEntity("underground-belt", p)
		if err != nil || st == nil {
			t.Fatalf("подземка %d в (%.1f, %.1f) не стоит (%v)", i+1, p.X, p.Y, err)
		}
		if st.Direction == nil || *st.Direction%8 != layout.N%8 {
			t.Errorf("подземка %d: направление %v, ждали север", i+1, st.Direction)
		}
	}
	clean(t, h2)
}

// TestPlaceSplitterCrosswiseKeepsNeighbors — делитель встал поперёк (рука повёрнута с прошлого
// запуска), накрыл ленту отвода рядом; после перестановки лента на месте (стадия 21 на
// 859088572: «не стоит transport-belt»).
func TestPlaceSplitterCrosswiseKeepsNeighbors(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "splitter") {
		t.Skip("делители не открыты (logistics)")
	}
	have(t, h, "splitter", 2)
	have(t, h, "transport-belt", 8)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X + 25, Y: p0.Position.Y - 25}, 10, 10, 60)
	if !ok {
		t.Skip("нет свободной площадки 10×10")
	}
	x, y := math.Floor(c.X)+0.5, math.Floor(c.Y)+0.5
	ctx := ctxT(t, 5*time.Minute)
	// прошлый запуск: делитель на север
	if _, err := h.Place(ctx, "splitter", bridge.Pos{X: x - 3, Y: y - 3}, layout.N); err != nil {
		t.Fatal(err)
	}
	var pos []bridge.Pos
	var dirs []int
	for dx := -2.0; dx <= 2; dx++ {
		pos, dirs = append(pos, bridge.Pos{X: x + dx, Y: y}), append(dirs, layout.E)
	}
	pos, dirs = append(pos, bridge.Pos{X: x + 1, Y: y + 1}, bridge.Pos{X: x + 1, Y: y + 2}), append(dirs, layout.S, layout.E)
	if _, err := h.PlaceDirs(ctx, "transport-belt", pos, dirs); err != nil {
		t.Fatal(err)
	}
	// новый запуск: память руки пуста
	h2, err := act.New(h.B, h.In)
	if err != nil {
		t.Fatal(err)
	}
	h2.Protos, h2.World, h2.Log, h2.DebugDir, h2.Console = h.Protos, h.World, h.Log, h.DebugDir, h.Console
	if _, err := h2.Place(ctx, "splitter", bridge.Pos{X: x, Y: y + 0.5}, layout.E); err != nil {
		t.Fatal(err)
	}
	for i, p := range pos {
		if p.X == x {
			continue // под делителем
		}
		if st, err := h2.B.GetEntity("transport-belt", p); err != nil || st == nil {
			t.Errorf("лента %d в (%.1f, %.1f) пропала (%v)", i+1, p.X, p.Y, err)
		}
	}
	clean(t, h2)
}

// keepNeighborsSpot — свободная площадка 10×10 со смещением d от персонажа (у каждого теста
// своя: тесты за собой не убирают); центр тайла.
func keepNeighborsSpot(t *testing.T, h *act.Hands, d bridge.Pos) (float64, float64) {
	t.Helper()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X + d.X, Y: p0.Position.Y + d.Y}, 10, 10, 60)
	if !ok {
		t.Skip("нет свободной площадки 10×10")
	}
	return math.Floor(c.X) + 0.5, math.Floor(c.Y) + 0.5
}

// standing — что из list (имя, место) стоит; не стоит — ошибка теста.
func standing(t *testing.T, h *act.Hands, name string, list []bridge.Pos, what string) {
	t.Helper()
	for i, p := range list {
		if st, err := h.B.GetEntity(name, p); err != nil || st == nil {
			t.Errorf("%s %d (%s в (%.1f, %.1f)) пропал(а) (%v)", what, i+1, name, p.X, p.Y, err)
		}
	}
}

// TestPickupInserterKeepsNeighbors — подбор инсертера между лентой и сундуком: лента и сундук
// на месте (зажатая ПКМ после цели не добывает то, что под курсором).
func TestPickupInserterKeepsNeighbors(t *testing.T) {
	h := handsT(t)
	have(t, h, "inserter", 1)
	have(t, h, "iron-chest", 1)
	have(t, h, "transport-belt", 5)
	x, y := keepNeighborsSpot(t, h, bridge.Pos{Y: 35})
	ctx := ctxT(t, 4*time.Minute)
	var belts []bridge.Pos
	for dx := -2.0; dx <= 2; dx++ {
		belts = append(belts, bridge.Pos{X: x + dx, Y: y})
	}
	if _, err := h.PlaceDirs(ctx, "transport-belt", belts, []int{layout.E, layout.E, layout.E, layout.E, layout.E}); err != nil {
		t.Fatal(err)
	}
	chest := bridge.Pos{X: x, Y: y + 2}
	if _, err := h.Place(ctx, "iron-chest", chest, layout.N); err != nil {
		t.Fatal(err)
	}
	ins, err := h.Place(ctx, "inserter", bridge.Pos{X: x, Y: y + 1}, layout.N)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Pickup(ctx, ins); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.B.GetEntity("inserter", ins.Position); st != nil {
		t.Errorf("инсертер не поднят")
	}
	standing(t, h, "transport-belt", belts, "лента")
	standing(t, h, "iron-chest", []bridge.Pos{chest}, "сундук")
	clean(t, h)
}

// TestPickupAllKeepsOthers — подбор лент ряда через одну: поднятые — нет, остальные — на месте.
func TestPickupAllKeepsOthers(t *testing.T) {
	h := handsT(t)
	have(t, h, "transport-belt", 7)
	x, y := keepNeighborsSpot(t, h, bridge.Pos{Y: -35})
	ctx := ctxT(t, 4*time.Minute)
	var all []bridge.Pos
	var dirs []int
	for dx := -3.0; dx <= 3; dx++ {
		all, dirs = append(all, bridge.Pos{X: x + dx, Y: y}), append(dirs, layout.E)
	}
	built, err := h.PlaceDirs(ctx, "transport-belt", all, dirs)
	if err != nil {
		t.Fatal(err)
	}
	var take []bridge.Entity
	var keep []bridge.Pos
	for i, e := range built {
		if i%2 == 1 {
			take = append(take, e)
		} else {
			keep = append(keep, all[i])
		}
	}
	if err := h.PickupAll(ctx, take); err != nil {
		t.Fatal(err)
	}
	for _, e := range take {
		if st, _ := h.B.GetEntity(e.Name, e.Position); st != nil {
			t.Errorf("лента в (%.1f, %.1f) не поднята", e.Position.X, e.Position.Y)
		}
	}
	standing(t, h, "transport-belt", keep, "лента")
	clean(t, h)
}

// TestPickupUndergroundKeepsNeighbors — подбор входа подземки рядом с параллельной лентой: лента
// рядом, лента перед входом и выход — на месте.
func TestPickupUndergroundKeepsNeighbors(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "underground-belt") {
		t.Skip("подземки не открыты (logistics)")
	}
	have(t, h, "underground-belt", 2)
	have(t, h, "transport-belt", 7)
	x, y := keepNeighborsSpot(t, h, bridge.Pos{X: 35})
	ctx := ctxT(t, 4*time.Minute)
	// параллельная лента сверху, перед входом — лента
	var belts []bridge.Pos
	var dirs []int
	for dx := -2.0; dx <= 3; dx++ {
		belts, dirs = append(belts, bridge.Pos{X: x + dx, Y: y - 1}), append(dirs, layout.E)
	}
	belts, dirs = append(belts, bridge.Pos{X: x - 1, Y: y}), append(dirs, layout.E)
	if _, err := h.PlaceDirs(ctx, "transport-belt", belts, dirs); err != nil {
		t.Fatal(err)
	}
	ug, err := h.PlaceDirs(ctx, "underground-belt", []bridge.Pos{{X: x, Y: y}, {X: x + 3, Y: y}}, []int{layout.E, layout.E})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Pickup(ctx, ug[0]); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.B.GetEntity("underground-belt", ug[0].Position); st != nil {
		t.Errorf("вход подземки не поднят")
	}
	standing(t, h, "transport-belt", belts, "лента")
	standing(t, h, "underground-belt", []bridge.Pos{ug[1].Position}, "выход подземки")
	clean(t, h)
}

// TestReplaceUndergroundKeepsNeighbors — подземка встала не так (рука повёрнута с прошлого
// запуска) и переставляется рядом с лентами: соседние ленты на месте.
func TestReplaceUndergroundKeepsNeighbors(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "underground-belt") {
		t.Skip("подземки не открыты (logistics)")
	}
	have(t, h, "underground-belt", 3)
	have(t, h, "transport-belt", 6)
	x, y := keepNeighborsSpot(t, h, bridge.Pos{X: -35})
	ctx := ctxT(t, 5*time.Minute)
	// прошлый запуск: подземка на запад
	if _, err := h.Place(ctx, "underground-belt", bridge.Pos{X: x - 4, Y: y - 4}, layout.W); err != nil {
		t.Fatal(err)
	}
	// ленты по обе стороны будущих подземок
	var belts []bridge.Pos
	var dirs []int
	for dy := -1.0; dy <= 3; dy += 4 {
		for dx := -1.0; dx <= 1; dx++ {
			belts, dirs = append(belts, bridge.Pos{X: x + dx, Y: y + dy}), append(dirs, layout.E)
		}
	}
	if _, err := h.PlaceDirs(ctx, "transport-belt", belts, dirs); err != nil {
		t.Fatal(err)
	}
	// новый запуск: память руки пуста
	h2, err := act.New(h.B, h.In)
	if err != nil {
		t.Fatal(err)
	}
	h2.Protos, h2.World, h2.Log, h2.DebugDir, h2.Console = h.Protos, h.World, h.Log, h.DebugDir, h.Console
	pos := []bridge.Pos{{X: x, Y: y + 2}, {X: x, Y: y}}
	if _, err := h2.PlaceDirs(ctx, "underground-belt", pos, []int{layout.N, layout.N}); err != nil {
		t.Fatal(err)
	}
	standing(t, h2, "transport-belt", belts, "лента")
	for i, p := range pos {
		st, err := h2.B.GetEntity("underground-belt", p)
		if err != nil || st == nil || st.Direction == nil || *st.Direction != layout.N {
			t.Errorf("подземка %d в (%.1f, %.1f): %v, ждали на север", i+1, p.X, p.Y, st)
		}
	}
	clean(t, h2)
}

// Взять из сундука только нужное (дело на будущее из questions.md: из хранения бот забирал сундук
// целиком — Ctrl+клик, инвентарь забивался, на стадии 23 на 859088572 дерево не срубить): в
// сундуке пачки двух предметов, взять 100 одного — у бота его +100…150 (пачками), другого —
// ничего, остальное — в сундуке.
func TestTakeItemFromChest(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 3*time.Minute)
	have(t, h, "wooden-chest", 1)
	have(t, h, "coal", 200) // уголь и камень на картах есть с запасом (дерево — рубить)
	have(t, h, "stone", 150)
	spot := freeArea(t, h, 2)
	ch, err := h.Place(ctx, "wooden-chest", spot, act.North)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Insert(ctx, ch, "coal", 200); err != nil {
		t.Fatal(err)
	}
	if err := h.Insert(ctx, ch, "stone", 150); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	if err := h.TakeItem(ctx, ch, "coal", 100); err != nil {
		t.Fatal(err)
	}
	p1, _ := h.B.Player()
	if d := p1.Inventory["coal"] - p0.Inventory["coal"]; d < 100 || d > 150 {
		t.Errorf("coal +%d, ждали 100…150", d)
	}
	if d := p1.Inventory["stone"] - p0.Inventory["stone"]; d != 0 {
		t.Errorf("stone +%d, ждали 0", d)
	}
	st, err := h.B.GetEntity(ch.Name, ch.Position)
	if err != nil || st == nil {
		t.Fatalf("сундук: %v", err)
	}
	if n := st.Count("stone"); n != 150 {
		t.Errorf("в сундуке stone %d, ждали 150", n)
	}
	if p, _ := h.B.Player(); p.OpenedGUI != "" && p.OpenedGUI != "none" {
		t.Errorf("окно не закрыто: %q", p.OpenedGUI)
	}
}

// Добыча руками при полном инвентаре (стадия 14 на 1028292478: «инвентарь полон: coal не
// поднять» — сбор падал): полон — освободить место (OnFull) и добывать дальше. Инвентарь должен
// быть полон с начала — карту готовит пользователь (на картах -0 его нет): не полон — пропуск.
func TestGatherFullInventory(t *testing.T) {
	h := handsT(t)
	p, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	if free := p.InventorySize - len(p.InventorySlots); free > 1 {
		t.Skipf("инвентарь не полон (свободно %d из %d) — нужна карта с полным инвентарём", free, p.InventorySize)
	}
	box := ""
	for _, c := range []string{"wooden-chest", "iron-chest"} {
		if p.Inventory[c] > 0 {
			box = c
		}
	}
	if box == "" {
		t.Skip("в полном инвентаре нет сундука — освободить место некуда")
	}
	ctx := ctxT(t, 5*time.Minute)
	// освободить место: сундук рядом, в него — самое многочисленное, кроме угля
	h.OnFull = func(ctx context.Context) error {
		q, err := h.B.Player()
		if err != nil {
			return err
		}
		it, n := "", 0
		for k, v := range q.Inventory {
			if k != "coal" && k != box && v > n {
				it, n = k, v
			}
		}
		e, err := h.Place(ctx, box, freeArea(t, h, 3), act.North)
		if err != nil {
			return err
		}
		_, err = h.InsertAll(ctx, e, it)
		return err
	}
	defer func() { h.OnFull = nil }()
	before := p.Inventory["coal"]
	if _, err := h.Gather(ctx, "coal", 20, 64); err != nil {
		if errors.Is(err, act.ErrNoTargets) {
			t.Skipf("нет угля рядом: %v", err)
		}
		t.Fatalf("уголь при полном инвентаре: %v", err)
	}
	if q, _ := h.B.Player(); q.Inventory["coal"] <= before {
		t.Errorf("угля не прибавилось: %d → %d", before, q.Inventory["coal"])
	}
	clean(t, h)
}

// Подобрать столб под кроной дерева (стадия 28 на 859088572: старый столб накрыла крона — подбор
// наводился на дерево): дерево, чья крона накрывает столб, рубится, столб поднимается.
func TestPickupPoleUnderCrown(t *testing.T) {
	h := handsT(t)
	have(t, h, "small-electric-pole", 1)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	// свободный тайл в кроне дерева (рамка выделения), вне его рамки коллизии
	var at bridge.Pos
	best := math.Inf(1)
	for _, o := range h.World.ObjectsNear(p.Position, 60) {
		if o.Type != "tree" {
			continue
		}
		sel, col := h.World.SelectionBox(o), h.World.CollisionBox(o)
		for y := int(math.Floor(sel.LeftTop.Y)); y < int(math.Ceil(sel.RightBottom.Y)); y++ {
			for x := int(math.Floor(sel.LeftTop.X)); x < int(math.Ceil(sel.RightBottom.X)); x++ {
				tl := world.Tile{X: x, Y: y}
				c := tl.Center()
				inCol := float64(x+1) > col.LeftTop.X && float64(x) < col.RightBottom.X && float64(y+1) > col.LeftTop.Y && float64(y) < col.RightBottom.Y
				inSel := c.X > sel.LeftTop.X && c.X < sel.RightBottom.X && c.Y > sel.LeftTop.Y && c.Y < sel.RightBottom.Y
				if ok, _ := h.World.Buildable(tl); !ok || inCol || !inSel || h.World.Blocked(tl) {
					continue
				}
				if d := dist(c, p.Position); d < best {
					at, best = c, d
				}
			}
		}
	}
	if math.IsInf(best, 1) {
		t.Skip("нет дерева с кроной над свободным тайлом рядом")
	}
	ctx := ctxT(t, 4*time.Minute)
	pole, err := h.Place(ctx, "small-electric-pole", at, act.North)
	if err != nil {
		t.Fatalf("столб под кроной: %v", err)
	}
	if err := h.Pickup(ctx, pole); err != nil {
		t.Fatalf("подобрать столб под кроной: %v", err)
	}
	if got, _ := h.B.EntityAt(pole.Name, pole.Position); got != nil {
		t.Errorf("столб стоит в (%.1f, %.1f)", pole.Position.X, pole.Position.Y)
	}
	clean(t, h)
}

// Делители, когда первый встал поперёк и его переставили (стадия 16 на 1028292478: перестановка
// увела персонажа — следующий оказался вне досягаемости, «Cannot reach»): поворот делителя в руке
// у игры не тот, что помнит бот; три делителя через 8 тайлов — все встают на восток.
func TestPlaceSplittersAfterReplace(t *testing.T) {
	h := handsT(t)
	have(t, h, "splitter", 3)
	c := freeArea(t, h, 22)
	ctx := ctxT(t, 4*time.Minute)
	// рука в игре повёрнута, а бот этого не знает
	if err := h.Pick(ctx, "splitter"); err != nil {
		t.Fatal(err)
	}
	if err := h.In.Tap("r"); err != nil {
		t.Fatal(err)
	}
	if err := h.ClearCursor(ctx); err != nil {
		t.Fatal(err)
	}
	x0, y0 := math.Floor(c.X)-8, math.Floor(c.Y)
	var pos []bridge.Pos
	for i := 0; i < 3; i++ {
		pos = append(pos, bridge.Pos{X: x0 + float64(8*i) + 0.5, Y: y0}) // на восток: 1×2, центр на стыке
	}
	if _, err := h.PlaceDirs(ctx, "splitter", pos, []int{act.East, act.East, act.East}); err != nil {
		t.Fatalf("делители: %v", err)
	}
	for _, q := range pos {
		st, err := h.B.GetEntity("splitter", q)
		if err != nil || st == nil || st.Direction == nil || *st.Direction != act.East {
			t.Errorf("делитель в (%.1f, %.1f): %+v (%v)", q.X, q.Y, st, err)
		}
	}
	clean(t, h)
}

// Подземки в проходе в тайл между рядами лент шины (стадия 22 на 1028292478: встать было негде,
// кроме лент, — «не сойти с ленты»): по 3 ряда лент сверху и снизу прохода, пара подземок —
// в проходе; персонаж приходит снаружи.
func TestPlaceUndergroundInBeltCorridor(t *testing.T) {
	h := handsT(t)
	have(t, h, "underground-belt", 2) // сначала: крафт подземок ест ленты
	have(t, h, "transport-belt", 96)
	c := freeArea(t, h, 20)
	ctx := ctxT(t, 6*time.Minute)
	x0, y0 := math.Floor(c.X)-8, math.Floor(c.Y)
	var rows []bridge.Pos
	for _, dy := range []float64{-3, -2, -1, 1, 2, 3} {
		for dx := 0.0; dx < 16; dx++ {
			rows = append(rows, bridge.Pos{X: x0 + dx + 0.5, Y: y0 + dy + 0.5})
		}
	}
	if _, err := h.PlaceAll(ctx, "transport-belt", rows, act.East); err != nil {
		t.Fatalf("ряды лент: %v", err)
	}
	if err := h.WalkTo(ctx, bridge.Pos{X: x0 - 6, Y: y0 + 0.5}, 1); err != nil {
		t.Fatalf("выйти из рядов: %v", err)
	}
	in, out := bridge.Pos{X: x0 + 5.5, Y: y0 + 0.5}, bridge.Pos{X: x0 + 9.5, Y: y0 + 0.5}
	if _, err := h.PlaceDirs(ctx, "underground-belt", []bridge.Pos{in, out}, []int{act.East, act.East}); err != nil {
		t.Fatalf("подземки в проходе: %v", err)
	}
	for _, q := range []struct {
		p    bridge.Pos
		kind string
	}{{in, "input"}, {out, "output"}} {
		st, err := h.B.GetEntity("underground-belt", q.p)
		if err != nil || st == nil || st.UGType != q.kind || !st.UGPaired {
			t.Errorf("подземка в (%.1f, %.1f): %+v (%v), ждали %s в паре", q.p.X, q.p.Y, st, err, q.kind)
		}
	}
	clean(t, h)
}

// TestPlacePipeToGroundChain — трасса подземных труб: пары вплотную друг к другу, как у трассы
// нефти (стадия 34 на 427965634: 182 из 188 подземных труб «встал не тем направлением —
// переставляю», по три подбора на каждую). Все встают своим направлением, без перестановок.
func TestPlacePipeToGroundChain(t *testing.T) {
	h := handsT(t)
	have(t, h, "pipe-to-ground", 6)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, p0.Position, 34, 4, 60)
	if !ok {
		t.Skip("нет свободной площадки 34×4")
	}
	x, y := math.Floor(c.X)-15+0.5, math.Floor(c.Y)+0.5
	var pos []bridge.Pos
	var dirs []int
	for k := 0.0; k < 3; k++ { // вход смотрит назад (на запад), выход — вперёд
		pos = append(pos, bridge.Pos{X: x + 10*k, Y: y}, bridge.Pos{X: x + 10*k + 9, Y: y})
		dirs = append(dirs, act.West, act.East)
	}
	ctx := ctxT(t, 4*time.Minute)
	_, seq0, _ := h.B.PollEvents(1 << 30)
	if _, err := h.PlaceDirs(ctx, "pipe-to-ground", pos, dirs); err != nil {
		t.Fatal(err)
	}
	for i, p := range pos {
		st, err := h.B.GetEntity("pipe-to-ground", p)
		if err != nil || st == nil {
			t.Fatalf("труба %d в (%.1f, %.1f) не стоит (%v)", i+1, p.X, p.Y, err)
		}
		if st.Direction == nil || *st.Direction != dirs[i] {
			t.Errorf("труба %d: направление %v, ждали %d", i+1, st.Direction, dirs[i])
		}
	}
	evs, _, err := h.B.PollEvents(seq0)
	if err != nil {
		t.Fatal(err)
	}
	mined := 0
	for _, e := range evs {
		if e.Type == "mined_entity" && e.Entity != nil && e.Entity.Name == "pipe-to-ground" {
			mined++
		}
	}
	if mined > 0 {
		t.Errorf("переставлял %d раз — ставить сразу своим направлением", mined)
	}
	clean(t, h)
}

// TestPlaceGridAroundSelf — сплошная сетка построек, персонаж стоит на месте одной из них
// (стадия 33 на 267145981: цистерны сеткой без зазоров — бот обставил себя ими и остался в
// последней дыре, «не дошёл»). Всё встаёт, ничего не поднимается (пролом своих — не выход).
func TestPlaceGridAroundSelf(t *testing.T) {
	h := handsT(t)
	have(t, h, "wooden-chest", 25)
	c := freeArea(t, h, 9)
	ctx := ctxT(t, 4*time.Minute)
	x, y := math.Floor(c.X)+0.5, math.Floor(c.Y)+0.5
	if err := h.WalkTo(ctx, bridge.Pos{X: x, Y: y}, 0.3); err != nil {
		t.Fatalf("встать в середину: %v", err)
	}
	var pos []bridge.Pos
	for dy := -2.0; dy <= 2; dy++ {
		for dx := -2.0; dx <= 2; dx++ {
			pos = append(pos, bridge.Pos{X: x + dx, Y: y + dy})
		}
	}
	_, seq0, _ := h.B.PollEvents(1 << 30)
	if _, err := h.PlaceAll(ctx, "wooden-chest", pos, act.North); err != nil {
		t.Fatal(err)
	}
	for _, p := range pos {
		if st, err := h.B.GetEntity("wooden-chest", p); err != nil || st == nil {
			t.Errorf("сундук в (%.1f, %.1f) не стоит (%v)", p.X, p.Y, err)
		}
	}
	evs, _, err := h.B.PollEvents(seq0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type == "mined_entity" && e.Entity != nil && e.Entity.Name == "wooden-chest" {
			t.Errorf("поднимал свой сундук в (%.1f, %.1f) — замуровал себя", e.Entity.Position.X, e.Entity.Position.Y)
		}
	}
	clean(t, h)
}

// Крафт насоса (стадия 34: «поиск "pump" — совпадения на разных вкладках»: насос, помпа и
// качалка на разных вкладках при любом слове) — сначала слово только с вкладки насоса. Нужны
// открытый рецепт и двигатели (руками не делаются) — нет на карте: пропуск.
func TestCraftPump(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "pump") {
		t.Skip("насос не открыт (fluid-handling) — что добавить на карты")
	}
	p, _ := h.B.Player()
	if p.Inventory["engine-unit"] < 1 {
		t.Skip("нет двигателя (руками не делается) — что добавить на карты")
	}
	have(t, h, "pipe", 1)
	ctx := ctxT(t, 2*time.Minute)
	before := p.Inventory["pump"]
	if err := h.CraftWait(ctx, "pump", 1); err != nil {
		t.Fatalf("крафт насоса: %v", err)
	}
	if q, _ := h.B.Player(); q.Inventory["pump"] != before+1 {
		t.Errorf("насосов %d → %d", before, q.Inventory["pump"])
	}
	clean(t, h)
}

// TestPlaceUndergroundOutputFirst — пара подземок в списке постановки выходом вперёд (сквозная
// лента bus-2 → bus-3 в плане записана от конца к началу): ставить всё равно сначала вход,
// потом выход — иначе первая поставленная становится входом и пара не складывается (стадия 40
// на 427965634: сера застряла во входе подземки, выход стоял «входом», синяя наука без серы).
func TestPlaceUndergroundOutputFirst(t *testing.T) {
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "underground-belt") {
		t.Skip("подземки не открыты (logistics)")
	}
	have(t, h, "underground-belt", 2)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	c, ok := spot(h, bridge.Pos{X: p0.Position.X + 25, Y: p0.Position.Y - 25}, 10, 10, 60)
	if !ok {
		t.Skip("нет свободной площадки 10×10")
	}
	x, y := math.Floor(c.X)+0.5, math.Floor(c.Y)+0.5
	ctx := ctxT(t, 4*time.Minute)
	// лента на запад: вход в (x, y), выход в (x−5, y) — в списке выход первым, как в плане
	pos := []bridge.Pos{{X: x - 5, Y: y}, {X: x, Y: y}}
	dirs := []int{layout.W, layout.W}
	if _, err := h.PlaceDirs(ctx, "underground-belt", pos, dirs); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"output", "input"} {
		p := pos[i]
		st, err := h.B.GetEntity("underground-belt", p)
		if err != nil || st == nil {
			t.Fatalf("подземка в (%.1f, %.1f) не стоит (%v)", p.X, p.Y, err)
		}
		if st.Direction == nil || *st.Direction != layout.W {
			t.Errorf("подземка в (%.1f, %.1f): направление %v, ждали запад", p.X, p.Y, st.Direction)
		}
		if st.UGType != want || !st.UGPaired {
			t.Errorf("подземка в (%.1f, %.1f): %s, спарена %v — ждали %s в паре", p.X, p.Y, st.UGType, st.UGPaired, want)
		}
	}
	clean(t, h)
}
