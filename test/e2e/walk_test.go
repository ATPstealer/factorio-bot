//go:build e2e

// Ходьба: до точки, в обход воды, из кольца своих построек.
package e2e

import (
	"errors"
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
	"math"
	"testing"
	"time"
)

// Ходьба туда и обратно с точностью 0.5 тайла.
func TestWalk(t *testing.T) {
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

// Выход из ловушки: персонажа обступили кольцом из 8 сундуков, дальше одна команда —
// «иди туда» за кольцо; как выйти, бот решает сам. Проверка — дошёл.
func TestUnstuck(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 5*time.Minute)
	have(t, h, "wooden-chest", 8)
	// свободно и до цели (+8 по X) с запасом 2 тайла: чужие ленты прошлых тестов (PlaceAmongBelts)
	// на цели — встать нельзя (просьба пользователя)
	c := freeArea(t, h, 2*(8+2)+1)
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
	p, _ = h.B.Player()
	if d := math.Hypot(p.Position.X-target.X, p.Position.Y-target.Y); d > 0.5 {
		t.Errorf("до цели осталось %.2f", d)
	}
	left := 0
	for _, pos := range ring {
		if e, _ := h.B.EntityAt("wooden-chest", pos); e != nil {
			left++
		}
	}
	t.Logf("сундуков кольца на месте: %d из %d", left, len(ring))
	clean(t, h)
}

// Дойти до точки за лентами, которые несут по кругу назад (стадия 17 на 859088572: «не дошёл
// до (46.7, -26.8): стою в (48.61, -27.02)», доводка 11.5 с). Раскладка у места — от тайла
// персонажа C (48,-28): на запад (C-1,0) лента на юг, под ней (C-1,+1) — на восток, (C,+1) и
// (C+1,+1) — на север (обратно в C); над ним ряды лент на восток (y C-1 и C-2), делитель на
// восток на (C-2, -1..0); цель — под делителем, (C-2,+1) + (0.7, 0.2).
func TestWalkAgainstBelt(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 3*time.Minute)
	have(t, h, "transport-belt", 16)
	have(t, h, "splitter", 1)
	c := freeArea(t, h, 14)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	at := func(dx, dy int) bridge.Pos { return world.Tile{X: ct.X + dx, Y: ct.Y + dy}.Center() }
	// ряды на восток над персонажем (делитель — вместо лент на x = -2)
	var east []bridge.Pos
	for dx := -4; dx <= 2; dx++ {
		if dx != -2 {
			east = append(east, at(dx, -1), at(dx, -2))
		}
	}
	east = append(east, at(-1, 1))
	if _, err := h.PlaceAll(ctx, "transport-belt", east, act.East); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PlaceAll(ctx, "transport-belt", []bridge.Pos{at(-1, 0)}, act.South); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PlaceAll(ctx, "transport-belt", []bridge.Pos{at(0, 1), at(1, 1)}, act.North); err != nil {
		t.Fatal(err)
	}
	sp := at(-2, -1)
	if _, err := h.Place(ctx, "splitter", bridge.Pos{X: sp.X, Y: sp.Y + 0.5}, act.East); err != nil {
		t.Fatal(err)
	}
	// персонаж — как в игре: (C) + (0.61, 0.98); цель — (C-2, +1) + (0.7, 0.2)
	start := bridge.Pos{X: float64(ct.X) + 0.61, Y: float64(ct.Y) + 0.98}
	if err := h.WalkTo(ctx, start, 0.2); err != nil {
		t.Fatal(err)
	}
	target := bridge.Pos{X: float64(ct.X-2) + 0.7, Y: float64(ct.Y+1) + 0.2}
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	p, _ = h.B.Player()
	if d := math.Hypot(p.Position.X-target.X, p.Position.Y-target.Y); d > 0.5 {
		t.Errorf("до цели осталось %.2f", d)
	}
}

// Встать в проход в 1 тайл между рядами лент (стадия 18 на 794457638: «застрял: не сойти с ленты у
// (79.0, 116.7)» — ряды лент на восток y=116 и y=118, точка стоянки (79.0, 117.0) — на границе
// прохода и ленты: рамка персонажа задевает ленту, лента уносит). Дойти без ошибки и стоять
// (лента не несёт).
func TestStandBetweenBelts(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 3*time.Minute)
	have(t, h, "transport-belt", 16)
	c := freeArea(t, h, 14)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	var rows []bridge.Pos
	for dx := -4; dx <= 3; dx++ {
		rows = append(rows, world.Tile{X: ct.X + dx, Y: ct.Y - 1}.Center(), world.Tile{X: ct.X + dx, Y: ct.Y + 1}.Center())
	}
	if _, err := h.PlaceAll(ctx, "transport-belt", rows, act.East); err != nil {
		t.Fatal(err)
	}
	// персонаж — как в игре: на 4–5 тайлов восточнее, у прохода; цель — на границе прохода и
	// верхней ленты
	if err := h.WalkTo(ctx, bridge.Pos{X: float64(ct.X) + 5.7, Y: float64(ct.Y) - 0.3}, 0.3); err != nil {
		t.Fatal(err)
	}
	target := bridge.Pos{X: float64(ct.X), Y: float64(ct.Y)}
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	time.Sleep(2 * time.Second)
	p1, _ := h.B.Player()
	if d := math.Hypot(p1.Position.X-p0.Position.X, p1.Position.Y-p0.Position.Y); d > 0.05 {
		t.Errorf("персонажа несёт: (%.2f, %.2f) → (%.2f, %.2f)", p0.Position.X, p0.Position.Y, p1.Position.X, p1.Position.Y)
	}
	if d := math.Hypot(p1.Position.X-target.X, p1.Position.Y-target.Y); d > 1 {
		t.Errorf("стою в %.2f от цели", d)
	}
}

// Выход из ловушки не теряет содержимое (стадия 2 на 859088572: «stone-furnace точки iron-ore
// без топлива» — печь подняли, чтобы выйти, поставили назад пустой): в каждом сундуке кольца —
// уголь; после выхода у каждого сундука на месте — его уголь.
func TestUnstuckKeepsContents(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 5*time.Minute)
	have(t, h, "wooden-chest", 8)
	have(t, h, "coal", 8)
	c := freeArea(t, h, 2*(8+2)+1)
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
	es, err := h.PlaceAll(ctx, "wooden-chest", ring, act.North)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if err := h.Insert(ctx, e, "coal", 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	if p, _ = h.B.Player(); !h.World.Enclosed(p.Position) {
		t.Fatalf("кольцо не замкнуто: персонаж в (%.2f, %.2f)", p.Position.X, p.Position.Y)
	}
	if err := h.WalkTo(ctx, bridge.Pos{X: ct.Center().X + 8, Y: ct.Center().Y}, 0.5); err != nil {
		t.Fatal(err)
	}
	for _, pos := range ring {
		st, err := h.B.GetEntity("wooden-chest", pos)
		if err != nil {
			t.Fatal(err)
		}
		if st == nil {
			t.Errorf("сундука в (%.1f, %.1f) нет", pos.X, pos.Y)
		} else if n := st.Count("coal"); n != 1 {
			t.Errorf("в сундуке (%.1f, %.1f) угля %d, ждали 1", pos.X, pos.Y, n)
		}
	}
}

// Путь A* в обход ближайшего к игроку водоёма (не меньше 100 тайлов, открыт целиком): точки по разные
// стороны воды, путь с поворотами, дойти по нему.
func TestPathAroundLake(t *testing.T) {
	h := handsT(t)
	w := h.World
	if _, err := w.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p0, _ := h.B.Player()
	// водоёмы — связные (4 соседа) области воды
	water := map[world.Tile]bool{}
	for _, c := range w.Chunks {
		for i := range c.Tiles {
			if c.Tiles[i] == world.Water {
				water[world.Tile{X: c.Pos.X*world.ChunkSize + i%world.ChunkSize, Y: c.Pos.Y*world.ChunkSize + i/world.ChunkSize}] = true
			}
		}
	}
	seen := map[world.Tile]bool{}
	best, bestD := []world.Tile(nil), math.Inf(1)
	for t0 := range water {
		if seen[t0] {
			continue
		}
		lake, queue := []world.Tile{}, []world.Tile{t0}
		seen[t0] = true
		near, open := math.Inf(1), false
		for len(queue) > 0 {
			q := queue[0]
			queue = queue[1:]
			lake = append(lake, q)
			near = math.Min(near, math.Hypot(float64(q.X)-p0.Position.X, float64(q.Y)-p0.Position.Y))
			for _, d := range []world.Tile{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
				n := world.Tile{X: q.X + d.X, Y: q.Y + d.Y}
				if water[n] && !seen[n] {
					seen[n] = true
					queue = append(queue, n)
				}
				if w.TileAt(n) == world.Unknown {
					open = true // уходит за край открытой карты — берега не видно
				}
			}
		}
		if len(lake) >= 100 && !open && near < bestD {
			best, bestD = lake, near
		}
	}
	if best == nil {
		t.Skip("нет водоёма")
	}
	// крайние тайлы воды слева и справа — точки на берегу за ними
	wt, et := best[0], best[0]
	for _, q := range best {
		if q.X < wt.X {
			wt = q
		}
		if q.X > et.X {
			et = q
		}
	}
	t.Logf("водоём: %d тайлов, ближе всего %.0f тайлов, X %d…%d", len(best), bestD, wt.X, et.X)
	west, okW := w.FreeSpot(bridge.Pos{X: float64(wt.X) - 3, Y: float64(wt.Y) + 0.5}, 1, 1, 10, nil)
	east, okE := w.FreeSpot(bridge.Pos{X: float64(et.X) + 4, Y: float64(et.Y) + 0.5}, 1, 1, 10, nil)
	if !okW || !okE {
		t.Skip("нет проходимых точек у озера")
	}
	pts, err := w.Path(west, east, 0.5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) < 2 {
		t.Errorf("путь через озеро без поворотов: %v", pts)
	}
	ctx := ctxT(t, 3*time.Minute)
	for _, target := range []bridge.Pos{west, east} {
		if err := h.WalkTo(ctx, target, 0.5); err != nil {
			t.Fatal(err)
		}
		p, _ := h.B.Player()
		if d := math.Hypot(p.Position.X-target.X, p.Position.Y-target.Y); d > 0.5 {
			t.Errorf("до %v осталось %.2f", target, d)
		}
	}
	clean(t, h)
}

// Разведка: идти к краю открытой карты, пока не откроется ещё 10 чанков.
func TestExplore(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	start := len(h.World.Chunks)
	err := h.Explore(ctxT(t, 5*time.Minute), func() bool { return len(h.World.Chunks) >= start+10 })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("открыто чанков: %d → %d", start, len(h.World.Chunks))
	clean(t, h)
}

// Разведка области (стадия 3: квадрат вокруг старта): обойти квадрат 24×24 чанка вокруг
// игрока — закончить (открыто всё или недоступное пропущено), а не ходить кругами у
// недоступного (бот бродил вдоль воды, цели за ней не пропускал).
func TestExploreArea(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	c0 := world.ChunkOf(world.TileOf(p.Position))
	const half = 12
	inside := func(c bridge.ChunkPos) bool {
		return c.X >= c0.X-half && c.X < c0.X+half && c.Y >= c0.Y-half && c.Y < c0.Y+half
	}
	count := func() int {
		n := 0
		for c := range h.World.Chunks {
			if inside(c) {
				n++
			}
		}
		return n
	}
	start := count()
	err := h.ExploreWhere(ctxT(t, 20*time.Minute), inside, func() bool { return count() == 4*half*half })
	if err != nil && !errors.Is(err, act.ErrNothingToExplore) {
		t.Fatalf("разведка области: %v (открыто %d → %d из %d)", err, start, count(), 4*half*half)
	}
	t.Logf("открыто чанков области: %d → %d из %d", start, count(), 4*half*half)
	clean(t, h)
}

// Сквозь густой лес (стадия 12 на 859088572: «застрял у (17.3, 202.4)» среди деревьев,
// валунов и сухих стволов на прямом доходе к цели за 280 тайлов): самое густое место леса
// в 200 тайлах от игрока — дойти до точки за ним на 40 тайлов дальше.
func TestWalkDenseForest(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	const cell = 12
	count := map[[2]int]int{}
	for _, c := range h.World.Chunks {
		for _, o := range c.Objects {
			if o.Type == "tree" && dist(o.Pos, p.Position) < 200 {
				count[[2]int{int(math.Floor(o.Pos.X / cell)), int(math.Floor(o.Pos.Y / cell))}]++
			}
		}
	}
	best, bn := [2]int{}, 0
	for k, n := range count {
		if n > bn || n == bn && (k[0] < best[0] || k[0] == best[0] && k[1] < best[1]) {
			best, bn = k, n
		}
	}
	if bn < 40 {
		t.Skipf("густого леса рядом нет (самое густое — %d деревьев на %d×%d)", bn, cell, cell)
	}
	mid := bridge.Pos{X: float64(best[0]*cell + cell/2), Y: float64(best[1]*cell + cell/2)}
	d := dist(mid, p.Position)
	if d < 1 {
		d = 1
	}
	target := bridge.Pos{X: mid.X + (mid.X-p.Position.X)/d*40, Y: mid.Y + (mid.Y-p.Position.Y)/d*40}
	t.Logf("лес: %d деревьев у (%.0f, %.0f), цель (%.1f, %.1f)", bn, mid.X, mid.Y, target.X, target.Y)
	ctx := ctxT(t, 4*time.Minute)
	if err := h.WalkTo(ctx, target, 1); err != nil {
		if errors.Is(err, act.ErrNotReached) {
			t.Skipf("цель недостижима: %v", err)
		}
		t.Fatal(err)
	}
}
