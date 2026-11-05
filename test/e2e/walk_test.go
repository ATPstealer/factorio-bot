//go:build e2e

// Ходьба: до точки, в обход воды, из кольца своих построек.
package e2e

import (
	"errors"
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
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
	// цель — ближайшая к (+6, +3), где можно стоять (на водной карте точка бывала в воде)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	far, ok := spot(h, bridge.Pos{X: start.Position.X + 6, Y: start.Position.Y + 3}, 1, 1, 10)
	if !ok {
		t.Skip("рядом нет, где стоять")
	}
	for _, target := range []bridge.Pos{far, start.Position} {
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

// Выход из кармана станции (стадия 43 на 427965634: уголь в новые бойлеры power-3 — «не дошёл
// до (361.0, 784.1): пути нет, стою в (344.37, 782.79)»): персонаж в тайле C между двумя
// паровыми двигателями 3×5 (центры C + (−1.5, −0.5) и C + (2.5, −0.5)), сверху в тайле над ним
// — средний столб (C + (0.5, −0.5)), снизу — бойлеры 3×2 (C + (−1.5, 3), C + (2.5, 3)) и труба
// между ними (C + (0.5, 3.5)). Прорыв поднимал двигатель и ставил назад, не выйдя: точка «за
// поднятым» в 2.5 тайла от центра двигателя — ещё внутри него. Дальше одна команда — «иди
// туда» за двигатели; всё поднятое — назад на место.
func TestUnstuckBetweenEngines(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 8*time.Minute)
	have(t, h, "steam-engine", 2)
	have(t, h, "boiler", 2)
	have(t, h, "pipe", 1)
	have(t, h, "medium-electric-pole", 1)
	c := freeArea(t, h, 2*(8+5)+1)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	at := func(dx, dy float64) bridge.Pos { return bridge.Pos{X: float64(ct.X) + dx, Y: float64(ct.Y) + dy} }
	type part struct {
		item string
		pos  []bridge.Pos
	}
	parts := []part{
		{"steam-engine", []bridge.Pos{at(-1.5, -0.5), at(2.5, -0.5)}},
		{"boiler", []bridge.Pos{at(-1.5, 3), at(2.5, 3)}},
		{"pipe", []bridge.Pos{at(0.5, 3.5)}},
		{"medium-electric-pole", []bridge.Pos{at(0.5, -0.5)}},
	}
	for _, pt := range parts {
		if _, err := h.PlaceAll(ctx, pt.item, pt.pos, act.North); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	if p, _ = h.B.Player(); !h.World.Enclosed(p.Position) {
		t.Fatalf("карман не замкнут: персонаж в (%.2f, %.2f)", p.Position.X, p.Position.Y)
	}
	target := at(8.5, 0.5)
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	p, _ = h.B.Player()
	if d := math.Hypot(p.Position.X-target.X, p.Position.Y-target.Y); d > 0.5 {
		t.Errorf("до цели осталось %.2f", d)
	}
	for _, pt := range parts {
		for _, pos := range pt.pos {
			if e, _ := h.B.EntityAt(pt.item, pos); e == nil {
				t.Errorf("%s в (%.1f, %.1f) не вернулся на место", pt.item, pos.X, pos.Y)
			}
		}
	}
	clean(t, h)
}

// Выход из кольца с полным инвентарём (стадия 46 на 267145981: персонаж среди полных сундуков
// буферов, инвентарь полон после подъёма лент с чужим, до хранения не дойти — ни одну постройку
// не поднять, по кругу на каждом дереве расчистки). Инвентарь наполняется из сундуков базы
// (нечем — пропуск), в запасе один сундук; кольцо из 8 сундуков; «иди туда» за кольцо. Бот
// должен освободить ячейку на месте (запасной сундук рядом или стопка на землю) и выйти.
func TestUnstuckFullInventory(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 10*time.Minute)
	// кольцо из столбов (дешёвые: 58 = полная стопка 50 + 8 на кольцо): после кольца стопка
	// полна, поднятому столбу места в ней нет — нужна новая ячейка (из ровно 8 ячейка
	// освобождалась сама); 464 пластин на 58 сундуков у тестов нет
	const ringItem = "small-electric-pole"
	have(t, h, ringItem, 58)
	have(t, h, "steel-chest", 2)  // запасной сундук для freeSlot и «кладовка» рядом с кольцом
	have(t, h, "wooden-chest", 1) // в кладовку: заберём после кольца — займёт ячейку, которую кольцо освободило
	full := func() bool {
		q, err := h.B.Player()
		return err == nil && q.InventorySize-len(q.InventorySlots) == 0
	}
	c := freeArea(t, h, 2*(8+2)+1)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	// кладовка — в досягаемости из кольца, не на его месте
	pantryPos, ok := spot(h, world.Tile{X: ct.X + 4, Y: ct.Y}.Center(), 1, 1, 3)
	if !ok || (abs(world.TileOf(pantryPos).X-ct.X) <= 2 && abs(world.TileOf(pantryPos).Y-ct.Y) <= 2) {
		t.Skip("нет места под кладовку")
	}
	pantry, err := h.Place(ctx, "steel-chest", pantryPos, act.North)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.InsertAll(ctx, pantry, "wooden-chest"); err != nil {
		t.Fatal(err)
	}
	// инвентарь — доверху из сундуков базы
	chests, err := h.B.FindNearest(bridge.FindArgs{Type: "container", Radius: 400, Limit: 60, Position: &p.Position})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range chests {
		if full() {
			break
		}
		if e.Position == pantry.Position {
			continue
		}
		if st, err := h.B.GetEntity(e.Name, e.Position); err != nil || st == nil || !st.Own {
			continue
		}
		if _, err := h.Take(ctx, e); err != nil {
			t.Logf("забрать из %s: %v", e.Name, err)
		}
	}
	if !full() {
		t.Skip("инвентарь не наполнить: мало сундуков с содержимым рядом")
	}
	if err := h.WalkTo(ctx, ct.Center(), 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ = h.B.Player()
	// лишние столбы (попали из сундуков базы) — рядом, до кольца: после кольца стопка должна
	// быть ровно полной
	if extra := (p.Inventory[ringItem] - 8) % 50; extra > 0 {
		if extra > 24 {
			t.Skipf("лишних столбов %d — много", extra)
		}
		var line []bridge.Pos
		used := map[world.Tile]bool{}
		for i := 0; len(line) < extra && i < 200; i++ {
			near := bridge.Pos{X: float64(ct.X-14) + float64(i%28), Y: float64(ct.Y-8) - float64(i/28)*2}
			pos, ok := spot(h, near, 1, 1, 4)
			if t := world.TileOf(pos); ok && !used[t] && (abs(t.X-ct.X) > 2 || abs(t.Y-ct.Y) > 2) {
				used[t] = true
				line = append(line, pos)
			}
		}
		if len(line) < extra {
			t.Skipf("нет места под %d лишних столбов", extra)
		}
		if _, err := h.PlaceAll(ctx, ringItem, line, act.North); err != nil {
			t.Fatal(err)
		}
		if err := h.WalkTo(ctx, ct.Center(), 0.2); err != nil {
			t.Fatal(err)
		}
	}
	var ring []bridge.Pos
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx != 0 || dy != 0 {
				ring = append(ring, world.Tile{X: ct.X + dx, Y: ct.Y + dy}.Center())
			}
		}
	}
	es, err := h.PlaceAll(ctx, ringItem, ring, act.North)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ = h.B.Player(); p.Inventory[ringItem]%50 != 0 || len(es) == 0 {
		t.Skipf("столбов в инвентаре %d — стопка не полна, поднятый ляжет в неё", p.Inventory[ringItem])
	}
	// кольцо освободило ячейку неполной стопки столбов — занять её деревянным сундуком из кладовки
	if p.Inventory["wooden-chest"] > 0 {
		t.Skip("в инвентаре уже есть деревянные сундуки — ячейка не займётся")
	}
	if _, err := h.Take(ctx, pantry); err != nil {
		t.Fatal(err)
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	if p, _ = h.B.Player(); !h.World.Enclosed(p.Position) {
		t.Fatalf("кольцо не замкнуто: персонаж в (%.2f, %.2f)", p.Position.X, p.Position.Y)
	}
	if !full() {
		t.Fatalf("инвентарь после кольца не полон")
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
		if e, _ := h.B.EntityAt(ringItem, pos); e != nil {
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
	have(t, h, "splitter", 1) // до лент: крафт делителя ест ленты
	have(t, h, "transport-belt", 16)
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

// Сойти с ленты шины, когда по обе стороны — подземки (стадия 49 на 1098794301, дважды: «не
// сойти с ленты» — бот шёл прямо к ближней свободной точке, упирался в подземку, лента уносила):
// персонаж на паре лент на восток, над и под ней — ряды подземок с проходом только в одном
// месте; цель — к югу за подземками: дошёл, не на ленте.
func TestStepOffBeltAmongUndergrounds(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 4*time.Minute)
	have(t, h, "transport-belt", 40)
	have(t, h, "underground-belt", 24)
	c := freeArea(t, h, 20)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	var belts, north, south []bridge.Pos
	for dx := -6; dx <= 9; dx++ {
		belts = append(belts, world.Tile{X: ct.X + dx, Y: ct.Y}.Center(), world.Tile{X: ct.X + dx, Y: ct.Y + 1}.Center())
	}
	for dx := -3; dx <= 8; dx++ {
		if dx == 5 { // проход — в одном месте
			continue
		}
		north = append(north, world.Tile{X: ct.X + dx, Y: ct.Y - 1}.Center())
		south = append(south, world.Tile{X: ct.X + dx, Y: ct.Y + 2}.Center())
	}
	dirs := func(n, d int) []int {
		r := make([]int, n)
		for i := range r {
			r[i] = d
		}
		return r
	}
	// подземки — сначала (ставятся вокруг персонажа), ленты — под ним
	if _, err := h.PlaceDirs(ctx, "underground-belt", north, dirs(len(north), layout.S)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PlaceDirs(ctx, "underground-belt", south, dirs(len(south), layout.N)); err != nil {
		t.Fatal(err)
	}
	if err := h.WalkTo(ctx, world.Tile{X: ct.X, Y: ct.Y}.Center(), 0.2); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PlaceAll(ctx, "transport-belt", belts, act.East); err != nil {
		t.Fatal(err)
	}
	if q, _ := h.B.Player(); !h.World.TouchesBelt(q.Position) {
		t.Skipf("персонаж не на ленте (%.1f, %.1f) — случая нет", q.Position.X, q.Position.Y)
	}
	target := world.Tile{X: ct.X, Y: ct.Y + 6}.Center()
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatalf("к (%.1f, %.1f) с ленты: %v", target.X, target.Y, err)
	}
	q, _ := h.B.Player()
	if h.World.TouchesBelt(q.Position) {
		t.Errorf("персонаж на ленте: (%.2f, %.2f)", q.Position.X, q.Position.Y)
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

// Выход из ловушки не теряет рецепт сборщика (стадия 59 на 2448886157: сборщик поднят из кольца,
// поставлен назад без рецепта — «вернуть advanced-circuit: положено ×0 из 2»): кольцо — сборщик
// с рецептом шестерни и пластинами внутри (восточная сторона) и 5 сундуков; после выхода сборщик
// на месте — с рецептом и пластинами.
func TestUnstuckKeepsRecipe(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 6*time.Minute)
	have(t, h, "wooden-chest", 5)
	have(t, h, "assembling-machine-1", 1)
	have(t, h, "iron-plate", 10)
	c := freeArea(t, h, 2*(8+2)+1)
	if err := h.WalkTo(ctx, c, 0.2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	ct := world.TileOf(p.Position)
	am, err := h.Place(ctx, "assembling-machine-1", world.Tile{X: ct.X + 2, Y: ct.Y}.Center(), act.North)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetRecipe(ctx, am, "iron-gear-wheel"); err != nil {
		t.Fatal(err)
	}
	if err := h.Insert(ctx, am, "iron-plate", 10); err != nil {
		t.Fatal(err)
	}
	var ring []bridge.Pos
	for _, d := range [][2]int{{-1, -1}, {0, -1}, {-1, 0}, {-1, 1}, {0, 1}} {
		ring = append(ring, world.Tile{X: ct.X + d[0], Y: ct.Y + d[1]}.Center())
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
	if err := h.WalkTo(ctx, bridge.Pos{X: ct.Center().X - 8, Y: ct.Center().Y}, 0.5); err != nil {
		t.Fatal(err)
	}
	st, err := h.B.GetEntity(am.Name, am.Position)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatalf("сборщика в (%.1f, %.1f) нет", am.Position.X, am.Position.Y)
	}
	if st.Recipe != "iron-gear-wheel" {
		t.Errorf("рецепт %q, ждали iron-gear-wheel", st.Recipe)
	}
	if n := st.Count("iron-plate"); n != 10 {
		t.Errorf("пластин в сборщике %d, ждали 10", n)
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

// Рубка подряд в густом лесу (стадия 1 на 1067809293, карта с максимумом леса: «персонаж не
// добывает: tree-08» — подход застрял в чаще, дерево вне досягаемости): у края самого густого
// места леса в 200 тайлах — срубить 20 деревьев подряд, как стадия 1 (Gather по одному).
func TestGatherDenseForest(t *testing.T) {
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
	t.Logf("лес: %d деревьев у (%.0f, %.0f)", bn, mid.X, mid.Y)
	ctx := ctxT(t, 6*time.Minute)
	if err := h.WalkTo(ctx, mid, 3); err != nil && !errors.Is(err, act.ErrNotReached) {
		t.Fatal(err)
	}
	w0 := 0
	if q, err := h.B.Player(); err == nil {
		w0 = q.Inventory["wood"]
	}
	for i := 0; i < 20; i++ {
		if _, err := h.Gather(ctx, "tree", 1, 30); err != nil {
			t.Fatalf("дерево %d: %v", i+1, err)
		}
	}
	if q, err := h.B.Player(); err == nil {
		t.Logf("дерева: %d → %d", w0, q.Inventory["wood"])
	}
}

// Цель в полутора тайлах за рядом своих сундуков: по прямой — в сундук, нужно обойти ряд (стадия
// 22 на 1114315936: расширение хранения — встать над рядом хранения, стоя под ним; доводка
// упиралась в сундук трижды по 9 с — «не дошёл до (143.5, 423.7): стою в (143.58, 425.05)»).
func TestWalkShortBehindChests(t *testing.T) {
	h := handsT(t)
	have(t, h, "wooden-chest", 3)
	at := freeArea(t, h, 6)
	x, y := math.Floor(at.X)+0.5, math.Floor(at.Y)+0.5
	row := []bridge.Pos{{X: x - 1, Y: y}, {X: x, Y: y}, {X: x + 1, Y: y}}
	ctx := ctxT(t, 3*time.Minute)
	if _, err := h.PlaceDirs(ctx, "wooden-chest", row, []int{act.North, act.North, act.North}); err != nil {
		t.Fatal(err)
	}
	below := bridge.Pos{X: x + 0.08, Y: y + 0.55} // как в игре: вплотную под средним сундуком
	if err := h.WalkTo(ctx, below, 0.2); err != nil {
		t.Fatal(err)
	}
	above := bridge.Pos{X: x, Y: y - 1.3}
	if err := h.WalkTo(ctx, above, 0.3); err != nil {
		t.Fatalf("за ряд сундуков: %v", err)
	}
	if p, _ := h.B.Player(); dist(p.Position, above) > 0.6 {
		t.Errorf("стою в (%.2f, %.2f), цель (%.2f, %.2f)", p.Position.X, p.Position.Y, above.X, above.Y)
	}
}
