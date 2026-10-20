//go:build e2e

// Руки: добыча, крафт, постройка, подбор.
package e2e

import (
	"errors"
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"
)

// Добыча: дойти до дерева, навести курсор, срубить; событие mined_entity в буфере.
func TestMine(t *testing.T) {
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

// Печь как можно ближе к дереву, срубить дерево — печь цела.
func TestFurnaceHugsTree(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 3*time.Minute)
	have(t, h, "stone-furnace", 1)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	trees, err := h.B.FindMinable("tree", 300, 40)
	if err != nil {
		t.Fatal(err)
	}
	fb := h.Protos.Entities["stone-furnace"].CollisionBox
	var tree bridge.Entity
	var spot bridge.Pos
	best := math.Inf(1)
	for _, tr := range trees {
		tb := h.World.CollisionBox(world.Object{Name: tr.Name, Pos: tr.Position})
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				c := bridge.Pos{X: math.Round(tr.Position.X) + float64(dx), Y: math.Round(tr.Position.Y) + float64(dy)}
				s, ok := h.World.FreeSpot(c, 2, 2, 0, &p0.Position)
				if !ok {
					continue
				}
				box := bridge.Box{LeftTop: bridge.Pos{X: s.X + fb.LeftTop.X, Y: s.Y + fb.LeftTop.Y},
					RightBottom: bridge.Pos{X: s.X + fb.RightBottom.X, Y: s.Y + fb.RightBottom.Y}}
				if g := boxGap(box, tb); g < best {
					best, tree, spot = g, tr, s
				}
			}
		}
		if best < 0.35 {
			break
		}
	}
	if math.IsInf(best, 1) {
		t.Skip("нет места рядом с деревьями")
	}
	t.Logf("дерево %s %v, печь в %v, зазор %.2f тайла", tree.Name, tree.Position, spot, best)
	f, err := h.Place(ctx, "stone-furnace", spot, act.North)
	if err != nil {
		t.Fatal(err)
	}
	if best > 0.7 {
		t.Errorf("зазор между печью и деревом %.2f — не вплотную", best)
	}
	if err := h.WalkToMine(ctx, tree); err != nil {
		t.Fatal(err)
	}
	if err := h.MineCovered(ctx, tree, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if e, _ := h.B.EntityAt(tree.Name, tree.Position); e != nil {
		t.Error("дерево на месте")
	}
	if st, _ := h.B.GetEntity(f.Name, f.Position); st == nil {
		t.Fatal("рубя дерево, бот разобрал печь")
	}
	clean(t, h)
}

// Бур 2×2 ровно на одной клетке руды (остальные три — пустая суша), повёрнут на восток; подобрать.
func TestDrillOneOreTile(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 3*time.Minute)
	have(t, h, "burner-mining-drill", 1)
	p0, _ := h.B.Player()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	w := h.World
	isOre := func(tl world.Tile) bool { _, ok := w.ResourceAt(tl); return ok }
	free := func(tl world.Tile) bool { return w.TileAt(tl) == world.Land && !w.Blocked(tl) }
	var spot bridge.Pos
	best := math.Inf(1)
	for _, pt := range w.Patches() {
		if pt.Name == "crude-oil" {
			continue
		}
		for _, ore := range pt.Tiles {
			for _, o := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				lt := world.Tile{X: ore.X - o[0], Y: ore.Y - o[1]}
				n, ok := 0, true
				for y := 0; y < 2 && ok; y++ {
					for x := 0; x < 2 && ok; x++ {
						tl := world.Tile{X: lt.X + x, Y: lt.Y + y}
						ok = free(tl)
						if isOre(tl) {
							n++
						}
					}
				}
				c := bridge.Pos{X: float64(lt.X) + 1, Y: float64(lt.Y) + 1}
				if d := math.Hypot(c.X-p0.Position.X, c.Y-p0.Position.Y); ok && n == 1 && d < best && d > 2 {
					best, spot = d, c
				}
			}
		}
	}
	if math.IsInf(best, 1) {
		t.Skip("нет места, где 2×2 задевает ровно 1 клетку руды")
	}
	e, err := h.Place(ctx, "burner-mining-drill", spot, act.East)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	lua(t, fmt.Sprintf(`s.count_entities_filtered{type="resource", area={{%f,%f},{%f,%f}}}`,
		spot.X-0.99, spot.Y-0.99, spot.X+0.99, spot.Y+0.99), &n)
	if n != 1 {
		t.Errorf("под буром %d клеток руды, ждали 1", n)
	}
	t.Logf("бур в %v, клеток руды в зоне добычи: %d", spot, n)
	if st, _ := h.B.GetEntity(e.Name, e.Position); st == nil || st.Direction == nil || *st.Direction != act.East {
		t.Errorf("бур не повёрнут на восток: %+v", st)
	}
	// подбор своей постройки
	if err := h.Pickup(ctx, e); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.B.GetEntity(e.Name, e.Position); st != nil {
		t.Error("бур не подобран")
	}
	clean(t, h)
}

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
	if err != nil {
		t.Fatalf("сундук под собой: %v", err)
	}
	p, _ = h.B.Player()
	next := bridge.Pos{X: math.Floor(p.Position.X) + 2, Y: math.Floor(p.Position.Y)} // центр 2×2 — на стыке тайлов
	furnace, err := h.Place(ctx, "stone-furnace", next, act.North)
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
