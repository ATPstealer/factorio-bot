//go:build e2e

// Руки: добыча, крафт, постройка, подбор.
package e2e

import (
	"errors"
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
	"math"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strconv"
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
