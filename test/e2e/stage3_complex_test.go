//go:build e2e

// Стадия 3 — сложные сценарии на точность ввода и поиск пути (по заданию пользователя):
// улитка из печей вокруг себя и выход из неё; печь вплотную к дереву; бур ровно на одной
// клетке руды; 10 предметов на земле и сбор в заданном порядке.
package e2e

import (
	"factorio-bot/internal/exec"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// freeArea — центр свободной (суша без объектов) площадки size×size рядом с игроком, не под ним.
func freeArea(t *testing.T, h *act.Hands, size int) bridge.Pos {
	t.Helper()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	c, ok := h.World.FreeSpot(p.Position, size, size, 60, &p.Position)
	if !ok {
		t.Skipf("нет свободной площадки %d×%d", size, size)
	}
	return c
}

// snailBlocks — левые верхние тайлы печей 2×2 относительно центра: комната 2×2 с игроком,
// кольцо 1 (проём на севере), коридор в 1 тайл, кольцо 2 (проём на юге).
func snailBlocks() []world.Tile {
	var res []world.Tile
	for _, y := range []int{-3, -1, 1} {
		for _, x := range []int{-3, -1, 1} {
			if (x == -1 && y == -1) || (x == -1 && y == -3) { // комната и проём кольца 1
				continue
			}
			res = append(res, world.Tile{X: x, Y: y})
		}
	}
	for _, x := range []int{-6, -4, -2, 0, 2, 4} {
		res = append(res, world.Tile{X: x, Y: -6})
		if x != 0 { // проём кольца 2
			res = append(res, world.Tile{X: x, Y: 4})
		}
	}
	for _, y := range []int{-4, -2, 0, 2} {
		res = append(res, world.Tile{X: -6, Y: y}, world.Tile{X: 4, Y: y})
	}
	return res
}

func pathLen(from bridge.Pos, pts []bridge.Pos) float64 {
	l := 0.0
	for _, p := range pts {
		l += math.Hypot(p.X-from.X, p.Y-from.Y)
		from = p
	}
	return l
}

// Улитка из печей вокруг себя, затем выход из лабиринта.
// Прочий сценарий (SET=extra).
func TestExtra_SnailMaze(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 10*time.Minute)
	blocks := snailBlocks()
	have(t, h, "stone-furnace", len(blocks))

	c := freeArea(t, h, 18) // кольцо 2 — 12×12, вокруг — запас для выхода
	if err := h.WalkTo(ctx, c, 0.5); err != nil {
		t.Fatal(err)
	}
	var positions []bridge.Pos
	for _, b := range blocks {
		positions = append(positions, bridge.Pos{X: c.X + float64(b.X) + 1, Y: c.Y + float64(b.Y) + 1})
	}
	start := time.Now()
	placed, err := h.PlaceAll(ctx, "stone-furnace", positions, act.North)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("поставлено %d за %.1f с", len(placed), time.Since(start).Seconds())
	p, _ := h.B.Player()
	if math.Abs(p.Position.X-c.X) > 1 || math.Abs(p.Position.Y-c.Y) > 1 {
		t.Fatalf("строя улитку, персонаж вышел из комнаты: %v, центр %v", p.Position, c)
	}

	// выход: точка к северу снаружи; прямо — 8 тайлов, по лабиринту — через оба проёма
	out := bridge.Pos{X: c.X + 0.5, Y: c.Y - 8.5}
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	pts, err := h.World.Path(p.Position, out, 0.5, 0)
	if err != nil {
		t.Fatalf("пути из улитки нет: %v", err)
	}
	l := pathLen(p.Position, pts)
	t.Logf("путь из улитки: %d точек поворота, %.0f тайлов (напрямую %.0f)", len(pts), l, math.Hypot(out.X-p.Position.X, out.Y-p.Position.Y))
	if l < 25 {
		t.Errorf("путь %.0f тайлов — слишком короткий для улитки (проходит сквозь стены?)", l)
	}
	if err := h.WalkTo(ctx, out, 0.5); err != nil {
		t.Fatal(err)
	}
	p, _ = h.B.Player()
	if math.Abs(p.Position.X-c.X) < 6.5 && math.Abs(p.Position.Y-c.Y) < 6.5 {
		t.Errorf("персонаж всё ещё внутри улитки: %v", p.Position)
	}
	clean(t, h)
}

// boxGap — расстояние между прямоугольниками (0 — касаются или пересекаются).
func boxGap(a, b bridge.Box) float64 {
	dx := math.Max(0, math.Max(a.LeftTop.X-b.RightBottom.X, b.LeftTop.X-a.RightBottom.X))
	dy := math.Max(0, math.Max(a.LeftTop.Y-b.RightBottom.Y, b.LeftTop.Y-a.RightBottom.Y))
	return math.Hypot(dx, dy)
}

// Печь как можно ближе к дереву, срубить дерево — печь цела.
func TestBase_FurnaceHugsTree(t *testing.T) {
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
	if err := h.Mine(ctx, tree, 10*time.Second); err != nil {
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
func TestBase_DrillOneOreTile(t *testing.T) {
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

// 4 разных предмета на земле вокруг себя (просьба пользователя: для скорости), затем сбор в заданном порядке
// (обратном алфавитному): каждый раз поднимается ровно нужный, остальные лежат.
func TestBase_DropAndCollectInOrder(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 8*time.Minute)
	items := []string{"stone", "coal", "wood", "iron-plate"}
	// всё нужное достаёт планировщик: добудет, скрафтит, переплавит, при нужде разведает
	var goals []exec.GoalSpec
	for _, it := range items {
		goals = append(goals, exec.GoalSpec{Kind: "have", Item: it, N: 1})
	}
	st := path("run", "state-drop.json")
	os.Remove(st)
	defer os.Remove(st)
	saved, err := exec.Load(st)
	if err != nil {
		t.Fatal(err)
	}
	saved.Goals = goals
	r := &exec.Runner{H: h, B: h.B, Saved: saved, StatePath: st, LogPath: path("run", "bot-test.log")}
	defer r.Close()
	if err := r.Run(ctx); err != nil {
		t.Fatalf("достать предметы: %v", err)
	}

	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	pl, _ := h.B.Player()
	noOre := func(tl world.Tile) bool { _, ok := h.World.ResourceAt(tl); return !ok }
	c, ok := h.World.FreeSpotWhere(pl.Position, 9, 9, 60, nil, noOre)
	if !ok {
		t.Skip("нет свободной площадки 9×9 без руды")
	}
	c = bridge.Pos{X: c.X + 0.5, Y: c.Y + 0.5}
	if err := h.WalkTo(ctx, c, 0.5); err != nil {
		t.Fatal(err)
	}
	dropped := map[string]bridge.Entity{}
	for i, it := range items {
		a := 2 * math.Pi * float64(i) / float64(len(items))
		pos := world.TileOf(bridge.Pos{X: c.X + 3.2*math.Cos(a), Y: c.Y + 3.2*math.Sin(a)}).Center()
		e, err := h.Drop(ctx, it, pos)
		if err != nil {
			t.Fatal(err)
		}
		dropped[it] = e
	}
	onGround := func() map[string]int {
		es, err := h.B.FindNearest(bridge.FindArgs{Type: "item-entity", Position: &c, Radius: 6, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]int{}
		for _, e := range es {
			m[e.Item]++
		}
		return m
	}
	if g := onGround(); len(g) < len(items) {
		t.Fatalf("на земле %v, ждали %d разных", g, len(items))
	}

	order := append([]string(nil), items...)
	sort.Sort(sort.Reverse(sort.StringSlice(order)))
	for i, it := range order {
		p0, _ := h.B.Player()
		if err := h.PickupItem(ctx, dropped[it]); err != nil {
			t.Fatalf("%d-й (%s): %v", i+1, it, err)
		}
		p1, _ := h.B.Player()
		if p1.Inventory[it] != p0.Inventory[it]+1 {
			t.Errorf("%d-й (%s): в инвентаре %d → %d", i+1, it, p0.Inventory[it], p1.Inventory[it])
		}
		g := onGround()
		for _, rest := range order[i+1:] {
			if g[rest] == 0 {
				t.Errorf("подняв %s, задели %s — он пропал с земли раньше очереди", it, rest)
			}
		}
	}
	clean(t, h)
}

// Выход из ловушки: персонажа обступили кольцом из 8 сундуков — идти наружу: поднять
// сундук, выйти, поставить назад; все сундуки на месте.
func TestBase_Unstuck(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 5*time.Minute)
	have(t, h, "wooden-chest", 8)
	c := freeArea(t, h, 7)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	var ring []bridge.Pos
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx != 0 || dy != 0 {
				ring = append(ring, world.Tile{X: ct.X + dx, Y: ct.Y + dy}.Center())
			}
		}
	}
	if _, err := h.PlaceAll(ctx, "wooden-chest", ring, act.North); err != nil {
		t.Fatal(err)
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	if p, _ = h.B.Player(); !h.World.Enclosed(p.Position) {
		t.Fatalf("кольцо не замкнуто: персонаж в (%.2f, %.2f)", p.Position.X, p.Position.Y)
	}
	target := bridge.Pos{X: ct.Center().X + 8, Y: ct.Center().Y}
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	for _, pos := range ring {
		if e, _ := h.B.EntityAt("wooden-chest", pos); e == nil {
			t.Errorf("сундука в %v нет — не поставлен назад", pos)
		}
	}
	clean(t, h)
}
