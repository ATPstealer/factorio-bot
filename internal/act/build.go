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
// подземки и делители друг друга заменяют сами).
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
		r := math.Max(fp.RightBottom.X-fp.LeftTop.X, fp.RightBottom.Y-fp.LeftTop.Y)/2 + 2
		for _, o := range h.World.ObjectsNear(c, r) {
			rock := o.Type == "simple-entity" && strings.Contains(o.Name, "rock")
			// своё на месте — помеха, кроме быстрой замены: ленты друг друга, сундук сундуком
			// (железный поверх деревянного — содержимое остаётся, стадия 17)
			pt := h.Protos.Entities[entity].Type
			own := o.Own && o.Type != "character" && !(beltLike(o.Type) && beltLike(pt)) && !(o.Type == "container" && pt == "container")
			if o.Type != "tree" && !rock && !own || seen[o.Pos] {
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
			if own {
				return fmt.Errorf("%w: %s в (%.1f, %.1f)", ErrOccupied, o.Name, o.Pos.X, o.Pos.Y)
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

func beltLike(typ string) bool {
	return typ == "transport-belt" || typ == "underground-belt" || typ == "splitter"
}

func inBox(p bridge.Pos, b bridge.Box, margin float64) bool {
	return p.X > b.LeftTop.X-margin && p.X < b.RightBottom.X+margin && p.Y > b.LeftTop.Y-margin && p.Y < b.RightBottom.Y+margin
}

// buildReach — с какого расстояния ставим и кладём не сходя с места (досягаемость
// персонажа 10, с запасом). Важно, например, когда персонаж строит вокруг себя.
const buildReach = 9

// standNear — подойти к месту постройки: на расстояние досягаемости, но не на саму
// площадку (персонаж мешает поставить). Точка — ближайшая к персонажу свободная вокруг
// площадки (рядом могут стоять другие постройки).
func (h *Hands) standNear(ctx context.Context, fp bridge.Box) error {
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	c := fp.Center()
	if !inBox(p.Position, fp, 0.35) && dist(p.Position, c) < buildReach { // рамка персонажа ±0.2 + задержка
		return nil
	}
	spot := bridge.Pos{X: c.X, Y: fp.RightBottom.Y + 1.5}
	if h.World != nil {
		best := math.Inf(1)
		for _, r := range []float64{1.5, 2.5, 4} {
			for k := 0; k < 16; k++ {
				a := float64(k) * math.Pi / 8
				q := bridge.Pos{X: c.X + (r+(fp.RightBottom.X-fp.LeftTop.X)/2)*math.Cos(a), Y: c.Y + (r+(fp.RightBottom.Y-fp.LeftTop.Y)/2)*math.Sin(a)}
				if inBox(q, fp, 0.8) || !h.World.Standable(q) {
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
	}
	err = h.WalkTo(ctx, spot, 0.5)
	if errors.Is(err, ErrNotReached) {
		// не дошёл до точки, но и отсюда ставится — не ошибка
		if q, perr := h.B.Player(); perr == nil && !inBox(q.Position, fp, 0.35) && dist(q.Position, c) < buildReach {
			return nil
		}
	}
	return err
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
	// поворот руки — из памяти (игра сохраняет его и для заново взятого предмета: стадия 13,
	// каждая лента вставала не туда при «взятый смотрит на север»); после каждой постановки
	// сверяется с тем, как встала постройка
	if q, err := h.B.Player(); err == nil && cursorName(q) != item {
		if err := h.ClearCursor(ctx); err != nil {
			return nil, err
		}
	}
	hand := h.handDir[item] // куда смотрит предмет в руке (по предмету; не было — север)
	defer func() { h.setHand(item, hand) }()
	// held — предмет точно в руке (после удачной постановки игра доливает руку из инвентаря,
	// пока он есть): не спрашивать игру перед каждой постановкой
	held, stock := false, -1
	built := make([]bridge.Entity, len(positions))
	var misrotated []int
	var failed []bridge.Pos
	defer h.ClearCursor(ctx)
	fps := make([]bridge.Box, len(positions))
	left := make([]int, len(positions))
	for i, pos := range positions {
		fps[i], left[i] = h.footprint(it.PlaceResult, pos, dirs[i]), i
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
		var here []int
		for _, i := range left {
			if !inBox(p.Position, fps[i], 0.35) && dist(p.Position, fps[i].Center()) < buildReach && h.clickable(p, p.Position, fps[i].Center()) {
				here = append(here, i)
			}
		}
		if len(here) == 0 {
			// перейти туда, откуда достаётся больше всего; не вышло дважды — к ближней
			if stand, ok := h.buildStand(p, fps, left); ok && stalls < 2 {
				stalls++
				if err := h.WalkTo(ctx, stand, 0.5); err != nil && !errors.Is(err, ErrNotReached) {
					return built, err
				}
				continue
			}
			if err := h.standNear(ctx, fps[left[0]]); err != nil {
				return built, err
			}
			here = []int{left[0]}
		}
		stalls = 0
		held = false // после ходьбы — проверить руку
		if h.Protos.Entities[it.PlaceResult].Type == "underground-belt" {
			// подземки — строго по порядку «вход, потом его выход»: первая поставленная пары
			// становится входом; ближняя первой — выход встанет входом и пара не сложится
			if len(here) == 0 || here[0] != left[0] {
				if err := h.standNear(ctx, fps[left[0]]); err != nil {
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
			pos, dir := positions[i], dirs[i]
			// выход подземки (позади — свой непарный вход туда же): обычно игра сама делает
			// поставленную выходом и спаривает; не вышло — см. ugExit ниже
			want, exit := dir, h.ugExitBehind(it.PlaceResult, pos, dir)
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
				held = true
			}
			if hand != want {
				// довернуть руку от запомненного поворота (иначе постройка встанет не так и её
				// придётся поворачивать)
				for k := ((want-hand)/4%4 + 4) % 4; k > 0; k-- {
					if err := h.In.Tap("r"); err != nil {
						return built, err
					}
				}
				hand = want
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
					e, err = h.clickBuild(ctx, it.PlaceResult, pos)
				}
			}
			if errors.Is(err, ErrPlace) {
				// место временно занято — остальные ставить, это — потом (вернём ошибку)
				failed = append(failed, pos)
				left = removeInt(left, i)
				continue
			}
			if err != nil {
				return built, err
			}
			w, hh := h.Protos.Entities[e.Name].TileWidth, h.Protos.Entities[e.Name].TileHeight
			ug := h.Protos.Entities[e.Name].Type == "underground-belt"
			if e.Direction != nil && (w != hh && *e.Direction%8 != want%8 || ug && !exit && *e.Direction != want) {
				// прямоугольная встала поперёк (поворот на месте сдвинет центр) или подземка
				// не туда (R у подземки меняет вход/выход) — поднять, довернуть руку, поставить
				if e, err = h.replace(ctx, item, e, pos, want); err != nil {
					return built, err
				}
				held, hand = false, want
			}
			if exit {
				if e, err = h.ugExit(ctx, item, e, pos, dir); err != nil {
					return built, err
				}
				held = false
			}
			if len(near) > 0 {
				n, err := h.restoreNear(ctx, near, nil, &e, nil)
				if err != nil {
					return built, err
				}
				if n > 0 {
					held = false
				}
			}
			if stock--; stock <= 0 {
				held = false
			}
			built[i] = e
			left = removeInt(left, i)
			// подземку R не поворачивает, а меняет вход/выход — её переставляет replace выше
			if !exit && !ug && e.Direction != nil && *e.Direction != dir && !(h.Protos.Entities[e.Name].Type == "generator" && *e.Direction%8 == dir%8) {
				misrotated = append(misrotated, i)
			}
			if e.Direction != nil && !exit && !ug && w == hh {
				// рука смотрит, куда встала постройка (сверка памяти): следующая довернёт от неё
				hand = *e.Direction
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
	if len(failed) > 0 {
		return built, fmt.Errorf("%w %s в %d местах, первое (%.1f, %.1f)", ErrPlace, it.PlaceResult, len(failed), failed[0].X, failed[0].Y)
	}
	return built, nil
}

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
	best, bestScore := bridge.Pos{}, math.Inf(-1)
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
				if !h.World.Standable(q) || h.nearBelt(q) {
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
				if score := float64(n)*10 - dist(from, q)/3; score > bestScore {
					best, bestScore = q, score
				}
			}
		}
	}
	return best, !math.IsInf(bestScore, -1)
}

// replace — поднять e, довернуть предмет в руке до dir и поставить в pos снова.
func (h *Hands) replace(ctx context.Context, item string, e bridge.Entity, pos bridge.Pos, dir int) (bridge.Entity, error) {
	h.Say("%s встал не тем направлением — переставляю", e.Name)
	ug := h.Protos.Entities[e.Name].Type == "underground-belt"
	// игра помнит поворот руки и для заново взятого предмета (не «всегда север», стадия 13:
	// «не встал направлением 0»), и между запусками бота (стадия 23 на 794457638): рука
	// смотрит, как встала неверная — R от него до dir. Подземка: вход или выход решает игра
	// (по паре), направление руки по ней не узнать наверняка (вход встал задом наперёд,
	// стадия 19) — не встала точно по ходу — ещё R и снова, до четырёх раз
	hand := h.handDir[item]
	if e.Direction != nil {
		hand = *e.Direction
	}
	tries := 1
	if ug {
		tries = 4
	}
	for try := 0; ; try++ {
		h.noRestore = true
		err := h.Pickup(ctx, e)
		h.noRestore = false
		if err != nil {
			return e, err
		}
		if err := h.Pick(ctx, item); err != nil {
			return e, err
		}
		k := ((dir-hand)/4%4 + 4) % 4
		if try > 0 {
			k = 1
		}
		for ; k > 0; k-- {
			if err := h.In.Tap("r"); err != nil {
				return e, err
			}
		}
		h.setHand(item, dir)
		if err := sleep(ctx, inputLatency); err != nil {
			return e, err
		}
		ne, err := h.clickBuild(ctx, e.Name, pos)
		if err != nil {
			return e, err
		}
		if ne.Direction == nil || *ne.Direction == dir || !ug && *ne.Direction%8 == dir%8 {
			return ne, nil
		}
		if try+1 >= tries {
			return ne, fmt.Errorf("act: %s не встал направлением %d", e.Name, dir)
		}
		e = ne
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
	for time.Now().Before(deadline) {
		if err := sleep(ctx, pollEvery); err != nil {
			return bridge.Entity{}, err
		}
		evs, _, err := h.B.PollEvents(seq0)
		if err != nil {
			return bridge.Entity{}, err
		}
		// прямоугольная, повёрнутая не так, встаёт со сдвигом центра — до тайла
		for _, e := range evs {
			if e.Type == "built_entity" && e.Entity != nil && e.Entity.Name == entity &&
				math.Abs(e.Entity.Position.X-pos.X) <= 0.5 && math.Abs(e.Entity.Position.Y-pos.Y) <= 0.5 {
				return *e.Entity, nil
			}
		}
	}
	h.debugShot("place-" + entity)
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
		if d == dir || h.Protos.Entities[e.Name].Type == "generator" && d%8 == dir%8 {
			return nil // у симметричных (паровой двигатель) d и d+8 — одно
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
	if n*5 >= held(p0, item)*4 || n >= 4*h.stack(item) {
		// положить всё или почти всё (≥ 80%) или много (≥ 4 пачек; лишняя пачка не беда) —
		// Ctrl+клик: кладётся максимум возможного одним действием; удержание Z кладёт по
		// одному — 200 угля ≈ 2 минуты
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
			return held(p0, item)-held(q, item) >= n-6
		})
		h.In.KeyUp("z")
		if err := sleep(ctx, inputLatency); err != nil {
			return err
		}
		if q, err := h.B.Player(); err == nil {
			left = n - (held(p0, item) - held(q, item))
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
	_, werr := h.waitFor(ctx, 2*time.Second, func(p bridge.Player) bool { return held(p0, item)-held(p, item) >= n })
	// нажатие могло потеряться — доложить недостающее (до 3 заходов, курсор — заново: в игре
	// терялись и повторные нажатия, стадия 2 на 794457638)
	for try := 0; try < 3 && werr != nil; try++ {
		p, err := h.B.Player()
		if err != nil {
			break
		}
		if lack := n - (held(p0, item) - held(p, item)); lack > 0 && held(p, item) > 0 {
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
			_, werr = h.waitFor(ctx, 3*time.Second, func(p bridge.Player) bool { return held(p0, item)-held(p, item) >= n })
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
	if got := held(p0, item) - held(p1, item); got < n { // перебор после удержания Z — не беда
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
		if bestD > mineReachGame {
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
		if err := h.mineHeld(ctx, e, nil, &held); err != nil {
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
	c := world.TileOf(e.Position)
	best, bestN, bestD := e.Position, -1, math.Inf(1)
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			t := world.Tile{X: c.X + dx, Y: c.Y + dy}
			if h.World != nil && !h.World.Walkable(t) {
				continue
			}
			pos := t.Center()
			d := reach(pos, e)
			if d > mineReachGame-0.3 {
				continue
			}
			n := 0
			for _, o := range others {
				if reach(pos, o) <= mineReachGame-0.3 {
					n++
				}
			}
			if n > bestN || (n == bestN && d < bestD) {
				best, bestN, bestD = pos, n, d
			}
		}
	}
	return best
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
		return dist(p.Position, closestOnBox(p.Position, *h.collisionBox(e))) <= mineReachGame, nil
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

// LeaveArea — если персонаж внутри рамки a (с полем 0.5), выйти к ближайшей свободной
// точке снаружи (в досягаемости стройки).
func (h *Hands) LeaveArea(ctx context.Context, a bridge.Box) error {
	p, err := h.B.Player()
	if err != nil || !inBox(p.Position, a, 0.5) || h.World == nil {
		return err
	}
	best, spot := math.Inf(1), bridge.Pos{}
	for _, d := range []float64{1.2, 2, 3} {
		cands := []bridge.Pos{
			{X: a.LeftTop.X - d, Y: p.Position.Y}, {X: a.RightBottom.X + d, Y: p.Position.Y},
			{X: p.Position.X, Y: a.LeftTop.Y - d}, {X: p.Position.X, Y: a.RightBottom.Y + d},
		}
		for _, q := range cands {
			if h.World.Standable(q) && dist(p.Position, q) < best {
				best, spot = dist(p.Position, q), q
			}
		}
		if !math.IsInf(best, 1) {
			break
		}
	}
	if math.IsInf(best, 1) {
		return nil
	}
	h.Say("выхожу с площадки стройки")
	return h.WalkTo(ctx, spot, 0.5)
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
		}
		if err := sleep(ctx, inputLatency); err != nil {
			return e, err
		}
		ne, err := h.clickBuild(ctx, e.Name, pos)
		if err != nil {
			return e, err
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

// StandFor — где встать, чтобы ставить из places (сущность и место) больше всего разом,
// и что оттуда ставится сейчас (стоя на месте): индексы places.
func (h *Hands) StandFor(ctx context.Context, entities []string, places []bridge.Pos, dirs []int) (here []int, stand bridge.Pos, err error) {
	p, err := h.B.Player()
	if err != nil {
		return nil, bridge.Pos{}, err
	}
	fps := make([]bridge.Box, len(places))
	left := make([]int, len(places))
	for i := range places {
		fps[i], left[i] = h.footprint(entities[i], places[i], dirs[i]), i
	}
	for _, i := range left {
		if !inBox(p.Position, fps[i], 0.35) && dist(p.Position, fps[i].Center()) < buildReach-0.5 && h.clickable(p, p.Position, fps[i].Center()) {
			here = append(here, i)
		}
	}
	if len(here) > 0 {
		return here, p.Position, nil
	}
	stand, ok := h.buildStand(p, fps, left)
	if !ok {
		return nil, fps[0].Center(), nil
	}
	return nil, stand, nil
}

// nearBelt — рядом с точкой (соседние тайлы) лента: стоять там — в щели между бурами у
// ленты коридора добычи бота сносило и он застревал.
func (h *Hands) nearBelt(q bridge.Pos) bool {
	for _, d := range [][2]float64{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		if h.World.OnBelt(bridge.Pos{X: q.X + d[0], Y: q.Y + d[1]}) {
			return true
		}
	}
	return false
}

// setHand — запомнить поворот руки для предмета item.
func (h *Hands) setHand(item string, dir int) {
	if h.handDir == nil {
		h.handDir = map[string]int{}
	}
	h.handDir[item] = dir
}
