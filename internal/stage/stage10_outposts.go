package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 10 (stages.md): добывающие точки на твердотопливных бурах — outpostCoal угольных пар
// (буры носом друг в друга), outpostIron пар «бур → печь» на железе, outpostCopper на меди, 4
// бура носом в сундук на камне.

// Точки стадии 10: железа на стройку добычи, плавки и шины нужно ~11 000 — 30 пар копят его за
// ~25 минут, пока идут стадии 11–14 (10 пар — ~75 минут); уголь — топливо всем точкам.
const (
	outpostIron   = 30
	outpostCopper = 6
	outpostCoal   = 8 // пар (по 2 бура)
	// outpostReach — точки на пятнах не дальше стольких тайлов (между краями) от уже занятых: не
	// влезло на ближнее — следующее по близости; дальше — только если в пределах места нет вообще
	outpostReach = 150
)

// Железо на буры вначале — руками: руда в печи будущих пар; дальше итеративно — пластины с
// печей. Подошёл к точке — забрать со всех её печей. Уголь — с пар, пока их нет — руками.

const (
	drill        = "burner-mining-drill"
	furnace      = "stone-furnace"
	outpostsFile = "state-outposts.json"
	fuelLevel    = 20 // угля в бур или печь: доливать до стольких
	fuelFull     = 50 // в конце стадии — до стольких в каждый бур и печь
	fuelLow      = 3  // когда топлива меньше — доливать
	craftFree    = 4  // свободных ячеек при ожидании крафта: меньше — ненужное в хранение
)

// chest — сундук стадий: деревянный до обслуживания, с него (стадия 22) — железный (stage.Do).
var chest = "wooden-chest"

// outpostSite — точка: вид (coal, iron-ore, copper-ore, stone) и её постройки по порядку стройки.
type outpostSite struct {
	Ore   string             `json:"ore"`
	Items []layout.Placement `json:"items"`
}

// outpostOres — руды точек стадии 10 и сколько свободных тайлов руды им нужно в одном пятне:
// по ~8 тайлов на бур (2×2 и проходы), не меньше 60.
var outpostOres = map[string]int{
	"iron-ore":   max(60, 8*outpostIron),
	"copper-ore": max(60, 8*outpostCopper),
	"coal":       max(60, 8*2*outpostCoal),
	"stone":      60,
}

// outpostAnchor — место точек (stages.md, стадия 10; решение пользователя 27.10): где меньше
// всего бегать между рудами — сумма расстояний до ближайшего пятна каждой руды, куда влезают все
// её точки (свободных от закрытого в l тайлов руды — outpostOres). Кандидаты — центр базы и
// центры пятен; к сумме — четверть расстояния до центра базы: при равенстве — ближе к нему.
// Какой-то руды нет — центр. (Без размера: место у маленького пятна железа — 30 пар расползлись
// до 477 тайлов, 260647970.)
func outpostAnchor(h *act.Hands, l *layout.Layout, center world.Tile) world.Tile {
	var usable []world.Patch
	for _, pt := range h.World.Patches() {
		need, ok := outpostOres[pt.Name]
		if !ok || len(pt.Tiles) < need {
			continue
		}
		free := 0
		for _, t := range pt.Tiles {
			if !l.Reserved[t] {
				free++
			}
		}
		if free >= need {
			usable = append(usable, pt)
		}
	}
	// до пятна — от точки до его рамки
	to := func(c world.Tile, pt world.Patch) float64 {
		dx := max(0, pt.Min.X-c.X, c.X-pt.Max.X)
		dy := max(0, pt.Min.Y-c.Y, c.Y-pt.Max.Y)
		return math.Hypot(float64(dx), float64(dy))
	}
	cost := func(c world.Tile) float64 {
		sum := 0.25 * math.Hypot(float64(c.X-center.X), float64(c.Y-center.Y))
		for ore := range outpostOres {
			best := math.Inf(1)
			for _, pt := range usable {
				if pt.Name == ore {
					best = math.Min(best, to(c, pt))
				}
			}
			sum += best
		}
		return sum
	}
	best, bestCost := center, cost(center)
	for _, pt := range usable {
		c := world.Tile{X: int(pt.Center.X), Y: int(pt.Center.Y)}
		if v := cost(c); v < bestCost {
			best, bestCost = c, v
		}
	}
	return best
}

// reserveRect — тайлы r закрыты для раскладки l.
func reserveRect(l *layout.Layout, r cluster.Rect) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			l.Reserved[world.Tile{X: x, Y: y}] = true
		}
	}
}

// outpostsLayout — раскладка всех точек у ближайших месторождений; один раз на карту (файл).
func outpostsLayout(h *act.Hands) ([]outpostSite, error) {
	if data, err := os.ReadFile(runPath(outpostsFile)); err == nil {
		var sites []outpostSite
		if json.Unmarshal(data, &sites) == nil && len(sites) > 0 {
			return sites, nil
		}
	}
	if h.B != nil { // на бумаге (paper.go) — карта уже вся в h.World
		if _, err := h.World.Sync(h.B, true); err != nil {
			return nil, err
		}
	}
	// от центра базы (голова bus-1), а не от персонажа: план считается до стадии (проверка), когда
	// персонаж ещё там, где кончилась разведка, — точки легли на краю квадрата, в 1300 тайлах от
	// базы, без пути (260647970). Разметки нет — от персонажа.
	near, err := hereOrBase(h)
	if err != nil {
		return nil, err
	}
	if c, ok := baseCenter(); ok {
		near = c
	}
	// patches — пятна ore (не меньше min тайлов) по близости к near: сначала в пределах
	// outpostReach (на бег между точками время не терять), дальше — только если в них не влезло
	type patchAt struct {
		world.Patch
		d float64 // до ближайшего занятого пятна (по краям), пока их нет — до старта
	}
	var used []world.Patch // пятна, где уже стоят точки
	// gap — расстояние между краями пятен a и b (0 — касаются)
	gap := func(a, b world.Patch) float64 {
		dx := max(0, max(a.Min.X-b.Max.X, b.Min.X-a.Max.X))
		dy := max(0, max(a.Min.Y-b.Max.Y, b.Min.Y-a.Max.Y))
		return math.Hypot(float64(dx), float64(dy))
	}
	// patches — пятна ore (не меньше min тайлов) по близости к уже занятым: точки — кучно, чтобы
	// между ними бегать недалеко
	patches := func(ore string, min int) []patchAt {
		var all []patchAt
		for _, pt := range h.World.Patches() {
			if pt.Name != ore || len(pt.Tiles) < min {
				continue
			}
			d := math.Hypot(pt.Center.X-float64(near.X), pt.Center.Y-float64(near.Y))
			if len(used) > 0 {
				d = math.Inf(1)
				for _, u := range used {
					d = math.Min(d, gap(pt, u))
				}
			}
			all = append(all, patchAt{pt, d})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].d < all[j].d })
		return all
	}
	l := layout.New(h.Protos, h.World)
	// точки — не в зонах, которые строятся до их разбора (стадия 15): энергия, шины, производство,
	// конечный продукт; зоны добычи — можно (добыча строится на 16–17, stages.md). Закрытые зоны
	// добычи уводили точки в 600–1000 тайлов от базы: пятна у центра — под ними целиком
	if r, err := LoadRough(); err == nil {
		for _, z := range r.Zones {
			if z.Kind == cluster.ZoneMine {
				continue
			}
			reserveRect(l, z.Rect.Grow(cluster.ZoneBorder))
		}
	}
	// и кластеры плана вне зон, кроме добычи (хранение у головы bus-1 — стадия 13, до разбора точек)
	if plan, err := LoadPlan(); err == nil {
		for _, c := range plan.Clusters {
			if c.Kind != cluster.Mine && c.Kind != cluster.Bus {
				reserveRect(l, c.Area.Grow(cluster.ZoneBorder))
			}
		}
	}
	near = outpostAnchor(h, l, near)
	h.Log("место точек — (%d, %d)", near.X, near.Y)
	var sites []outpostSite
	add := func(ore string, build func(tiles []world.Tile, at world.Tile) error, n int) error {
		ps := patches(ore, 60)
		if len(ps) == 0 {
			return fmt.Errorf("на открытой карте нет месторождения %s", ore)
		}
		k := 0 // пятно, на которое ставим; не влезло — следующее по близости
		for i := 0; i < n; i++ {
			mark := len(l.List)
			for {
				err := build(ps[k].Tiles, near)
				if err == nil {
					if !slices.ContainsFunc(used, func(u world.Patch) bool { return u.Min == ps[k].Min && u.Name == ps[k].Name }) {
						used = append(used, ps[k].Patch)
					}
					break
				}
				if k+1 >= len(ps) {
					return fmt.Errorf("%s: %w", ore, err)
				}
				k++
				if ps[k].d > outpostReach && ps[k-1].d <= outpostReach {
					if i >= (n+1)/2 { // половина уже стоит — дальше не бегать (stages.md, стадия 10)
						h.Say("точки %s: %d из %d — дальше %d тайлов от занятых пятен не ставлю", ore, i, n, outpostReach)
						return nil
					}
					h.Say("точки %s: в %d тайлах от занятых пятен места нет — дальше (%.0f)", ore, outpostReach, ps[k].d)
				}
			}
			// в паре «бур → печь» — сначала печь (в неё руками руда, пока бура нет)
			items := append([]layout.Placement(nil), l.List[mark:]...)
			sort.SliceStable(items, func(a, b int) bool { return items[a].Name != drill && items[b].Name == drill })
			sites = append(sites, outpostSite{Ore: ore, Items: items})
		}
		return nil
	}
	if err := add("iron-ore", func(t []world.Tile, at world.Tile) error {
		return l.DrillFurnace(t, "iron-ore", drill, furnace, at, "iron")
	}, outpostIron); err != nil {
		return nil, err
	}
	if err := add("coal", func(t []world.Tile, at world.Tile) error { return l.CoalPair(t, "coal", drill, at, "coal") }, outpostCoal); err != nil {
		return nil, err
	}
	if err := add("stone", func(t []world.Tile, at world.Tile) error {
		return l.StoneChest(t, "stone", drill, chest, 4, at, "stone")
	}, 1); err != nil {
		return nil, err
	}
	if err := add("copper-ore", func(t []world.Tile, at world.Tile) error {
		return l.DrillFurnace(t, "copper-ore", drill, furnace, at, "copper")
	}, outpostCopper); err != nil {
		return nil, err
	}
	return sites, writeState(outpostsFile, sites)
}

// outpostOrder — порядок стройки: 3 железные пары (железо на остальное), угольные пары, остальное
// железо, камень, медь.
func outpostOrder(sites []outpostSite) []int {
	var iron, coal, stone, copper []int
	for i, s := range sites {
		switch s.Ore {
		case "iron-ore":
			iron = append(iron, i)
		case "coal":
			coal = append(coal, i)
		case "stone":
			stone = append(stone, i)
		case "copper-ore":
			copper = append(copper, i)
		}
	}
	k := min(3, len(iron))
	res := append([]int{}, iron[:k]...)
	res = append(res, coal...)
	res = append(res, iron[k:]...)
	res = append(res, stone...)
	return append(res, copper...)
}

type worker struct {
	h        *act.Hands
	sites    []outpostSite
	useStore bool // брать из кластера хранения (стадии после 13)
	any      bool // store — и нужное текущей стадии (полный инвентарь посреди дела)
	// placed — после постановки всех построек вида name (build): сразу настроить, пока по
	// лентам ничего не пошло
	placed func(ctx context.Context, name string) error
	// growing — store расширяет хранение (стройка сундуков сама зовёт store — не по кругу)
	growing bool
	// noWait — крафт в очередь и дальше, не ждать готового: стоящее в очереди считается как
	// уже есть (have); конец очереди — waitQueue (стадия 14: бот стоял 39–57 мин из 62–86, пока
	// шёл ручной крафт, — правила скорости 27.10)
	noWait bool
}

// pending — сколько item будет из очереди крафта (только в режиме noWait).
func (s *worker) pending(item string) int {
	if !s.noWait {
		return 0
	}
	return s.queued(item) * s.perCraft(item)
}

// waitQueue — дождаться конца очереди крафта; готовое некуда класть — ненужное в хранение (как
// waitInv). Очередь 3 мин не убывает — ошибка.
func (s *worker) waitQueue(ctx context.Context) error {
	left := func() (int, error) {
		p, err := s.h.B.Player()
		if err != nil {
			return 0, err
		}
		n := 0
		for _, q := range p.CraftingQueue {
			n += q.Count
		}
		return n, nil
	}
	n, err := left()
	if err != nil || n == 0 {
		return err
	}
	s.h.Say("жду конца очереди крафта: %d крафтов", n)
	last, since := n, time.Now()
	for n > 0 {
		if err := s.store(ctx, craftFree); err != nil && !errors.Is(err, errNoStorage) {
			return err
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return err
		}
		if n, err = left(); err != nil {
			return err
		}
		if n < last {
			last, since = n, time.Now()
		} else if time.Since(since) > 3*time.Minute {
			return fmt.Errorf("очередь крафта стоит: %d крафтов за 3 мин не убавилось", n)
		}
	}
	return nil
}

func (s *worker) entity(pl layout.Placement) (*bridge.Entity, error) {
	return s.h.B.EntityAt(pl.Name, pl.Pos)
}

func (s *worker) inv(item string) int {
	p, err := s.h.B.Player()
	if err != nil {
		return 0
	}
	return p.Inventory[item]
}

// freeSlots — свободных ячеек инвентаря.
func (s *worker) freeSlots() int {
	p, err := s.h.B.Player()
	if err != nil {
		return 0
	}
	return p.InventorySize - len(p.InventorySlots)
}

func runOutposts(ctx context.Context, h *act.Hands) error {
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites}
	for _, si := range outpostOrder(sites) {
		for _, pl := range sites[si].Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, err := s.entity(pl)
			if err != nil {
				return err
			}
			fresh := e == nil
			if fresh {
				if err := s.have(ctx, pl.Name, 1); err != nil {
					return err
				}
				placed, err := h.Place(ctx, pl.Name, pl.Pos, pl.Dir)
				if err != nil {
					return fmt.Errorf("поставить %s в (%.1f, %.1f): %w", pl.Name, pl.Pos.X, pl.Pos.Y, err)
				}
				e = &placed
			}
			// угольные пары — только при постановке: дальше заправляют друг друга
			if pl.Name != chest && (fresh || sites[si].Ore != "coal") {
				if err := s.fuel(ctx, *e); err != nil {
					return err
				}
			}
		}
	}
	// ранние точки за время стройки сожгли топливо — в конце обойти все и долить до полного
	// (пользователь: по 50); во время стройки — по fuelLevel, угля в начале мало
	for _, si := range outpostOrder(sites) {
		if sites[si].Ore == "coal" {
			continue // угольные пары заправляют друг друга
		}
		for _, pl := range sites[si].Items {
			if pl.Name == chest {
				continue
			}
			e, err := s.entity(pl)
			if err != nil {
				return err
			}
			if e != nil {
				if err := s.fuelTo(ctx, *e, fuelFull, fuelFull); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// fuel — в бур или печь уголь: меньше fuelLow — долить до fuelLevel.
func (s *worker) fuel(ctx context.Context, e bridge.Entity) error {
	return s.fuelTo(ctx, e, fuelLow, fuelLevel)
}

// fuelTo — в бур или печь уголь: меньше low — долить до to.
func (s *worker) fuelTo(ctx context.Context, e bridge.Entity, low, to int) error {
	st, err := s.h.B.GetEntity(e.Name, e.Position)
	if err != nil || st == nil {
		return err
	}
	have := st.Inventories["fuel"]["coal"]
	if have >= low {
		return nil
	}
	// угля много — Ctrl+клик (весь слот разом), а не по штуке
	if s.inv("coal") > 200 {
		_, err := s.h.InsertAll(ctx, e, "coal")
		return err
	}
	if err := s.have(ctx, "coal", to-have); err != nil {
		return err
	}
	err = s.h.Insert(ctx, e, "coal", to-have)
	if err != nil {
		// угольная пара: сосед докладывает в тот же слот, пока кладём — слот полон раньше
		if st, e2 := s.h.B.GetEntity(e.Name, e.Position); e2 == nil && st != nil && st.Inventories["fuel"]["coal"] >= to*9/10 {
			return nil
		}
	}
	return err
}

// have — довести предмет в инвентаре до n: сырьё — добыть (уголь и камень — с точек, если
// есть), пластины — с печей точек или руда руками в печи, остальное — скрафтить.
func (s *worker) have(ctx context.Context, item string, n int) error {
	for tries := 0; s.inv(item)+s.pending(item) < n; tries++ { // noWait: в очереди — как есть
		if tries > 40+n/10 { // много — дольше (за круг с точек — десятки)
			return fmt.Errorf("не набрать %s ×%d (есть %d)", item, n, s.inv(item))
		}
		// перед добычей — не меньше freeBefore свободных ячеек (stages.md, инвентарь; 4000
		// пластин и готовые постройки забивали его на стадии 14): ненужное — в хранение;
		// хранения ещё нет — как есть
		if err := s.store(ctx, freeBefore, item); err != nil && !errors.Is(err, errNoStorage) {
			return err
		}
		if s.useStore && !s.any && s.freeSlots() < 2 {
			// инвентарь полон нужным стадии (буры, столбы, пластины) — брать некуда (260647970,
			// стадия 16: «взято transport-belt ×0 из 40»): нужное, кроме item, временно в
			// хранение — стройка идёт пачками, остальное доберётся потом
			s.any = true
			err := s.store(ctx, freeBefore, item)
			s.any = false
			if err != nil && !errors.Is(err, errNoStorage) {
				return err
			}
		}
		lack := n - s.inv(item) - s.pending(item)
		if s.useStore {
			if got, err := s.fromStorage(ctx, item, lack); err != nil || got {
				if err != nil {
					return err
				}
				continue
			}
		}
		var err error
		switch item {
		case "iron-plate", "copper-plate":
			if s.useStore { // точек стадии 10 уже нет — из буферов выхода плавки, ждать их
				err = s.fromBuffers(ctx, item, lack)
				break
			}
			err = s.plates(ctx, item, lack)
		case "coal":
			err = s.coal(ctx, lack)
		case "stone":
			err = s.stone(ctx, lack)
		case "wood":
			_, err = s.h.Gather(ctx, "tree", max(1, lack/4), 300)
		case "iron-ore", "copper-ore":
			_, err = s.h.Gather(ctx, item, lack, 300)
		default:
			err = s.craft(ctx, item, lack)
		}
		if err != nil && !errors.Is(err, act.ErrNoTargets) {
			return err
		}
	}
	return nil
}

// craft — скрафтить n item: сначала сырьё на всё дерево (пластины, камень, дерево). Уже в
// очереди крафта — только дождаться.
func (s *worker) craft(ctx context.Context, item string, n int) error {
	if s.queued(item)*s.perCraft(item) >= n {
		if s.noWait {
			return nil
		}
		return s.waitInv(ctx, item, s.inv(item)+n, s.craftLimit(item, 0))
	}
	need := map[string]int{}
	switch item {
	case drill: // 3 шестерни (6 железа) + 3 железа + печь (5 камня)
		need["iron-plate"], need["stone"] = 9*n, 5*n
	case furnace:
		need["stone"] = 5 * n
	case "wooden-chest": // не chest: с обслуживания chest — железный (8 пластин), не дерево
		need["wood"] = 2 * n
	default: // по дереву ручных рецептов
		f, err := s.h.B.Force()
		if err != nil {
			return err
		}
		// сырьё — на целое число крафтов (ленты — по 2 за крафт: на 65 считалось 97.5 железа,
		// а крафтов 33 — нужно 99, последнему не хватило: стадия 21 на 954176679)
		per := s.perCraft(item)
		hc := calc.Handcraft(s.h.Protos, s.h.Protos.NewAvailability(f.Researched, false), item, float64((n+per-1)/per*per))
		for raw, v := range hc.Base {
			if raw != "iron-plate" && raw != "copper-plate" && raw != "stone" && raw != "wood" {
				return fmt.Errorf("крафт %s: нужно %s — руками не сделать", item, raw)
			}
			need[raw] = int(math.Ceil(v - 1e-9))
		}
	}
	for _, raw := range []string{"iron-plate", "copper-plate", "stone", "wood"} {
		if need[raw] > 0 {
			if err := s.have(ctx, raw, need[raw]); err != nil {
				return err
			}
		}
	}
	// пачкой: в очередь — сколько ещё не хватает на точках и на сколько есть сырья; ждать
	// только нужное сейчас, остальное крафтится, пока бот ставит и заправляет
	total := n
	if item == drill || item == furnace {
		missing := s.missing(item) - s.inv(item) - s.queued(item)
		afford := s.inv("stone") / 5
		if item == drill {
			afford = min(afford, s.inv("iron-plate")/9)
		}
		total = max(n, min(missing, afford))
	}
	if total > n {
		s.h.Say("в очередь %s ×%d (нужно сейчас %d)", item, total, n)
	}
	before := s.inv(item)
	per := s.perCraft(item)
	if err := s.h.Craft(ctx, item, (total+per-1)/per); err != nil {
		return err
	}
	if s.noWait { // крафтится, пока бот делает остальное (waitQueue — в конце)
		return nil
	}
	return s.waitInv(ctx, item, before+n, s.craftLimit(item, 0))
}

// craftLimit — сколько ждать крафта: вся очередь (с item) и ещё n item, с запасом.
func (s *worker) craftLimit(item string, n int) time.Duration {
	p, err := s.h.B.Player()
	f, ferr := s.h.B.Force()
	if err != nil || ferr != nil {
		return 10 * time.Minute
	}
	av := s.h.Protos.NewAvailability(f.Researched, true)
	secs := calc.Handcraft(s.h.Protos, av, item, float64(n)).Seconds
	for _, q := range p.CraftingQueue {
		if !q.Prerequisite {
			secs += calc.Handcraft(s.h.Protos, av, q.Recipe, float64(q.Count*s.perCraft(q.Recipe))).Seconds
		}
	}
	return time.Duration(secs*1.3+20) * time.Second
}

// perCraft — предметов за один крафт (подземная труба — 2).
func (s *worker) perCraft(item string) int {
	for _, pr := range s.h.Protos.Recipes[item].Products {
		if pr.Name == item && pr.Mean() > 1 {
			return int(pr.Mean())
		}
	}
	return 1
}

// waitInv — дождаться, пока item в инвентаре станет не меньше n.
func (s *worker) waitInv(ctx context.Context, item string, n int, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for s.inv(item) < n {
		// готовое некуда класть — крафт стоит («inventory full»): ненужное сейчас — в хранение
		// (крафтящееся — нет); хранения ещё нет — ждать как есть
		t0 := time.Now()
		if err := s.store(ctx, craftFree, item); err != nil && !errors.Is(err, errNoStorage) {
			return err
		}
		deadline = deadline.Add(time.Since(t0)) // пока носил — крафт шёл, но лимит не тратить
		if time.Now().After(deadline) {
			return fmt.Errorf("крафт %s не закончился (есть %d, ждали %d)", item, s.inv(item), n)
		}
		if err := sleepCtx(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// missing — сколько item ещё не стоит на точках (и на буры: печь в рецепте бура).
func (s *worker) missing(item string) int {
	n := 0
	for _, site := range s.sites {
		for _, pl := range site.Items {
			if e, err := s.entity(pl); err == nil && e == nil {
				if pl.Name == item {
					n++
				}
				if item == furnace && pl.Name == drill {
					n++ // печь — в рецепте бура
				}
			}
		}
	}
	return n
}

// queued — сколько item уже в очереди крафта (своих, не полуфабрикатов).
func (s *worker) queued(item string) int {
	p, err := s.h.B.Player()
	if err != nil {
		return 0
	}
	n := 0
	for _, q := range p.CraftingQueue {
		// полуфабрикаты (печи для буров в очереди) уйдут в свой крафт, в инвентарь не попадут
		if q.Recipe == item && !q.Prerequisite {
			n += q.Count
		}
	}
	return n
}

// built — постройки name на точках руды ore, которые уже стоят.
func (s *worker) built(ore, name string) []bridge.Entity {
	var res []bridge.Entity
	for _, site := range s.sites {
		if site.Ore != ore {
			continue
		}
		for _, pl := range site.Items {
			if pl.Name != name {
				continue
			}
			if e, err := s.entity(pl); err == nil && e != nil {
				res = append(res, *e)
			}
		}
	}
	return res
}

// pairDonors — с каких угольных буров брать уголь: в каждой паре (точка угля) — один, где угля
// больше, и не меньше pairTake; второй остаётся с топливом и кормит соседа.
func (s *worker) pairDonors() []bridge.Entity {
	var res []bridge.Entity
	for _, site := range s.sites {
		if site.Ore != "coal" {
			continue
		}
		var best *bridge.Entity
		most := pairTake - 1
		for _, pl := range site.Items {
			if pl.Name != drill {
				continue
			}
			st, err := s.h.B.GetEntity(pl.Name, pl.Pos)
			if err != nil || st == nil {
				continue
			}
			if n := st.Inventories["fuel"]["coal"]; n > most {
				e := st.Entity
				best, most = &e, n
			}
		}
		if best != nil {
			res = append(res, *best)
		}
	}
	return res
}

// pairTake — с бура угольной пары брать, только если в нём не меньше стольких.
const pairTake = 5

// takeAll — забрать со всех построек точки (печи, сундук), где что-то есть; подошёл — со всех.
func (s *worker) takeAll(ctx context.Context, es []bridge.Entity, inv string) (int, error) {
	got := 0
	for _, e := range es {
		st, err := s.h.B.GetEntity(e.Name, e.Position)
		if err != nil || st == nil {
			continue
		}
		n := 0
		for _, v := range st.Inventories[inv] {
			n += v
		}
		if n == 0 {
			continue
		}
		g, err := s.h.Take(ctx, e)
		if err != nil {
			return got, err
		}
		for _, v := range g {
			got += v
		}
	}
	return got, nil
}

// plates — пластины: забрать со всех печей точки; мало — руду руками в печи точки (с углём),
// подождать и забрать. Печей ещё нет — поставить первую печь точки.
func (s *worker) plates(ctx context.Context, plate string, lack int) error {
	ore := map[string]string{"iron-plate": "iron-ore", "copper-plate": "copper-ore"}[plate]
	fs := s.built(ore, furnace)
	if got, err := s.takeAll(ctx, fs, "crafter_output"); err != nil || got >= lack {
		return err
	}
	if len(fs) == 0 {
		pl, ok := s.firstMissing(ore, furnace)
		if !ok {
			return fmt.Errorf("нет печей для %s", plate)
		}
		if err := s.have(ctx, furnace, 1); err != nil {
			return err
		}
		e, err := s.h.Place(ctx, furnace, pl.Pos, pl.Dir)
		if err != nil {
			return err
		}
		fs = []bridge.Entity{e}
	}
	// печи с буром плавят сами — подождать их; без бура — руда руками
	manual := lack
	if len(s.built(ore, drill)) > 0 {
		manual = 0
	}
	per := 0
	if manual > 0 {
		per = (min(manual, 50*len(fs)) + len(fs) - 1) / len(fs)
		if err := s.have(ctx, ore, per*len(fs)); err != nil {
			return err
		}
	}
	// буры точки — тоже с топливом (без него печам нечего плавить)
	for _, d := range s.built(ore, drill) {
		if err := s.fuel(ctx, d); err != nil {
			return err
		}
	}
	for _, f := range fs {
		if err := s.fuel(ctx, f); err != nil {
			return err
		}
		if per > 0 {
			if err := s.h.Insert(ctx, f, ore, min(per, s.inv(ore))); err != nil {
				return err
			}
		}
	}
	wait := time.Duration(float64(max(per, 5))*3.3) * time.Second
	s.h.Say("жду плавки %s %.0f с (нужно ещё %d)", plate, wait.Seconds(), lack)
	if err := sleepCtx(ctx, wait); err != nil {
		return err
	}
	_, err := s.takeAll(ctx, fs, "crafter_output")
	return err
}

func (s *worker) firstMissing(ore, name string) (layout.Placement, bool) {
	for _, site := range s.sites {
		if site.Ore != ore {
			continue
		}
		for _, pl := range site.Items {
			if pl.Name != name {
				continue
			}
			if e, err := s.entity(pl); err == nil && e == nil {
				return pl, true
			}
		}
	}
	return layout.Placement{}, false
}

// coal — уголь: с угольных пар (топливо буров), нет пар — с больших камней или руками.
func (s *worker) coal(ctx context.Context, lack int) error {
	if ds := s.pairDonors(); len(ds) > 0 {
		// назад не класть (stages.md): у каждого бура горит свой уголь, сосед докладывает ему
		// новый быстрее, чем он сгорает; с пары — только с одного бура (выгребали оба — оба
		// догорали с пустыми слотами, и пара вставала насовсем: 1028292478)
		got, err := s.takeAll(ctx, ds, "fuel")
		if err != nil || got >= lack {
			return err
		}
		// с только что поставленных пар — по паре кусков: остальное — с камней и руками (иначе
		// попытки have кончались на крохах с пар — «не набрать coal ×20», 208708719)
		lack -= got
	}
	// руками — сначала руда (ближе к точкам), нет — большие камни так же далеко, как для камня
	// (в 60 тайлах их не было, ближайший — в 217: 208708719)
	_, err := s.h.Gather(ctx, "coal", lack, 300)
	if !errors.Is(err, act.ErrNoTargets) {
		return err
	}
	if _, err = s.h.Gather(ctx, "huge-rock", 1, 300); errors.Is(err, act.ErrNoTargets) {
		return fmt.Errorf("угля нет: ни с пар, ни руды, ни больших камней в 300 тайлах")
	}
	return err
}

// stone — камень: из сундука каменной точки, нет — с камней.
func (s *worker) stone(ctx context.Context, lack int) error {
	if cs := s.built("stone", chest); len(cs) > 0 {
		got, err := s.takeAll(ctx, cs, "chest")
		if err != nil || got >= lack {
			return err
		}
	}
	if _, err := s.h.Gather(ctx, "huge-rock", 1, 300); err == nil {
		return nil
	}
	_, err := s.h.Gather(ctx, "rock", 1, 300)
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func checkOutposts(h *act.Hands) error {
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	count := map[string]int{}
	for _, site := range sites {
		for _, pl := range site.Items {
			e, err := h.B.EntityAt(pl.Name, pl.Pos)
			if err != nil {
				return err
			}
			if e == nil {
				return fmt.Errorf("%s точки %s в (%.1f, %.1f) не стоит", pl.Name, site.Ore, pl.Pos.X, pl.Pos.Y)
			}
			if pl.Name == drill {
				count[site.Ore]++
			}
			if pl.Name != chest {
				// угольная пара сразу после того, как с неё забрали уголь: слот пуст, пока сосед
				// не доложит (или бур ждёт места) — ждать до 30 с, а не падать (729989868)
				var st *bridge.EntityState
				for wait := 0; wait <= 30; wait += 5 {
					if wait > 0 {
						time.Sleep(5 * time.Second)
					}
					if st, err = h.B.GetEntity(pl.Name, pl.Pos); err != nil {
						return err
					}
					if st != nil && (len(st.Inventories["fuel"]) > 0 || st.Status == "working") {
						break
					}
				}
				if st == nil || len(st.Inventories["fuel"]) == 0 && st.Status != "working" {
					return fmt.Errorf("%s точки %s в (%.1f, %.1f) без топлива", pl.Name, site.Ore, pl.Pos.X, pl.Pos.Y)
				}
			}
		}
	}
	// буров — сколько в плане точек (дальше outpostReach план может поставить меньше нужного)
	want := map[string]int{}
	for _, site := range sites {
		for _, pl := range site.Items {
			if pl.Name == drill {
				want[site.Ore]++
			}
		}
	}
	for ore, n := range want {
		if count[ore] != n {
			return fmt.Errorf("буров на %s: %d из %d", ore, count[ore], n)
		}
	}
	return nil
}

// fromBuffers — пластины из буферов выхода плавки (стадия 17): сколько не хватает (lack) и
// две стопки запаса, с ближних; нигде нет — подождать. Не всё подряд: «забрать всё» набирало
// тысячи пластин, их не выкладывают — инвентарь забит, крафт стоит (стадия 28 на 859088572).
func (s *worker) fromBuffers(ctx context.Context, plate string, lack int) error {
	data, err := os.ReadFile(runPath(smeltingFile))
	if err != nil {
		return fmt.Errorf("%s: нет буферов выхода плавки (стадия 17): %w", plate, err)
	}
	var list []layout.Placement
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	var es []bridge.Entity
	for _, pl := range list {
		if pl.Name != chest || !strings.HasPrefix(pl.Tag, "outbuf/") {
			continue
		}
		if st, err := s.h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && st.Inventories["chest"][plate] > 0 {
			es = append(es, st.Entity)
		}
	}
	if len(es) == 0 {
		s.h.Say("жду %s в буферах выхода плавки", plate)
		return sleepCtx(ctx, 20*time.Second)
	}
	p, err := s.h.B.Player()
	if err != nil {
		return err
	}
	sort.Slice(es, func(i, j int) bool { return dist2(es[i].Position, p.Position) < dist2(es[j].Position, p.Position) })
	want := s.inv(plate) + max(lack, 1) + 200
	for _, e := range es {
		if s.inv(plate) >= want {
			break
		}
		if err := s.h.TakeItem(ctx, e, plate, want-s.inv(plate)); err != nil {
			return err
		}
	}
	return nil
}
