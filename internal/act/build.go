package act

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

// Направления 2.0 (16 румбов): постройки поворачиваются по 4.
const (
	North = 0
	East  = 4
	South = 8
	West  = 12
)

// ErrPlace — постройка не встала (место занято, далеко, не та клетка).
var ErrPlace = errors.New("act: не удалось поставить")

// ErrOccupied — место постройки занято своей постройкой (её не трогаем: может быть нужной).
var ErrOccupied = errors.New("act: место занято своей постройкой")

// footprint — рамка коллизии постройки item в точке pos с направлением dir (восток/запад —
// поперёк: двигатель 3×5 → 5×3).
func (h *Hands) footprint(entity string, pos bridge.Pos, dir int) bridge.Box {
	b := bridge.Box{LeftTop: bridge.Pos{X: -0.5, Y: -0.5}, RightBottom: bridge.Pos{X: 0.5, Y: 0.5}}
	if e, ok := h.Protos.Entities[entity]; ok {
		b = e.CollisionBox
	}
	b = world.RotateBox(b, dir)
	return bridge.Box{
		LeftTop:     bridge.Pos{X: pos.X + b.LeftTop.X, Y: pos.Y + b.LeftTop.Y},
		RightBottom: bridge.Pos{X: pos.X + b.RightBottom.X, Y: pos.Y + b.RightBottom.Y},
	}
}

// clearSpots — убрать, что мешает постройке entity: деревья и камни под рамкой — срубить и
// разбить. Своя постройка на месте — ErrOccupied (может быть нужной — не трогать; ленты,
// подземки и делители друг друга заменяют сами); своя такая же, стоящая не так, — поднять.
func (h *Hands) clearSpots(ctx context.Context, entity string, fps []bridge.Box) error {
	if h.World == nil {
		return nil
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	seen := map[bridge.Pos]bool{}
	for _, fp := range fps {
		c := bridge.Pos{X: (fp.LeftTop.X + fp.RightBottom.X) / 2, Y: (fp.LeftTop.Y + fp.RightBottom.Y) / 2}
		// + 9: центр большого объекта далеко от места (корабль крушения — 16×8 тайлов: «Spaceship
		// is in the way», стадия 28 на 1028292478); пересечение — ниже, по рамке
		r := math.Max(fp.RightBottom.X-fp.LeftTop.X, fp.RightBottom.Y-fp.LeftTop.Y)/2 + 9
		for _, o := range h.World.ObjectsNear(c, r) {
			rock := o.Type == "simple-entity" && strings.Contains(o.Name, "rock")
			// обломки корабля — руками; сила у них игрока (Own), но это не постройки бота
			wreck := strings.HasPrefix(o.Name, "crash-site-")
			// своё на месте — помеха, кроме быстрой замены: ленты друг друга, сундук сундуком
			// (железный поверх деревянного — содержимое остаётся, стадия 22)
			pt := h.Protos.Entities[entity].Type
			own := o.Own && !wreck && o.Type != "character" && !(beltLike(o.Type) && beltLike(pt)) && !(o.Type == "container" && pt == "container") &&
				!(pipeLike(o.Type) && pipeLike(pt)) && // труба и подземная — одна группа замены (стадия 50 на 427965634)
				!TierUp(h.Protos, o.Name, entity)
			// подземка другого уровня: поверх встаёт, только если игра заменит и пару (в руке
			// хватает на обе); один конец уже быстрый — «Underground belt is in the way» (стадия 42
			// на 1098794301) — снять и поставить новую: в пару она встанет сама
			ugTier := o.Own && o.Type == "underground-belt" && pt == "underground-belt" && o.Name != entity
			if o.Type != "tree" && !rock && !wreck && !own && !ugTier || seen[o.Pos] {
				continue
			}
			b := h.World.CollisionBox(o)
			if b.LeftTop.X >= fp.RightBottom.X || b.RightBottom.X <= fp.LeftTop.X || b.LeftTop.Y >= fp.RightBottom.Y || b.RightBottom.Y <= fp.LeftTop.Y {
				continue
			}
			seen[o.Pos] = true
			e, err := h.B.EntityAt(o.Name, o.Pos)
			if err != nil {
				return err
			}
			if e == nil {
				continue
			}
			if own && o.Name == entity && !sameBox(b, fp) {
				// своя такая же, но стоит не так (поперёк, со сдвигом — неверный поворот руки или
				// сбитый клик: стадия 29 на 427965634) — поднять и поставить как надо; точно на
				// месте — не трогать (поставлено)
				h.Say("%s стоит не так — поднимаю", o.Name)
				if err := h.Pickup(ctx, *e); err != nil {
					return err
				}
				continue
			}
			if ugTier {
				h.Say("%s — другого уровня, снимаю под %s", o.Name, entity)
				if err := h.Pickup(ctx, *e); err != nil {
					return err
				}
				continue
			}
			if own && o.Name != entity {
				return fmt.Errorf("%w: %s в (%.1f, %.1f)", ErrOccupied, o.Name, o.Pos.X, o.Pos.Y)
			}
			if own {
				continue
			}
			h.Say("%s мешает постройке — убираю", o.Name)
			if err := h.WalkToMine(ctx, *e); err != nil && !errors.Is(err, ErrNotReached) {
				return err
			}
			if err := h.Mine(ctx, *e, 20*time.Second); err != nil {
				return err
			}
		}
	}
	return nil
}

// sameBox — рамки a и b совпадают (с допуском на зазор рамки коллизии внутри тайлов).
func sameBox(a, b bridge.Box) bool {
	ac, bc := a.Center(), b.Center()
	aw, ah := a.RightBottom.X-a.LeftTop.X, a.RightBottom.Y-a.LeftTop.Y
	bw, bh := b.RightBottom.X-b.LeftTop.X, b.RightBottom.Y-b.LeftTop.Y
	return dist(ac, bc) < 0.25 && math.Abs(aw-bw) < 0.5 && math.Abs(ah-bh) < 0.5
}

func beltLike(typ string) bool {
	return typ == "transport-belt" || typ == "underground-belt" || typ == "splitter"
}

// pipeLike — труба или подземная труба: заменяют друг друга на месте (fast_replaceable_group
// "pipe" в данных игры).
func pipeLike(typ string) bool { return typ == "pipe" || typ == "pipe-to-ground" }

// TierUp — new ставится поверх old на месте (stages.md, стадия 50): печь поверх печи того же
// размера, сборщик поверх сборщика (одна группа замены в данных игры: старая — в инвентарь,
// рецепт и содержимое остаются; химзавод — тоже assembling-machine, но не из их группы); быстрый
// инсертер поверх обычного (инсертер пары по плану — быстрый, stages.md, общее).
// Только вверх: машина — быстрее старой (каменная поверх стальной — нет: чертёж стадии 42 на
// 366255770 брал из раскладки прошлой стадии каменную печь вместо стальной плана — обслуживание
// ставило каменные назад).
func TierUp(p *knowledge.Prototypes, old, new string) bool {
	o, n := p.Entities[old], p.Entities[new]
	if old == new || o.Type != n.Type || o.TileWidth != n.TileWidth || o.TileHeight != n.TileHeight {
		return false
	}
	asm := func(s string) bool { return strings.HasPrefix(s, "assembling-machine-") }
	return (o.Type == "furnace" || asm(old) && asm(new)) && n.CraftingSpeed > o.CraftingSpeed ||
		old == "inserter" && new == "fast-inserter"
}

func inBox(p bridge.Pos, b bridge.Box, margin float64) bool {
	return p.X > b.LeftTop.X-margin && p.X < b.RightBottom.X+margin && p.Y > b.LeftTop.Y-margin && p.Y < b.RightBottom.Y+margin
}

// buildReach — с какого расстояния ставим и кладём не сходя с места (досягаемость
// персонажа 10, с запасом). Важно, например, когда персонаж строит вокруг себя.
const buildReach = 9

// standNear — подойти к месту постройки: на расстояние досягаемости, но не на саму
// площадку (персонаж мешает поставить) и не на площадки avoid (ещё не поставленное той же
// стройкой: обставит себя ими и замурует — сплошная сетка цистерн, стадия 33 на 267145981).
// Точка — ближайшая к персонажу свободная вокруг площадки (рядом могут стоять другие постройки).
func (h *Hands) standNear(ctx context.Context, fp bridge.Box, avoid []bridge.Box) error {
	return h.standWithin(ctx, fp, buildReach, avoid)
}

// onSpot — персонаж ближе этого к рамке коллизии постройки — стоит на её месте (игра не
// поставит): рамка персонажа ±0.2 (место смотрят, когда персонаж стоит). Было 0.35 — столб в
// 0.43 от персонажа (до рамки 0.28, игре не мешает) считался «на месте», бот уходил, а уйти в
// тесноте труб у НПЗ некуда — «не вернул» (стадия 63 на 778631482); 0.25 — и в 0.38
// (TestPlaceBesideSelf на 778631482-0).
const onSpot = 0.21

// inAny — p в одной из рамок bs (с запасом margin).
func inAny(p bridge.Pos, bs []bridge.Box, margin float64) bool {
	return slices.ContainsFunc(bs, func(b bridge.Box) bool { return inBox(p, b, margin) })
}

// pocket — из q не выйти (World.Pocket), считая стоящим и ещё не поставленное этим же вызовом
// (planned): встал между будущими постройками — поставит их вокруг себя и замурует (стадия 36 на
// 778631482: щель в тайл между НПЗ, трубы по обе стороны — пролом туда-обратно, «не дошёл»).
// Кроме пролома: поднятое возвращается, стоя за ним, — внутри так и задумано (иначе бот уходил
// из кармана, куда только что прошёл, — TestPlaceNotWallingSelfIn).
func (h *Hands) pocket(q bridge.Pos, planned []bridge.Box) bool {
	if h.breaching {
		planned = nil
	}
	// дальние Pocket не смотрит (пути на 12 тайлов), а трасса бывает в тысячу построек
	var objs []world.Object
	for i := range planned {
		if b := planned[i]; dist(b.Center(), q) < 20 {
			objs = append(objs, world.Object{Name: "planned", Pos: b.Center(), Own: true, Box: &b})
		}
	}
	res := false
	h.World.With(objs, func() { res = h.World.Pocket(q) })
	return res
}

// walls — поставит planned — из q не выйти (pocket): отсюда не ставить. И когда по модели уже не
// выйти: щель в 0.5 тайла у столба в игре проходима, в модели — нет (TestPlaceNotWallingSelfIn:
// вошёл в щель мимо столба, ставил трубу в торец изнутри).
func (h *Hands) walls(q bridge.Pos, planned []bridge.Box) bool {
	return h.World != nil && !h.breaching && h.pocket(q, planned)
}

// standWithin — standNear, но ближе reach к центру площадки.
func (h *Hands) standWithin(ctx context.Context, fp bridge.Box, reach float64, avoid []bridge.Box) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	c := fp.Center()
	// и цель на экране кликабельна — не под панелью (клик в быструю панель брал из неё ленты в
	// руку вместо подземки — TestPlaceUndergroundAfterTurn, «не встало: в руке transport-belt»)
	from := func(q bridge.Player, reach float64) bool {
		return !inBox(q.Position, fp, onSpot) && !inAny(q.Position, avoid, onSpot) && dist(q.Position, c) < reach && h.clickable(q, q.Position, c)
	}
	planned := append(slices.Clone(avoid), fp)
	// стоит там, откуда с этой стройкой не выйти, — отойти (walls)
	if from(p, reach) && !h.walls(p.Position, planned) {
		return nil
	}
	spot := bridge.Pos{X: c.X, Y: fp.RightBottom.Y + 1.5}
	// место в закрытом кармане своих построек (дойти — только проломом: стадия 63 на 24996062) или
	// в кармане, который закроет сама эта стройка (fp и avoid), — другое; все такие — первое из них
	pocket := map[bridge.Pos]bool{}
	var first *bridge.Pos
	for try := 0; h.World != nil && try < 4; try++ {
		spot = bridge.Pos{X: c.X, Y: fp.RightBottom.Y + 1.5}
		// сначала — не у ленты и с запасом вокруг (коридор в тайл между рядами лент шины: доводка
		// с инерцией роняла на ленту — «не сойти с ленты», стадия 22 на 1028292478), дальше — до
		// досягаемости (до 6 тайлов не нашлось — встал в щель между рядами, стадия 56 на
		// 2448886157); потом — с запасом, хоть и у ленты; нет такого — любое, где можно стоять
		best := math.Inf(1)
		levels := []struct {
			ok   func(bridge.Pos) bool
			maxR float64
		}{
			{func(q bridge.Pos) bool { return !h.nearBelt(q) && h.World.Roomy(q) }, 8},
			{h.World.Roomy, 6},
			{func(bridge.Pos) bool { return true }, 4},
		}
		for li, lv := range levels {
			for _, r := range []float64{1.5, 2.5, 4, 6, 7.5} {
				if r > lv.maxR {
					break
				}
				for k := 0; k < 16; k++ {
					a := float64(k) * math.Pi / 8
					q := bridge.Pos{X: c.X + (r+(fp.RightBottom.X-fp.LeftTop.X)/2)*math.Cos(a), Y: c.Y + (r+(fp.RightBottom.Y-fp.LeftTop.Y)/2)*math.Sin(a)}
					if pocket[q] || dist(q, c) > reach-0.5 || inBox(q, fp, 0.8) || inAny(q, avoid, 0.8) || !h.World.Standable(q) || !h.clickable(p, q, c) || !lv.ok(q) {
						continue
					}
					if d := dist(p.Position, q); d < best {
						best, spot = d, q
					}
				}
				if !math.IsInf(best, 1) {
					break
				}
			}
			// не у ленты и просторно — узкое место (столбец в тайл между лентами) кольца с шагом π/8
			// пропускают: сеткой (стадия 59 на 778631482: такое было в 6 тайлах, встал в карман)
			if math.IsInf(best, 1) && li == 0 {
				if q, ok := h.fineStand(p, c, fp, avoid, reach, pocket); ok {
					best, spot = 0, q
				}
			}
			if !math.IsInf(best, 1) {
				break
			}
		}
		if math.IsInf(best, 1) {
			if first != nil { // кроме карманов — ничего
				spot = *first
			}
			break
		}
		if !h.pocket(spot, planned) {
			break
		}
		h.Log("место стоять (%.1f, %.1f) — в закрытом кармане, другое", spot.X, spot.Y)
		pocket[spot] = true
		if first == nil {
			first = &bridge.Pos{X: spot.X, Y: spot.Y}
		}
	}
	if first != nil && pocket[spot] {
		spot = *first
	}
	h.standOK = func(q bridge.Player) bool { return from(q, reach) } // встал не у ленты там, откуда ставится, — дошёл
	err = h.WalkTo(ctx, spot, 0.5)
	h.standOK = nil
	if errors.Is(err, ErrNotReached) {
		// не дошёл до точки, но и отсюда ставится — не ошибка
		if q, perr := h.B.Player(); perr == nil && from(q, buildReach) {
			return nil
		}
	}
	return err
}

// fineStand — точка не у ленты и просторная ближе reach-0.5 к c, не на площадках fp и avoid,
// откуда цель кликабельна, — сеткой в четверть тайла от центра тайла; ближняя к персонажу; кроме
// skip (закрытые карманы).
func (h *Hands) fineStand(p bridge.Player, c bridge.Pos, fp bridge.Box, avoid []bridge.Box, reach float64, skip map[bridge.Pos]bool) (bridge.Pos, bool) {
	// ленты вокруг — один раз (nearBelt на каждой точке сетки — тысячи поисков объектов)
	belt := map[world.Tile]bool{}
	for _, o := range h.World.ObjectsNear(c, reach+3) {
		switch o.Type {
		case "transport-belt", "underground-belt":
			belt[world.TileOf(o.Pos)] = true
		case "splitter": // 2 тайла поперёк хода
			dx, dy := 0.5, 0.0
			if o.Direction == East || o.Direction == West {
				dx, dy = 0, 0.5
			}
			belt[world.TileOf(bridge.Pos{X: o.Pos.X - dx, Y: o.Pos.Y - dy})] = true
			belt[world.TileOf(bridge.Pos{X: o.Pos.X + dx, Y: o.Pos.Y + dy})] = true
		}
	}
	near := func(q bridge.Pos) bool { // как nearBelt
		for _, d := range [][2]float64{{1.3, 0}, {-1.3, 0}, {0, 1.3}, {0, -1.3}, {0.9, 0.9}, {-0.9, 0.9}, {0.9, -0.9}, {-0.9, -0.9}} {
			if belt[world.TileOf(bridge.Pos{X: q.X + d[0], Y: q.Y + d[1]})] {
				return true
			}
		}
		return false
	}
	r := reach - 0.5
	c0 := bridge.Pos{X: math.Floor(c.X) + 0.5, Y: math.Floor(c.Y) + 0.5}
	best, spot := math.Inf(1), bridge.Pos{}
	for dy := -math.Ceil(r); dy <= math.Ceil(r); dy += 0.25 {
		for dx := -math.Ceil(r); dx <= math.Ceil(r); dx += 0.25 {
			q := bridge.Pos{X: c0.X + dx, Y: c0.Y + dy}
			d := dist(p.Position, q)
			if d >= best || skip[q] || dist(q, c) > r || inBox(q, fp, 0.8) || inAny(q, avoid, 0.8) || near(q) || !h.World.Roomy(q) || !h.clickable(p, q, c) {
				continue
			}
			best, spot = d, q
		}
	}
	return spot, !math.IsInf(best, 1)
}

// Place ставит постройку из предмета item центром в pos и поворачивает в dir.
func (h *Hands) Place(ctx context.Context, item string, pos bridge.Pos, dir int) (bridge.Entity, error) {
	es, err := h.PlaceAll(ctx, item, []bridge.Pos{pos}, dir)
	if len(es) == 0 || es[0].Name == "" {
		return bridge.Entity{}, err
	}
	return es[0], err
}

// PlaceAll ставит постройки из item в точки positions направлением dir. Из текущего места
// ставит всё, что в досягаемости (ближние раньше); остальное — переходит в точку, откуда
// достаётся больше всего оставшихся (не на ленте, не на площадках). Предмет берётся в руку
// один раз. Подтверждение каждой — событие built_entity в этой точке. Результат выровнен
// по positions (не поставленные — пустые, только при ошибке).
// Направление: с предметом в руке R поворачивает сам предмет (а не постройку под курсором),
// поэтому после первой постройки руку доворачиваем R, а уже поставленные с неверным
// направлением поворачиваем в конце пустой рукой.
func (h *Hands) PlaceAll(ctx context.Context, item string, positions []bridge.Pos, dir int) ([]bridge.Entity, error) {
	dirs := make([]int, len(positions))
	for i := range dirs {
		dirs[i] = dir
	}
	return h.PlaceDirs(ctx, item, positions, dirs)
}

// PlaceDirs — как PlaceAll, но у каждого места своё направление (лента с поворотами — одним
// проходом по трассе): рука доворачивается R между постановками.
func (h *Hands) PlaceDirs(ctx context.Context, item string, positions []bridge.Pos, dirs []int) ([]bridge.Entity, error) {
	return h.placeDirs(ctx, item, positions, dirs, false)
}

// placeDirs — PlaceDirs; again — повтор того, что не встало: к каждому месту вплотную.
func (h *Hands) placeDirs(ctx context.Context, item string, positions []bridge.Pos, dirs []int, again bool) ([]bridge.Entity, error) {
	dir := dirs[0]
	mixed := false
	for _, d := range dirs {
		mixed = mixed || d != dir
	}
	it, ok := h.Protos.Items[item]
	if !ok || it.PlaceResult == "" {
		return nil, fmt.Errorf("act: %s не ставится", item)
	}
	if err := h.guard(ctx); err != nil {
		return nil, err
	}
	if len(positions) == 1 {
		h.Say("ставлю %s в (%.1f, %.1f)%s", it.PlaceResult, positions[0].X, positions[0].Y, dirName(dir))
	} else if mixed {
		h.Say("ставлю %s ×%d по трассе", it.PlaceResult, len(positions))
	} else {
		h.Say("ставлю %s ×%d%s", it.PlaceResult, len(positions), dirName(dir))
	}
	// поворот руки — из памяти (один на все предметы, hand.go; игра сохраняет его и для заново
	// взятого предмета: стадия 13, каждая лента вставала не туда при «взятый смотрит на север»);
	// после каждой постановки сверяется с тем, как встала постройка
	if q, err := h.B.Player(); err == nil && cursorName(q) != item {
		if err := h.ClearCursor(ctx); err != nil {
			return nil, err
		}
	}
	hand := h.hand()
	defer func() { h.setHand(hand) }()
	// held — предмет точно в руке (после удачной постановки игра доливает руку из инвентаря,
	// пока он есть): не спрашивать игру перед каждой постановкой; nodir — у постройки нет
	// направления (сундук, столб: мод не отдаёт его) — руку не крутить (R и с ней поворачивает
	// руку для следующих)
	held, stock, nodir := false, -1, false
	built := make([]bridge.Entity, len(positions))
	var misrotated []int
	var failed []int
	defer h.ClearCursor(ctx)
	fps := make([]bridge.Box, len(positions))
	left := make([]int, len(positions))
	for i, pos := range positions {
		fps[i], left[i] = h.footprint(it.PlaceResult, pos, dirs[i]), i
	}
	if h.Protos.Entities[it.PlaceResult].Type == "underground-belt" {
		left = ugOrder(positions, dirs)
	}
	// место задевают дерево или камень — сначала убрать (игра иначе: «… is in the way»)
	if err := h.clearSpots(ctx, it.PlaceResult, fps); err != nil {
		return nil, err
	}
	stalls := 0
	for len(left) > 0 {
		p, err := h.B.Player()
		if err != nil {
			return built, err
		}
		// площадки ещё не поставленного, кроме i: на них не вставать
		rest := func(i int) []bridge.Box {
			var bs []bridge.Box
			for _, j := range left {
				if j != i {
					bs = append(bs, fps[j])
				}
			}
			return bs
		}
		// стоит на месте ещё не поставленного — сначала сойти: отсюда поставит соседей и замурует
		// себя в последней дыре (сплошная сетка цистерн, стадия 33 на 267145981: «не дошёл»); так же
		// — стоит там, откуда с ещё не поставленным не выйти (трубы по обе стороны щели между НПЗ,
		// стадия 36 на 778631482)
		standsOn := inAny(p.Position, rest(-1), onSpot) || h.walls(p.Position, rest(-1))
		var here []int
		for _, i := range left {
			if !standsOn && dist(p.Position, fps[i].Center()) < buildReach && h.clickable(p, p.Position, fps[i].Center()) {
				here = append(here, i)
			}
		}
		if again { // повтор: по одному, вплотную
			if err := h.standWithin(ctx, fps[left[0]], 4, rest(left[0])); err != nil {
				return built, err
			}
			here = []int{left[0]}
		}
		if len(here) == 0 {
			// только что поставленное этим же вызовом (ленты трассы) — в модель: иначе место стоять
			// выбиралось на них и рядом, сход с ленты раз за разом (стадия 59 на 778631482, «не
			// сойти с ленты»)
			if h.World != nil {
				if _, err := h.World.Sync(h.B, false); err != nil {
					return built, err
				}
			}
			// перейти туда, откуда достаётся больше всего; не вышло дважды — к ближней
			if stand, ok := h.buildStand(p, fps, left); ok && stalls < 2 {
				stalls++
				// встал не у ленты там, откуда хоть что-то ставится, — дошёл (standOK)
				h.standOK = func(q bridge.Player) bool {
					if inAny(q.Position, rest(-1), onSpot) {
						return false
					}
					return slices.ContainsFunc(left, func(i int) bool {
						return dist(q.Position, fps[i].Center()) < buildReach && h.clickable(q, q.Position, fps[i].Center())
					})
				}
				err := h.WalkTo(ctx, stand, 0.5)
				h.standOK = nil
				if err != nil && !errors.Is(err, ErrNotReached) {
					return built, err
				}
				continue
			}
			if err := h.standNear(ctx, fps[left[0]], rest(left[0])); err != nil {
				return built, err
			}
			here = []int{left[0]}
		}
		stalls = 0
		held = false // после ходьбы — проверить руку
		// после ходьбы персонаж ещё скользит — клик ушёл бы на соседний тайл (стадия 17 на
		// 46082668: инсертер встал тайлом выше, на место длинного): дождаться остановки
		if err := h.waitStill(ctx); err != nil {
			return built, err
		}
		// ленты рядами — протяжкой (drag.go): предмет в руку, рука по ходу ряда, зажатая ЛКМ по
		// тайлам; не вставшее — кликами ниже
		if h.Protos.Entities[it.PlaceResult].Type == "transport-belt" && !again {
			for _, run := range dragRuns(positions, dirs, here) {
				want := dirs[run[0]]
				if !held {
					if err := h.Pick(ctx, item); err != nil {
						return built, err
					}
					held, hand = true, h.hand()
					if q, err := h.B.Player(); err == nil {
						stock = q.Inventory[item] + q.Cursor.Count
					}
				}
				for k := ((want-hand)/4%4 + 4) % 4; k > 0; k-- {
					if err := h.In.Tap("r"); err != nil {
						return built, err
					}
				}
				hand = want
				h.setHand(hand)
				rp := make([]bridge.Pos, len(run))
				for k, i := range run {
					rp[k] = positions[i]
				}
				es, err := h.dragBuild(ctx, it.PlaceResult, rp)
				if errors.Is(err, ErrPlace) {
					held = false // в руке не то — взять заново перед кликами
					break
				}
				if err != nil {
					return built, err
				}
				for k, i := range run {
					e := es[k]
					if e.Name == "" {
						continue
					}
					built[i] = e
					left, here = removeInt(left, i), removeInt(here, i)
					stock--
					if h.World != nil {
						o := world.Object{Name: e.Name, Type: h.Protos.Entities[e.Name].Type, Pos: e.Position, Own: true}
						if e.Direction != nil {
							o.Direction = *e.Direction
						}
						h.World.Put([]world.Object{o})
					}
					if e.Direction != nil && !h.sameDir(e.Name, *e.Direction, dirs[i]) {
						misrotated = append(misrotated, i)
					}
					if d, ok := h.handAfter(e); ok {
						hand = d // протяжка оставляет руку по ходу последней ленты
						h.setHand(hand)
					}
				}
				if stock <= 0 {
					held = false
				}
			}
			if len(here) == 0 {
				continue
			}
		}
		if h.Protos.Entities[it.PlaceResult].Type == "underground-belt" {
			// подземки — строго по порядку «вход, потом его выход»: первая поставленная пары
			// становится входом; ближняя первой — выход встанет входом и пара не сложится
			if len(here) == 0 || here[0] != left[0] {
				if err := h.standNear(ctx, fps[left[0]], rest(left[0])); err != nil {
					return built, err
				}
			}
			here = []int{left[0]}
		} else {
			sort.Slice(here, func(a, b int) bool {
				return dist(p.Position, fps[here[a]].Center()) < dist(p.Position, fps[here[b]].Center())
			})
		}
		for _, i := range here {
			moved := false
			pos, dir := positions[i], dirs[i]
			if h.Protos.Entities[it.PlaceResult].Type == "underground-belt" {
				// подземка поверх подземки: игра заменяет и её пару (быстрая поверх жёлтой — оба
				// конца, из руки уходит 2; TestReplaceBeltFast) — этот конец уже стоит в паре по
				// ходу: поставлено, руку — проверить заново
				if st, err := h.B.GetEntity(it.PlaceResult, pos); err != nil {
					return built, err
				} else if st != nil && st.UGPaired && st.Direction != nil && *st.Direction == dir {
					built[i], held = st.Entity, false
					left = removeInt(left, i)
					if h.World != nil {
						h.World.Put([]world.Object{{Name: st.Name, Type: "underground-belt", Pos: st.Position, Own: true, Direction: dir}})
					}
					continue
				}
			}
			// выход подземки (позади — свой непарный вход туда же): игра сама делает поставленную
			// выходом и спаривает, а выход встаёт против руки — рука наоборот от хода (hand.go); не
			// вышло — см. ugExit ниже
			want, exit := dir, h.ugExitBehind(it.PlaceResult, pos, dir)
			if exit {
				want = (dir + 8) % 16
			}
			fresh := false
			if !held {
				// в руке уже — с прежним поворотом; иначе взять
				q, err := h.B.Player()
				if err != nil || cursorName(q) != item {
					fresh = true
				}
				stock = q.Inventory[item]
				if !fresh {
					stock += q.Cursor.Count
				}
				if err := h.Pick(ctx, item); err != nil {
					return built, err
				}
				held, hand = true, h.hand() // взятое из инвентаря — на север
			}
			if hand != want && !nodir {
				// довернуть руку от запомненного поворота (иначе постройка встанет не так и её
				// придётся поворачивать)
				for k := ((want-hand)/4%4 + 4) % 4; k > 0; k-- {
					if err := h.In.Tap("r"); err != nil {
						return built, err
					}
				}
				hand = want
				h.setHand(hand) // сразу: перестановка и выход подземки читают руку из памяти
			}
			// прямоугольная, вставшая поперёк, накрывает соседей (ленты заменяет), а при
			// перестановке уносит их — свои постройки рядом запомнить, пропавшие — назад
			// (стадия 21 на 859088572: лента отвода у делителя)
			var near []bridge.Entity
			if pe := h.Protos.Entities[it.PlaceResult]; pe.TileWidth != pe.TileHeight {
				near, _ = h.B.FindNearest(bridge.FindArgs{Radius: 2.5, Limit: 30, Position: &pos})
			}
			e, err := h.clickBuild(ctx, it.PlaceResult, pos)
			if errors.Is(err, ErrPlace) {
				// бывает, клик приходится на момент, когда камера ещё едет за персонажем, — ещё раз
				if err := h.waitStill(ctx); err != nil {
					return built, err
				}
				e, err = h.clickBuild(ctx, it.PlaceResult, pos)
			}
			if pe := h.Protos.Entities[it.PlaceResult]; errors.Is(err, ErrPlace) && pe.TileWidth != pe.TileHeight {
				// прямоугольная в руке повёрнута поперёк — может задевать воду/соседей:
				// повернуть руку (R) и снова; направление поправит replace ниже
				for k := 0; k < 3 && errors.Is(err, ErrPlace); k++ {
					if err := h.In.Tap("r"); err != nil {
						return built, err
					}
					hand = (hand + 4) % 16
					h.setHand(hand)
					e, err = h.clickBuild(ctx, it.PlaceResult, pos)
				}
			}
			if errors.Is(err, ErrPlace) {
				// уже стоит (поставил сбитый клик; клик по своей же постройке ничего не строит —
				// стадия 17 на 46082668) — поставлено; поворот сверится ниже
				if ex, xerr := h.B.EntityAt(it.PlaceResult, pos); xerr == nil && ex != nil {
					e, err = *ex, nil
				}
			}
			if errors.Is(err, ErrPlace) {
				// место временно занято — остальные ставить, это — потом (повтор в конце)
				failed = append(failed, i)
				left = removeInt(left, i)
				continue
			}
			if err != nil {
				return built, err
			}
			if d, ok := h.handAfter(e); ok {
				// рука после постановки (сверка памяти): следующая довернёт от неё; сразу в память —
				// перестановка и выход подземки читают руку оттуда
				hand = d
				h.setHand(hand)
			}
			w, hh := h.Protos.Entities[e.Name].TileWidth, h.Protos.Entities[e.Name].TileHeight
			ug := h.Protos.Entities[e.Name].Type == "underground-belt"
			fl := h.fluidLinked(e.Name)
			if e.Direction != nil && (w != hh && *e.Direction%8 != want%8 || ug && !exit && *e.Direction != want ||
				fl && !h.sameDir(e.Name, *e.Direction, want)) {
				// прямоугольная встала поперёк (поворот на месте сдвинет центр) или подземка
				// не туда (R у подземки меняет вход/выход) — поднять, довернуть руку, поставить
				if e, err = h.replace(ctx, item, e, pos, want); err != nil {
					return built, err
				}
				held, hand, moved = false, h.hand(), true
			}
			if exit {
				if e, err = h.ugExit(ctx, item, e, pos, dir); err != nil {
					return built, err
				}
				held, hand = false, h.hand()
			}
			if len(near) > 0 {
				n, err := h.restoreNear(ctx, near, nil, &e, nil)
				if err != nil {
					return built, err
				}
				if n > 0 {
					held, hand = false, h.hand() // ставил другое — и руку вертел
				}
			}
			if stock--; stock <= 0 {
				held = false
			}
			built[i] = e
			left = removeInt(left, i)
			// поставленное — сразу в модель карты: следующее место стоять выбирается уже с ним
			// (свежая лента была «свободна» — бот вставал на неё, его несло: «не сойти с ленты»,
			// TestPlaceUndergroundInBeltCorridor на 1028292478-0)
			if h.World != nil {
				o := world.Object{Name: e.Name, Type: h.Protos.Entities[e.Name].Type, Pos: e.Position, Own: true}
				if e.Direction != nil {
					o.Direction = *e.Direction
				}
				h.World.Put([]world.Object{o})
			}
			// подземку R не поворачивает, а меняет вход/выход — её переставляет replace выше
			if !exit && !ug && !fl && e.Direction != nil && !h.sameDir(e.Name, *e.Direction, dir) {
				misrotated = append(misrotated, i)
			}
			nodir = nodir || e.Direction == nil
			if moved || exit {
				// перестановка (replace, ugExit) водит персонажа: что отсюда достаётся — заново
				// (делитель за 13 тайлов — «Cannot reach», стадия 16 на 1028292478)
				break
			}
		}
	}
	if err := h.ClearCursor(ctx); err != nil {
		return built, err
	}
	for _, i := range misrotated {
		if err := h.Rotate(ctx, built[i], dirs[i]); err != nil {
			return built, err
		}
		d := dirs[i]
		built[i].Direction = &d
	}
	if len(failed) > 0 && !again {
		// не встало — ещё раз после остальных: вплотную и рукой заново (стадия 16 на 46082668:
		// 2 ленты из 40 не встали по два клика, место свободно, события постройки нет — причина
		// не найдена, отдельно потом ставятся)
		ps, ds := make([]bridge.Pos, len(failed)), make([]int, len(failed))
		for k, i := range failed {
			ps[k], ds[k] = positions[i], dirs[i]
		}
		h.Say("%s: не встало в %d местах — ещё раз, вплотную", it.PlaceResult, len(failed))
		more, err := h.placeDirs(ctx, item, ps, ds, true)
		hand = h.hand() // повтор вёл руку сам
		for k, i := range failed {
			if k < len(more) && more[k].Name != "" {
				built[i] = more[k]
			}
		}
		return built, err
	}
	if len(failed) > 0 {
		at := positions[failed[0]]
		return built, fmt.Errorf("%w %s в %d местах, первое (%.1f, %.1f)", ErrPlace, it.PlaceResult, len(failed), at.X, at.Y)
	}
	return built, nil
}

// fluidLinked — у постройки трубные соединения, и куда они смотрят — не всё равно (не простая
// труба, не сборщик): поворот на месте открывает её по очереди на все стороны — жидкость соседних
// труб смешивается (стадия 35 на 1028292478: подземные трубы воды крутились рядом с трубой нефти —
// вода в трубах нефти и во всех цистернах). Такие — поднять и поставить заново, не крутить.
func (h *Hands) fluidLinked(name string) bool {
	e := h.Protos.Entities[name]
	return len(e.PipeConnections) > 0 && e.Type != "pipe" && e.Type != "assembling-machine"
}

// sameDir — направления a и b у постройки name одно: у симметричных (паровой двигатель,
// цистерна — в игре два положения) d и d+8 — одно (стадия 33 на 1028292478: цистерна «не
// повернулась» на запад — встала на восток, а это то же).
func (h *Hands) sameDir(name string, a, b int) bool {
	switch h.Protos.Entities[name].Type {
	case "generator", "storage-tank":
		return a%8 == b%8
	}
	return a == b
}

// SameDir — sameDir для стадий: стоящая постройка смотрит, как в раскладке.
func (h *Hands) SameDir(name string, a, b int) bool { return h.sameDir(name, a, b) }

func removeInt(xs []int, x int) []int {
	for k, v := range xs {
		if v == x {
			return append(xs[:k], xs[k+1:]...)
		}
	}
	return xs
}

// buildStand — точка, откуда в досягаемости больше всего мест left (не на ленте, не на
// самих площадках); при равенстве — ближе к персонажу.
func (h *Hands) buildStand(pl bridge.Player, fps []bridge.Box, left []int) (bridge.Pos, bool) {
	from := pl.Position
	if h.World == nil {
		return bridge.Pos{}, false
	}
	type spot struct {
		q     bridge.Pos
		score float64
	}
	var spots []spot
	cands := append([]int(nil), left...)
	sort.Slice(cands, func(a, b int) bool { return dist(from, fps[cands[a]].Center()) < dist(from, fps[cands[b]].Center()) })
	if len(cands) > 20 {
		cands = cands[:20]
	}
	for _, i := range cands {
		c := fps[i].Center()
		for _, r := range []float64{2.5, 4.5, 6.5} {
			for k := 0; k < 8; k++ {
				a := float64(k) * math.Pi / 4
				q := bridge.Pos{X: c.X + r*math.Cos(a), Y: c.Y + r*math.Sin(a)}
				// и с запасом вокруг: щель в тайл между бурами у ленты блока добычи — дойти туда
				// только по ленте, инерция скатывает обратно на неё («не сойти с ленты», стадия 30
				// на 635406131); таких нет — стоянка у ближнего места (standWithin)
				if !h.World.Standable(q) || h.nearBelt(q) || !h.World.Roomy(q) {
					continue
				}
				n, blocked := 0, false
				for _, j := range left {
					if inBox(q, fps[j], 0.5) {
						blocked = true
						break
					}
					if dist(q, fps[j].Center()) < buildReach-0.5 && h.clickable(pl, q, fps[j].Center()) {
						n++
					}
				}
				if blocked || n == 0 {
					continue
				}
				spots = append(spots, spot{q, float64(n)*10 - dist(from, q)/3})
			}
		}
	}
	// лучшие — не в закрытом кармане своих построек (дойти — только проломом), и с ещё не
	// поставленным (обставит себя им)
	planned := make([]bridge.Box, len(left))
	for k, i := range left {
		planned[k] = fps[i]
	}
	sort.SliceStable(spots, func(a, b int) bool { return spots[a].score > spots[b].score })
	for k, s := range spots {
		if k == 4 {
			break
		}
		if !h.pocket(s.q, planned) {
			return s.q, true
		}
		h.Log("место стоять (%.1f, %.1f) — в закрытом кармане, другое", s.q.X, s.q.Y)
	}
	return bridge.Pos{}, false
}

// replace — поднять e, довернуть предмет в руке до dir и поставить в pos снова.
func (h *Hands) replace(ctx context.Context, item string, e bridge.Entity, pos bridge.Pos, dir int) (bridge.Entity, error) {
	h.Say("%s встал не тем направлением — переставляю", e.Name)
	ug := h.Protos.Entities[e.Name].Type == "underground-belt"
	// игра помнит поворот руки и для заново взятого предмета (не «всегда север», стадия 13:
	// «не встал направлением 0»), и между запусками бота (стадия 23 на 794457638): рука — из
	// памяти (сверена по вставшей неверной, hand.go), R от неё до dir. Подземка: вход или выход
	// решает игра (по паре), направление руки по ней не узнать наверняка (вход встал задом
	// наперёд, стадия 19) — не встала точно по ходу — ещё R и снова, до четырёх раз
	hand := h.hand()
	fl := h.fluidLinked(e.Name)
	// поворот руки после подбора не всегда тот, что у поднятой (последний предмет ушёл в
	// постройку — взятый заново смотрит на север: котёл встал поперёк, а при перестановке —
	// «Steam engine is in the way», стадия 29 на 427965634) — не встало или встало не так: ещё
	// R и снова, до четырёх поворотов
	placed := true // e стоит — поднять перед постановкой
	for try := 0; ; try++ {
		if placed {
			h.noRestore = true
			err := h.Pickup(ctx, e)
			h.noRestore = false
			if err != nil {
				return e, err
			}
		}
		if err := h.Pick(ctx, item); err != nil {
			return e, err
		}
		hand = h.hand() // взятое из инвентаря — на север
		k := ((dir-hand)/4%4 + 4) % 4
		if try > 0 {
			k = 1
		}
		for ; k > 0; k-- {
			if err := h.In.Tap("r"); err != nil {
				return e, err
			}
			hand = (hand + 4) % 16
		}
		h.setHand(hand)
		if err := sleep(ctx, inputLatency); err != nil {
			return e, err
		}
		// поднимал — мог сойти с ленты, и лента унесла: место дальше досягаемости — подойти
		// («Cannot reach» — делитель так и не встал, стадия 18 на 238178543)
		if p, err := h.B.Player(); err == nil && dist(p.Position, pos) > buildReach-1 {
			if err := h.WalkTo(ctx, pos, buildReach-3); err != nil && !errors.Is(err, ErrNotReached) {
				return e, err
			}
		}
		ne, err := h.clickBuild(ctx, e.Name, pos)
		if errors.Is(err, ErrPlace) && try < 3 {
			placed = false
			continue
		}
		if err != nil {
			return e, err
		}
		if d, ok := h.handAfter(ne); ok {
			hand = d
			h.setHand(hand)
		}
		if ne.Direction == nil || h.sameDir(ne.Name, *ne.Direction, dir) || !ug && !fl && *ne.Direction%8 == dir%8 {
			return ne, nil
		}
		if try >= 3 {
			return ne, fmt.Errorf("act: %s не встал направлением %d", e.Name, dir)
		}
		e, placed = ne, true
	}
}

// clickBuild — ЛКМ по точке с постройкой в руке и ожидание события built_entity.
func (h *Hands) clickBuild(ctx context.Context, entity string, pos bridge.Pos) (bridge.Entity, error) {
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		return bridge.Entity{}, err
	}
	p, err := h.B.Player()
	if err != nil {
		return bridge.Entity{}, err
	}
	// персонажа несёт лента — клик уйдёт мимо (стадия 17 на 46082668: лента встала тайлом выше,
	// а нужное место потом — на своей же ленте): сойти и встать
	if h.World != nil && h.World.OnBelt(p.Position) {
		if err := h.stepOffBelt(ctx, p.Position); err != nil {
			return bridge.Entity{}, err
		}
		if err := h.waitStill(ctx); err != nil {
			return bridge.Entity{}, err
		}
		if p, err = h.B.Player(); err != nil {
			return bridge.Entity{}, err
		}
	}
	// в руке не то (там же: делитель вместо ленты, потом пусто) — взять заново
	if item := h.Protos.ItemFor(entity); item != "" && cursorName(p) != item {
		if err := h.Pick(ctx, item); err != nil {
			return bridge.Entity{}, err
		}
		if p, err = h.B.Player(); err != nil {
			return bridge.Entity{}, err
		}
	}
	// цель под панелью или у края окна — не кликать (клик в быструю панель берёт из неё другой
	// предмет в руку): место не встало, вызывающий подойдёт иначе
	if !h.clickable(p, p.Position, pos) {
		return bridge.Entity{}, fmt.Errorf("%w %s в (%.1f, %.1f): под панелью или у края окна", ErrPlace, entity, pos.X, pos.Y)
	}
	if err := h.In.Move(h.Camera(p).ToScreen(pos.X, pos.Y)); err != nil {
		return bridge.Entity{}, err
	}
	if err := sleep(ctx, 40*time.Millisecond); err != nil {
		return bridge.Entity{}, err
	}
	if err := h.In.Click(input.Left); err != nil {
		return bridge.Entity{}, err
	}
	deadline := time.Now().Add(time.Second)
	var strays []bridge.Entity // встало не туда (клик на соседний тайл)
	for time.Now().Before(deadline) {
		if err := sleep(ctx, pollEvery); err != nil {
			return bridge.Entity{}, err
		}
		evs, _, err := h.B.PollEvents(seq0)
		if err != nil {
			return bridge.Entity{}, err
		}
		strays = strays[:0]
		// прямоугольная, повёрнутая не так, встаёт со сдвигом центра — до тайла
		for _, e := range evs {
			if e.Type != "built_entity" || e.Entity == nil || e.Entity.Name != entity {
				continue
			}
			if math.Abs(e.Entity.Position.X-pos.X) <= 0.5 && math.Abs(e.Entity.Position.Y-pos.Y) <= 0.5 {
				return *e.Entity, nil
			}
			if dist(e.Entity.Position, pos) < 3 {
				strays = append(strays, *e.Entity)
			}
		}
	}
	// встало не туда — поднять, иначе займёт чужое место плана (стадия 17 на 46082668: инсертер
	// тайлом выше — на месте длинного, «место занято своей постройкой»)
	for _, e := range strays {
		h.Say("%s встал не там (%.1f, %.1f) — поднимаю", e.Name, e.Position.X, e.Position.Y)
		h.noRestore = true
		err := h.Pickup(ctx, e)
		h.noRestore = false
		if err != nil {
			return bridge.Entity{}, err
		}
	}
	h.debugShot("place-" + entity)
	// что было при клике — для разбора (стадия 16 на 46082668: место свободно, а не встало)
	if q, err := h.B.Player(); err == nil {
		hand, n, sel := "", 0, ""
		if q.Cursor != nil {
			hand, n = q.Cursor.Name, q.Cursor.Count
		}
		if q.Selected != nil {
			sel = q.Selected.Name
		}
		x, y := h.Camera(p).ToScreen(pos.X, pos.Y)
		h.Log("не встало %s в (%.1f, %.1f): в руке %q ×%d, выделено %q, персонаж (%.2f, %.2f) → (%.2f, %.2f), клик (%d, %d)",
			entity, pos.X, pos.Y, hand, n, sel, p.Position.X, p.Position.Y, q.Position.X, q.Position.Y, x-h.Win.X, y-h.Win.Y)
	}
	return bridge.Entity{}, fmt.Errorf("%w %s в (%.1f, %.1f)", ErrPlace, entity, pos.X, pos.Y)
}

func dirName(d int) string {
	switch d {
	case North:
		return ""
	case East:
		return " на восток"
	case South:
		return " на юг"
	case West:
		return " на запад"
	}
	return fmt.Sprintf(" направление %d", d)
}

// Rotate поворачивает свою постройку клавишей R (по часовой, шаг 4) до направления dir.
// После каждого нажатия ждёт, пока игра применит поворот (иначе лишнее нажатие проскочит
// нужное направление).
func (h *Hands) Rotate(ctx context.Context, e bridge.Entity, dir int) error {
	if h.fluidLinked(e.Name) { // трубные соединения: не крутить на месте — поднять и поставить
		if e.Direction != nil && h.sameDir(e.Name, *e.Direction, dir) {
			return nil
		}
		h.Say("%s — не кручу на месте (жидкости смешаются): ставлю заново%s", e.Name, dirName(dir))
		if err := h.Pickup(ctx, e); err != nil {
			return err
		}
		_, err := h.PlaceDirs(ctx, h.Protos.ItemFor(e.Name), []bridge.Pos{e.Position}, []int{dir})
		return err
	}
	h.Say("поворачиваю %s%s", e.Name, dirName(dir))
	if err := h.reach(ctx, e, buildReach); err != nil {
		return err
	}
	if err := h.waitStill(ctx); err != nil {
		return err
	}
	if err := h.ClearCursor(ctx); err != nil { // с предметом в руке R поворачивает руку
		return err
	}
	if err := h.Hover(ctx, e); err != nil {
		return err
	}
	cur := func() (int, bool, error) {
		st, err := h.B.GetEntity(e.Name, e.Position)
		if err != nil {
			return 0, false, err
		}
		if st == nil {
			return 0, false, fmt.Errorf("act: %s исчез", e.Name)
		}
		if st.Direction == nil {
			return dir, true, nil
		}
		return *st.Direction, true, nil
	}
	for i := 0; i < 6; i++ {
		d, _, err := cur()
		if err != nil {
			return err
		}
		if h.sameDir(e.Name, d, dir) {
			return nil
		}
		// R — только когда выделена именно она: ленты вплотную, курсор мог выделить соседнюю
		// (стадия 13 на 851731946: «не повернулся» — поворачивалась соседняя)
		if p, err := h.B.Player(); err != nil {
			return err
		} else if !same(p.Selected, e) {
			if err := h.Hover(ctx, e); err != nil {
				return err
			}
		}
		if err := h.In.Tap("r"); err != nil {
			return err
		}
		for t := 0; t < 10; t++ {
			if err := sleep(ctx, pollEvery); err != nil {
				return err
			}
			if nd, _, err := cur(); err != nil || nd != d {
				break
			}
		}
	}
	return fmt.Errorf("act: %s не повернулся в %d", e.Name, dir)
}

// held — сколько предмета у игрока: инвентарь + рука.
func held(p bridge.Player, item string) int {
	n := p.Inventory[item]
	if p.Cursor != nil && p.Cursor.Name == item {
		n += p.Cursor.Count
	}
	return n
}

var zInterval = 120 * time.Millisecond

// stack — размер пачки предмета.
func (h *Hands) stack(item string) int {
	if h.Protos != nil && h.Protos.Items[item].StackSize > 0 {
		return h.Protos.Items[item].StackSize
	}
	return 50
}

// Insert кладёт n предметов в сущность: предмет в руку → навести → Z n раз
// (каждое нажатие — ровно один предмет); почти всё, что есть, — Ctrl+клик (может лечь
// чуть больше n). Проверка — у игрока стало на n меньше.
func (h *Hands) Insert(ctx context.Context, e bridge.Entity, item string, n int) error {
	p0, err := h.B.Player()
	if err != nil {
		return err
	}
	// в руках меньше нужного, а предмет ещё крафтится (очередь) — сначала дождаться: иначе «всё» —
	// Ctrl+клик кладёт то, что есть, остальное приезжает позже (e2e TestInsertManyIntoAssembler:
	// трубы 21 из 22 — 40 ещё крафтились)
	if held(p0, item) < n && queued(p0, item) > 0 {
		h.Say("жду крафт %s: при себе %d, нужно %d", item, held(p0, item), n)
		if p0, err = h.waitFor(ctx, 2*time.Minute, func(p bridge.Player) bool {
			return held(p, item) >= n || queued(p, item) == 0
		}); err != nil && ctx.Err() != nil {
			return err
		}
		if p0, err = h.B.Player(); err != nil {
			return err
		}
	}
	// «всё» Ctrl+кликом — только когда предмет не крафтится: счёт по убыли теряет докрафченное
	// за вставку (21 из 22 на 1039746529-0); крафтится — нажатиями Z со счётом крафта
	if n >= held(p0, item) && queued(p0, item) == 0 || n >= 4*h.stack(item) {
		// положить всё или много (≥ 4 пачек; лишняя пачка не беда) — Ctrl+клик: кладётся максимум
		// возможного одним действием; удержание Z кладёт по одному — 200 угля ≈ 2 минуты. Почти
		// всё — не Ctrl: он кладёт и лишнее (10 пластин из 11 — легло 11, e2e TestUnstuckKeepsRecipe)
		got, err := h.InsertAll(ctx, e, item)
		if err != nil {
			return err
		}
		// Ctrl+клик бывает кладёт одну пачку (рука) — ещё, пока кладётся (до полного ящика)
		for k := 0; k < 40 && got < n; k++ {
			if q, err := h.B.Player(); err != nil || held(q, item) == 0 {
				break
			}
			more, err := h.InsertAll(ctx, e, item)
			if err != nil {
				return err
			}
			if more == 0 {
				break
			}
			got += more
		}
		if got < n {
			return fmt.Errorf("act: в %s положено %s ×%d из %d (полон?)", e.Name, item, got, n)
		}
		return nil
	}
	if held(p0, item) < n {
		return fmt.Errorf("act: %s есть %d, нужно %d", item, held(p0, item), n)
	}
	h.Say("кладу %s ×%d в %s", item, n, e.Name)
	if err := h.reach(ctx, e, buildReach); err != nil {
		return err
	}
	// положено — убыль в инвентаре и скрафченное за это время: очередь крафта того же предмета
	// пополняет инвентарь (стадия 34 на 1098794301: «положено 6 из 22», а в сборщике — 57,
	// e2e TestInsertManyIntoAssembler); снимок инвентаря — заново, после подхода (за ходьбой
	// очередь доложила)
	if p0, err = h.B.Player(); err != nil {
		return err
	}
	_, seq, err := h.B.PollEvents(1 << 30)
	if err != nil {
		return err
	}
	crafted := 0
	put := func(q bridge.Player) int {
		if evs, s, err := h.B.PollEvents(seq); err == nil {
			for _, ev := range evs {
				if ev.Type == "crafted_item" && ev.Item == item {
					crafted += ev.Count
				}
			}
			seq = s
		}
		return held(p0, item) - held(q, item) + crafted
	}
	if err := h.Pick(ctx, item); err != nil {
		return err
	}
	if err := h.Hover(ctx, e); err != nil {
		h.ClearCursor(ctx)
		return err
	}
	// много — удерживать Z (игра повторяет вставку) и отпустить чуть раньше нужного
	// (отпускание доходит с задержкой); остаток — по одному нажатию
	left := n
	if n > 10 {
		if err := h.In.KeyDown("z"); err != nil {
			return err
		}
		_, _ = h.waitFor(ctx, time.Duration(n)*150*time.Millisecond+2*time.Second, func(q bridge.Player) bool {
			return put(q) >= n-6
		})
		h.In.KeyUp("z")
		// повтор Z доходит и после отпускания — остаток считать, когда счёт перестал меняться
		// (иначе лишний предмет: 151 из 150, e2e TestTakeItemFromChest)
		last := -1
		for k := 0; k < 10; k++ {
			if err := sleep(ctx, inputLatency); err != nil {
				return err
			}
			q, err := h.B.Player()
			if err != nil {
				break
			}
			if c := put(q); c == last {
				break
			} else {
				last = c
			}
		}
		if last >= 0 {
			left = n - last
		}
	}
	// одно нажатие Z (удержание Input.Hold) — ровно один предмет
	for i := 0; i < left; i++ {
		if err := h.In.Tap("z"); err != nil {
			return err
		}
		if err := sleep(ctx, zInterval); err != nil {
			return err
		}
	}
	_, werr := h.waitFor(ctx, 2*time.Second, func(p bridge.Player) bool { return put(p) >= n })
	// нажатие могло потеряться — доложить недостающее (до 3 заходов, курсор — заново: в игре
	// терялись и повторные нажатия — 794457638)
	for try := 0; try < 3 && werr != nil; try++ {
		p, err := h.B.Player()
		if err != nil {
			break
		}
		if lack := n - put(p); lack > 0 && held(p, item) > 0 {
			if p.Cursor == nil || p.Cursor.Name != item {
				if err := h.Pick(ctx, item); err != nil {
					return err
				}
			}
			if err := h.Hover(ctx, e); err != nil {
				h.ClearCursor(ctx)
				return err
			}
			// под нагрузкой (несколько игр разом) короткое нажатие теряется — держать дольше,
			// паузы и ожидание длиннее (автоповтор Z — после ~0.3 с удержания, 100 мс — одно)
			for i := 0; i < lack; i++ {
				if err := h.In.KeyDown("z"); err != nil {
					return err
				}
				if err := sleep(ctx, 100*time.Millisecond); err != nil {
					h.In.KeyUp("z")
					return err
				}
				if err := h.In.KeyUp("z"); err != nil {
					return err
				}
				if err := sleep(ctx, 2*zInterval); err != nil {
					return err
				}
			}
			_, werr = h.waitFor(ctx, 3*time.Second, func(p bridge.Player) bool { return put(p) >= n })
		} else {
			break
		}
	}
	p1, err := h.B.Player()
	if err != nil {
		return err
	}
	if err := h.ClearCursor(ctx); err != nil {
		return err
	}
	if got := put(p1); got < n { // перебор после удержания Z — не беда
		return fmt.Errorf("act: в %s положено %s ×%d из %d (полон?)", e.Name, item, got, n)
	}
	return nil
}

// Take забирает из сущности то, что она выдаёт (у печи — результат): Ctrl+ЛКМ с пустой
// рукой. Возвращает, что прибавилось у игрока.
func (h *Hands) Take(ctx context.Context, e bridge.Entity) (map[string]int, error) {
	h.Say("забираю из %s", e.Name)
	if err := h.reach(ctx, e, buildReach); err != nil {
		return nil, err
	}
	if err := h.Clean(ctx); err != nil {
		return nil, err
	}
	p0, err := h.B.Player()
	if err != nil {
		return nil, err
	}
	if err := h.Hover(ctx, e); err != nil {
		return nil, err
	}
	if err := h.In.KeyDown("ctrl"); err != nil {
		return nil, err
	}
	err = h.In.Click(input.Left)
	h.In.KeyUp("ctrl")
	if err != nil {
		return nil, err
	}
	if err := sleep(ctx, inputLatency); err != nil {
		return nil, err
	}
	p1, err := h.waitFor(ctx, time.Second, func(p bridge.Player) bool {
		for k, v := range p.Inventory {
			if v > p0.Inventory[k] {
				return true
			}
		}
		return false
	})
	got := map[string]int{}
	for k, v := range p1.Inventory {
		if d := v - p0.Inventory[k]; d > 0 {
			got[k] = d
		}
	}
	if errors.Is(err, errTimeout) {
		return got, nil // нечего забирать
	}
	return got, err
}

// Pickup подбирает свою постройку обратно в инвентарь (удержание ПКМ).
func (h *Hands) Pickup(ctx context.Context, e bridge.Entity) error {
	return h.PickupAll(ctx, []bridge.Entity{e})
}

// restoreNear — свои постройки из near (до действия), которых теперь нет, — поставить назад;
// кроме skip (их и убирали) и того, что под now (новая постройка встала на их место).
// Возвращает, сколько поставлено.
func (h *Hands) restoreNear(ctx context.Context, near, skip []bridge.Entity, now *bridge.Entity, release func()) (int, error) {
	var box *bridge.Box
	if now != nil {
		box = now.SelectionBox // настоящая рамка из игры (у делителя на восток расчётная — поперёк)
		if box == nil {
			box = h.collisionBox(*now)
		}
	}
	n := 0
	for _, o := range near {
		if !slices.Contains(builtTypes, o.Type) || slices.ContainsFunc(skip, func(x bridge.Entity) bool { return x.Name == o.Name && x.Position == o.Position }) {
			continue
		}
		if box != nil && box.Contains(o.Position, 0) {
			continue
		}
		if got, err := h.B.EntityAt(o.Name, o.Position); err != nil || got != nil {
			continue
		}
		// на месте другая своя постройка (делитель поперёк ещё стоит) — не задето, а накрыто
		pos := o.Position
		if here, _ := h.B.FindNearest(bridge.FindArgs{Radius: 1.5, Limit: 10, Position: &pos}); slices.ContainsFunc(here, func(x bridge.Entity) bool {
			b := h.collisionBox(x)
			return slices.Contains(builtTypes, x.Type) && b != nil && b.Contains(pos, 0)
		}) {
			continue
		}
		if release != nil {
			release()
		}
		h.Say("задел %s в (%.1f, %.1f) — ставлю назад", o.Name, o.Position.X, o.Position.Y)
		dir := 0
		if o.Direction != nil {
			dir = *o.Direction
		}
		if _, err := h.Place(ctx, o.Name, o.Position, dir); err != nil {
			return n, fmt.Errorf("поставить назад %s: %w", o.Name, err)
		}
		n++
	}
	return n, nil
}

// builtTypes — типы построек бота (рядом с подбираемой: задетые — ставить назад).
var builtTypes = []string{"transport-belt", "underground-belt", "splitter", "inserter", "electric-pole", "assembling-machine",
	"furnace", "mining-drill", "lab", "boiler", "generator", "pipe", "pipe-to-ground"}

// mineReachGame — с какого расстояния до рамки коллизии добываем, не подходя (игра — 2.7).
const mineReachGame = 2.4

// pickupReach — с какого расстояния до рамки коллизии поднимаем свою постройку (игра — reach 10,
// как ставить; 2.7 — только ресурсы): подбор подходил вплотную и вставал в щель между лентами
// шины (стадия 49 на 1114315936, TestPickupFromReach).
const pickupReach = 8.0

// reachOf — досягаемость добычи e: руда, деревья, камни, обломки, рыба, предмет на земле —
// вплотную (mineReachGame), свои постройки — pickupReach.
func reachOf(e bridge.Entity) float64 {
	if strings.HasPrefix(e.Name, "crash-site-") {
		return mineReachGame
	}
	switch e.Type {
	case "resource", "tree", "simple-entity", "simple-entity-with-owner", "fish", "item-entity":
		return mineReachGame
	}
	return pickupReach
}

// PickupAll подбирает свои постройки: ближайшую досягаемую — не сходя с места и не
// отпуская ПКМ между соседними; если досягаемых нет — встаёт туда, откуда достаёт
// больше всего оставшихся.
func (h *Hands) PickupAll(ctx context.Context, es []bridge.Entity) error {
	if len(es) == 0 {
		return nil
	}
	if len(es) == 1 {
		h.Say("подбираю %s", es[0].Name)
	} else {
		h.Say("подбираю %d построек", len(es))
	}
	if err := h.Clean(ctx); err != nil {
		return err
	}
	held := false
	release := func() {
		if held {
			h.In.ButtonUp(input.Right)
			held = false
		}
	}
	defer release()
	left := append([]bridge.Entity(nil), es...)
	reach := func(from bridge.Pos, e bridge.Entity) float64 {
		return dist(from, closestOnBox(from, *h.collisionBox(e)))
	}
	for len(left) > 0 {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		best, bestD := 0, math.Inf(1)
		for i, e := range left {
			if d := reach(p.Position, e); d < bestD {
				best, bestD = i, d
			}
		}
		e := left[best]
		left = append(left[:best], left[best+1:]...)
		if st, err := h.B.GetEntity(e.Name, e.Position); err != nil {
			return err
		} else if st == nil {
			continue // уже нет
		}
		t0 := time.Now()
		if bestD > reachOf(e) {
			release()
			if err := h.WalkTo(ctx, h.bestStand(e, left, reach), 0.5); err != nil {
				if err := h.reachMine(ctx, e); err != nil {
					return err
				}
			}
		}
		t1 := time.Now()
		// свои постройки рядом: зажатая ПКМ после цели добывает то, что оказалось под курсором
		// (лента у делителя пропала, стадия 21 на 859088572) — задетое ставится назад
		var near []bridge.Entity
		if !h.noRestore {
			near, _ = h.B.FindNearest(bridge.FindArgs{Radius: 2.5, Limit: 30, Position: &e.Position})
		}
		err = h.mineHeld(ctx, e, nil, &held)
		if errors.Is(err, ErrFull) && h.OnFull != nil {
			// инвентарь полон — освободить место (стадии — в хранение) и ещё раз (260647970,
			// стадия 16: поднять свой бур с пути — «не поднять»)
			h.Say("инвентарь полон — освобождаю место")
			release()
			if ferr := h.OnFull(ctx); ferr != nil {
				return fmt.Errorf("%w (освободить место: %v)", err, ferr)
			}
			if err := h.reachMine(ctx, e); err != nil {
				return err
			}
			err = h.mineHeld(ctx, e, nil, &held)
		}
		if errors.Is(err, ErrHover) {
			// навести не вышло, а цели уже нет (стадия 49 на 3462254828: лента пропала, пока бот
			// подходил — «не удалось навести курсор») — поднято
			if st, gerr := h.B.GetEntity(e.Name, e.Position); gerr == nil && st == nil {
				h.Log("подбор: %s в (%.1f, %.1f) уже нет", e.Name, e.Position.X, e.Position.Y)
				continue
			}
		}
		if err != nil {
			return err
		}
		h.Log("подбор: ходьба %.2f, добыча %.2f", t1.Sub(t0).Seconds(), time.Since(t1).Seconds())
		if _, err := h.restoreNear(ctx, near, es, nil, release); err != nil {
			return err
		}
	}
	return nil
}

// bestStand — точка у цели e (тайл в досягаемости добычи), откуда достаётся больше всего
// оставшихся others; при равенстве — ближе к цели. Только проходимые тайлы модели.
func (h *Hands) bestStand(e bridge.Entity, others []bridge.Entity, reach func(bridge.Pos, bridge.Entity) float64) bridge.Pos {
	// сначала — с запасом вокруг и в стороне от лент (щель в тайл между лентами шины ходьба
	// проскакивает — стадия 49 на 1114315936); потом — где можно стоять (не на ленте: несёт —
	// «не сойти с ленты», подбор подземки в проходе между рядами лент,
	// TestPlaceUndergroundInBeltCorridor на 1028292478-0); нет — любой проходимый
	if h.World != nil {
		for _, ok := range []func(world.Tile) bool{
			func(t world.Tile) bool { return h.World.Roomy(t.Center()) && !h.nearBelt(t.Center()) },
			func(t world.Tile) bool { return h.World.Standable(t.Center()) },
		} {
			if pos, found := h.standAt(e, others, reach, ok); found {
				return pos
			}
		}
	}
	pos, _ := h.standAt(e, others, reach, func(t world.Tile) bool { return h.World == nil || h.World.Walkable(t) })
	return pos
}

// standAt — bestStand по тайлам, где ok.
func (h *Hands) standAt(e bridge.Entity, others []bridge.Entity, reach func(bridge.Pos, bridge.Entity) float64, ok func(world.Tile) bool) (bridge.Pos, bool) {
	c := world.TileOf(e.Position)
	best, bestN, bestD := e.Position, -1, math.Inf(1)
	w := min(int(reachOf(e))+1, 6) // окно поиска — по досягаемости
	for dy := -w; dy <= w; dy++ {
		for dx := -w; dx <= w; dx++ {
			t := world.Tile{X: c.X + dx, Y: c.Y + dy}
			if !ok(t) {
				continue
			}
			pos := t.Center()
			d := reach(pos, e)
			if d > reachOf(e)-0.3 {
				continue
			}
			n := 0
			for _, o := range others {
				if reach(pos, o) <= reachOf(o)-0.3 {
					n++
				}
			}
			if n > bestN || (n == bestN && d < bestD) {
				best, bestN, bestD = pos, n, d
			}
		}
	}
	return best, bestN >= 0
}

// Drop выкладывает один предмет на землю в точку pos: предмет в руку → навести на пустое
// место → Z. Подтверждение — на земле появился item-entity с этим предметом рядом с pos.
func (h *Hands) Drop(ctx context.Context, item string, pos bridge.Pos) (bridge.Entity, error) {
	h.Say("выкладываю %s на землю в (%.1f, %.1f)", item, pos.X, pos.Y)
	if err := h.guard(ctx); err != nil {
		return bridge.Entity{}, err
	}
	p, err := h.B.Player()
	if err != nil {
		return bridge.Entity{}, err
	}
	if dist(p.Position, pos) > buildReach {
		if err := h.WalkTo(ctx, pos, 3); err != nil {
			return bridge.Entity{}, err
		}
	}
	before, err := h.B.FindNearest(bridge.FindArgs{Type: "item-entity", Position: &pos, Radius: 1, Limit: 20})
	if err != nil {
		return bridge.Entity{}, err
	}
	if err := h.Pick(ctx, item); err != nil {
		return bridge.Entity{}, err
	}
	p, err = h.B.Player()
	if err != nil {
		return bridge.Entity{}, err
	}
	if err := h.In.Move(h.Camera(p).ToScreen(pos.X, pos.Y)); err != nil {
		return bridge.Entity{}, err
	}
	if err := sleep(ctx, 60*time.Millisecond); err != nil {
		return bridge.Entity{}, err
	}
	// под курсором постройка — Z положит в неё; руда, предметы на земле, деревья и камни
	// (крона накрывает соседний тайл; инвентаря нет — Z бросит на землю) — не мешают
	noInv := map[string]bool{"resource": true, "item-entity": true, "tree": true, "simple-entity": true, "cliff": true, "fish": true}
	if p, _ := h.B.Player(); p.Selected != nil && !noInv[p.Selected.Type] {
		h.ClearCursor(ctx)
		return bridge.Entity{}, fmt.Errorf("act: в (%.1f, %.1f) под курсором %s — Z положил бы в него", pos.X, pos.Y, p.Selected.Name)
	}
	if err := h.In.Tap("z"); err != nil {
		return bridge.Entity{}, err
	}
	seen := map[bridge.Pos]bool{}
	for _, e := range before {
		seen[e.Position] = true
	}
	var dropped *bridge.Entity
	deadline := time.Now().Add(time.Second)
	for dropped == nil && time.Now().Before(deadline) {
		if err := sleep(ctx, pollEvery); err != nil {
			return bridge.Entity{}, err
		}
		after, err := h.B.FindNearest(bridge.FindArgs{Type: "item-entity", Position: &pos, Radius: 1, Limit: 20})
		if err != nil {
			return bridge.Entity{}, err
		}
		for i := range after {
			if after[i].Item == item && !seen[after[i].Position] {
				dropped = &after[i]
				break
			}
		}
	}
	if err := h.ClearCursor(ctx); err != nil {
		return bridge.Entity{}, err
	}
	if dropped == nil {
		h.debugShot("drop-" + item)
		return bridge.Entity{}, fmt.Errorf("act: %s не появился на земле у (%.1f, %.1f)", item, pos.X, pos.Y)
	}
	return *dropped, nil
}

// PickupItem поднимает предмет с земли (item-entity): подойти, навести, удержать ПКМ.
func (h *Hands) PickupItem(ctx context.Context, e bridge.Entity) error {
	h.Say("поднимаю с земли %s", e.Item)
	if err := h.reachMine(ctx, e); err != nil {
		return err
	}
	if err := h.Clean(ctx); err != nil {
		return err
	}
	return h.Mine(ctx, e, 5*time.Second)
}

// InsertAll кладёт в постройку весь предмет этого вида из инвентаря: предмет в руку →
// навести → Ctrl+ЛКМ. Возвращает, сколько ушло (сундук может переполниться).
func (h *Hands) InsertAll(ctx context.Context, e bridge.Entity, item string) (int, error) {
	h.Say("кладу весь %s в %s", item, e.Name)
	p0, err := h.B.Player()
	if err != nil {
		return 0, err
	}
	if held(p0, item) == 0 {
		return 0, nil
	}
	if err := h.reach(ctx, e, buildReach); err != nil {
		return 0, err
	}
	// после подхода (бывает полторы минуты) предмета может уже не быть: предыдущий вызов
	// доложил остаток по ещё не обновлённому инвентарю (859088572, стадия 14: «нет stone»)
	if p, err := h.B.Player(); err == nil && held(p, item) == 0 {
		return 0, nil
	}
	if err := h.Pick(ctx, item); err != nil {
		return 0, err
	}
	if err := h.Hover(ctx, e); err != nil {
		h.ClearCursor(ctx)
		return 0, err
	}
	if err := h.In.KeyDown("ctrl"); err != nil {
		return 0, err
	}
	err = h.In.Click(input.Left)
	h.In.KeyUp("ctrl")
	if err != nil {
		return 0, err
	}
	h.waitFor(ctx, 2*time.Second, func(p bridge.Player) bool { return held(p, item) < held(p0, item) })
	if err := sleep(ctx, inputLatency); err != nil {
		return 0, err
	}
	if err := h.ClearCursor(ctx); err != nil {
		return 0, err
	}
	p1, err := h.B.Player()
	if err != nil {
		return 0, err
	}
	return held(p0, item) - held(p1, item), nil
}

// reach — подойти к сущности на расстояние действия r (до рамки выделения). Уже в
// досягаемости — не идём; не дошли, но уже достаём — тоже не ошибка.
func (h *Hands) reach(ctx context.Context, e bridge.Entity, r float64) error {
	within := func() (bool, error) {
		p, err := h.B.Player()
		if err != nil {
			return false, err
		}
		b := e.SelectionBox
		if b == nil {
			return dist(p.Position, e.Position) <= r, nil
		}
		return dist(p.Position, closestOnBox(p.Position, *b)) <= r, nil
	}
	if ok, err := within(); err != nil || ok {
		return err
	}
	err := h.WalkNear(ctx, e, math.Min(2, r-0.5))
	if errors.Is(err, ErrNotReached) {
		if ok, _ := within(); ok {
			return nil
		}
		// к самой рамке пути нет (она в закрытом кармане между постройками) — куда угодно в
		// пределах досягаемости (стадия 50 на 427965634: бойлер в 12 тайлах, к его рамке — через
		// две трубы между двигателями, а дотянуться можно было, сняв одну)
		if r-0.5 > 2 {
			if err = h.WalkNear(ctx, e, r-0.5); err == nil {
				return nil
			}
			if ok, _ := within(); ok {
				return nil
			}
		}
	}
	return err
}

// reachMine — подойти на расстояние добычи руками (до рамки коллизии).
func (h *Hands) reachMine(ctx context.Context, e bridge.Entity) error {
	within := func() (bool, error) {
		p, err := h.B.Player()
		if err != nil {
			return false, err
		}
		return dist(p.Position, closestOnBox(p.Position, *h.collisionBox(e))) <= reachOf(e), nil
	}
	if ok, err := within(); err != nil || ok {
		return err
	}
	err := h.WalkToMine(ctx, e)
	if errors.Is(err, ErrNotReached) {
		if ok, _ := within(); ok {
			return nil
		}
	}
	return err
}

// ugOrder — порядок постановки подземок: одного направления — по ходу ленты (выше по течению
// первой), группы направлений — как в списке. Первая поставленная из пары становится входом:
// список, записанный от конца трассы к началу (сквозная лента шин в плане), ставил выход
// раньше входа — обе «входы», пара не складывалась (стадия 40 на 427965634: сера застряла).
func ugOrder(positions []bridge.Pos, dirs []int) []int {
	along := func(i int) float64 {
		switch dirs[i] {
		case 0:
			return -positions[i].Y
		case 4:
			return positions[i].X
		case 8:
			return positions[i].Y
		}
		return -positions[i].X
	}
	order := make([]int, len(positions))
	for i := range order {
		order[i] = i
	}
	first := map[int]int{} // направление — первое место в списке
	for i, d := range dirs {
		if _, ok := first[d]; !ok {
			first[d] = i
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		da, db := dirs[order[a]], dirs[order[b]]
		if da != db {
			return first[da] < first[db]
		}
		return along(order[a]) < along(order[b])
	})
	return order
}

// ugExitBehind — позади pos (против dir, в пределах дальности) свой непарный вход подземки
// entity, смотрящий в dir: подземка в pos — его выход.
func (h *Hands) ugExitBehind(entity string, pos bridge.Pos, dir int) bool {
	e := h.Protos.Entities[entity]
	if e.Type != "underground-belt" {
		return false
	}
	v := map[int][2]float64{0: {0, -1}, 4: {1, 0}, 8: {0, 1}, 12: {-1, 0}}[dir]
	dx, dy := v[0], v[1]
	for k := 1; k <= e.MaxUndergroundDistance; k++ {
		q := bridge.Pos{X: pos.X - dx*float64(k), Y: pos.Y - dy*float64(k)}
		st, err := h.B.GetEntity(entity, q)
		if err != nil || st == nil {
			continue
		}
		if st.Direction != nil && *st.Direction%8 != dir%8 {
			continue // поперечная подземка в тоннеле — не помеха паре (стадия 40 на 427965634: подземка стали поперёк тоннеля серы — поиск входа обрывался на ней)
		}
		return st.Own && st.UGType == "input" && !st.UGPaired && st.Direction != nil && *st.Direction == dir
	}
	return false
}

// ugExit — подземка в pos должна быть выходом (dir) парой к входу позади. Игра обычно
// делает её выходом сама; не вышло — перебрать направление руки (поднять, R, поставить):
// встала выходом по ходу — готово; входом навстречу (пара сложилась наоборот) — R
// переворачивает пару.
func (h *Hands) ugExit(ctx context.Context, item string, e bridge.Entity, pos bridge.Pos, dir int) (bridge.Entity, error) {
	state := func() (*bridge.EntityState, error) { return h.B.GetEntity(e.Name, pos) }
	for try := 0; try < 5; try++ {
		st, err := state()
		if err != nil {
			return e, err
		}
		if st != nil && st.Direction != nil && st.UGPaired {
			switch {
			case st.UGType == "output" && *st.Direction == dir:
				d := dir
				e.Direction = &d
				return e, nil
			case st.UGType == "input" && *st.Direction == (dir+8)%16:
				if err := h.ClearCursor(ctx); err != nil {
					return e, err
				}
				if err := h.Rotate(ctx, e, dir); err != nil {
					return e, err
				}
				continue
			}
		}
		if try == 4 {
			break
		}
		if try == 0 {
			h.Say("подземка не спарилась — перебираю направление руки")
		}
		if st != nil {
			if err := h.Pickup(ctx, e); err != nil {
				return e, err
			}
		}
		if err := h.Pick(ctx, item); err != nil {
			return e, err
		}
		// поворот предмета в руке при взятии сбрасывается — на попытке k жать R k раз
		for k := 0; k <= try; k++ {
			if err := h.In.Tap("r"); err != nil {
				return e, err
			}
			h.setHand((h.hand() + 4) % 16) // R с подземкой в руке — и поворот руки для следующих
		}
		if err := sleep(ctx, inputLatency); err != nil {
			return e, err
		}
		ne, err := h.clickBuild(ctx, e.Name, pos)
		if err != nil {
			return e, err
		}
		if d, ok := h.handAfter(ne); ok {
			h.setHand(d)
		}
		e = ne
	}
	return e, fmt.Errorf("act: подземка в (%.1f, %.1f) не встала выходом", pos.X, pos.Y)
}

// clickable — точка мира target видна на экране персонажу, стоящему в stand, и не под
// панелью интерфейса (быстрый доступ внизу, миникарта): клик туда попал бы в интерфейс.
func (h *Hands) clickable(p bridge.Player, stand, target bridge.Pos) bool {
	p.Position = stand
	x, y := h.Camera(p).ToScreen(target.X, target.Y)
	wx, wy := x-h.Win.X, y-h.Win.Y
	const margin = 24
	if wx < margin || wy < margin || wx >= h.Win.W-margin || wy >= h.Win.H-margin {
		return false
	}
	return !h.underPanel(x, y)
}

// nearBelt — лента ближе 1.3 тайла от точки (в 8 сторон): остановка с инерцией до 1.2 тайла
// заносит на неё (стоянка в 1.1 от ленты — шесть раз «на ленте — схожу», стадия 17 на
// 46082668; в щели между бурами у ленты коридора добычи бота сносило).
func (h *Hands) nearBelt(q bridge.Pos) bool {
	for _, d := range [][2]float64{{1.3, 0}, {-1.3, 0}, {0, 1.3}, {0, -1.3}, {0.9, 0.9}, {-0.9, 0.9}, {0.9, -0.9}, {-0.9, -0.9}} {
		if h.World.OnBelt(bridge.Pos{X: q.X + d[0], Y: q.Y + d[1]}) {
			return true
		}
	}
	return false
}
