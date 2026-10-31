package stage

import (
	"context"
	"encoding/json"
	"errors"
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/layout"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

// worker — руки стадии с инвентарём: набрать нужное (have: инвентарь, хранение, буферы выхода
// своих кластеров, крафт; до хранения — с точек стадии 10), поставить раскладку (build: по видам
// внутри участка, из хранения — на всю оставшуюся стройку). Выкладка в хранение — inventory.go,
// добыча с точек — stage10_outposts.go.

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
	// all — вся стройка стадии, когда build идёт по участкам (buildSorted): из хранения — на
	// всю оставшуюся, не на участок
	all []layout.Placement
	// far — буферы, до которых не дойти (fromProducts): больше не ходить
	far map[bridge.Pos]bool
	// noWait — крафт в очередь и дальше, не ждать готового: стоящее в очереди считается как
	// уже есть (have); конца очереди стадия 14 не ждёт — следующие стадии ждут предмет из
	// очереди, только когда он нужен (craft: «уже в очереди — только дождаться»; stages.md)
	noWait bool
}

// pending — сколько item будет из очереди крафта (только в режиме noWait).
func (s *worker) pending(item string) int {
	if !s.noWait {
		return 0
	}
	return s.queued(item) * s.perCraft(item)
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

// have — довести предмет в инвентаре до n: сырьё — добыть (уголь и камень — с точек, если
// есть), пластины — с печей точек или руда руками в печи, остальное — скрафтить.
func (s *worker) have(ctx context.Context, item string, n int) error {
	last := -1
	for tries := 0; s.inv(item)+s.pending(item) < n; tries++ { // noWait: в очереди — как есть
		// попытки без прибавки: крафт частями (стадия 30 — по 1–4 бура за заход, инвентарь
		// занят набранным на стадию) прибавляет понемногу — это не застревание (1028292478)
		if got := s.inv(item) + s.pending(item); got > last {
			last, tries = got, 0
		}
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
		// больше, чем влезает в инвентарь (с двумя ячейками про запас), не набрать: цель — что
		// влезает, иначе круг «взял из хранения — добыча упёрлась в полный инвентарь — OnFull
		// выложил — снова взял» (стадия 29 на 427965634: уголь на бойлеры, 1266 выкладок, 27 мин)
		stack := max(s.h.Protos.Items[item].StackSize, 1)
		if room := s.inv(item) + s.pending(item) + max(s.freeSlots()-2, 0)*stack; n > room && room > s.inv(item)+s.pending(item) {
			s.h.Log("%s ×%d не влезает — набираю %d", item, n, room)
			n = room
		}
		lack := n - s.inv(item) - s.pending(item)
		if s.useStore {
			// брать — на всю оставшуюся стройку стадии (сколько влезет), не на пачку: за 40
			// лентами к буферу у базы ходил из дальних блоков — 13–21 ходка в час (стадия 30)
			take := max(lack, s.remaining(item))
			switch item {
			case "iron-plate", "copper-plate", "steel-plate", "stone", "wood":
				take = max(take, s.remainingRaw(item)) // сырьё на крафт всего оставшегося
			}
			if got, err := s.fromStorage(ctx, item, take); err != nil || got {
				if err != nil {
					return err
				}
				continue
			}
			// готовое — из буферов выхода своих кластеров, руками — только чего там нет
			// (stages.md, инвентарь)
			if _, made := s.h.Protos.Recipes[item]; made && item != "iron-plate" && item != "copper-plate" {
				if got, err := s.fromProducts(ctx, item, take); err != nil || got {
					if err != nil {
						return err
					}
					continue
				}
			}
		}
		var err error
		s.h.Log("набираю %s: есть %d, нужно %d", item, s.inv(item), n)
		gathering = item // OnFull (stage.go) набираемое не выкладывает
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
			if s.useStore && !s.handcraftable(item) {
				// руками не сделать (сталь) — только из буферов выхода кластеров: ждать, как
				// пластины; а кластера, что его делает, ещё нет — временный сборщик (stages.md,
				// инвентарь)
				if !s.produced(item) {
					err = s.machineCraft(ctx, item, lack)
					break
				}
				s.h.Say("жду %s в буферах выхода кластеров", item)
				err = sleepCtx(ctx, 20*time.Second)
				break
			}
			err = s.craft(ctx, item, lack)
		}
		gathering = ""
		if err != nil && !errors.Is(err, act.ErrNoTargets) {
			return err
		}
	}
	return nil
}

// gathering — что сейчас набирает have (добыча, крафт): OnFull при полном инвентаре это не
// выкладывает (стадия 29 на 427965634: уголь на бойлеры выкладывался и брался по кругу).
var gathering string

// handcraftable — item делается руками (сталь — нет: только в печи).
func (s *worker) handcraftable(item string) bool {
	f, err := s.h.B.Force()
	if err != nil {
		return true
	}
	_, base := calc.Handcraft(s.h.Protos, s.h.Protos.NewAvailability(f.Researched, false), item, 1).Base[item]
	return !base
}

// produced — item делает свой кластер: в игре стоит буфер выхода кластера с этим продуктом.
func (s *worker) produced(item string) bool {
	plan, err := LoadPurplePlan()
	if err != nil {
		return false
	}
	makes := map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Product == item {
			makes[c.Name] = true
		}
	}
	var bufs []layout.Placement
	names, lists := stateLayouts()
	for _, base := range names {
		for _, pl := range lists[base] {
			if name, ok := strings.CutPrefix(pl.Tag, "outbuf/"); ok && makes[name] && strings.HasSuffix(pl.Name, "chest") {
				bufs = append(bufs, pl)
			}
		}
	}
	left, err := notBuilt(s.h, bufs)
	return err == nil && len(left) < len(bufs)
}

// machineCraft — n item во временном сборщике (stages.md, инвентарь): руками не сделать, а
// кластера ещё нет — сборщик у хранения рядом со столбом сети, рецепт, ингредиенты, ждать,
// забрать готовое, сборщик поднять.
func (s *worker) machineCraft(ctx context.Context, item string, n int) error {
	f, err := s.h.B.Force()
	if err != nil {
		return err
	}
	recipe := ""
	for _, r := range s.h.Protos.RecipesFor(item) {
		if slices.Contains(f.Recipes, r) {
			recipe = r
			break
		}
	}
	if recipe == "" {
		return fmt.Errorf("%s: рецепт не открыт", item)
	}
	rec := s.h.Protos.Recipes[recipe]
	m, ok := s.h.Protos.BestCrafter(rec.Category, s.h.Protos.NewAvailability(f.Researched, false), "")
	if !ok || s.h.Protos.Entities[m.Name].Type != "assembling-machine" {
		return fmt.Errorf("%s: руками не сделать, а сборщика под %s нет", item, rec.Category)
	}
	mitem := s.h.Protos.ItemFor(m.Name)
	// сразу на всю оставшуюся стройку стадии (насосы трассы — по одному двигателю: сборщик
	// ставился и поднимался на каждый)
	n = max(n, s.treeNeed(item, f.Researched)-s.inv(item))
	crafts := (n + s.perCraft(item) - 1) / s.perCraft(item)
	need := map[string]int{}
	for _, in := range rec.Ingredients {
		need[in.Name] = int(math.Ceil(in.Amount*float64(crafts) - 1e-9))
	}
	need[mitem]++
	for _, it := range slices.Sorted(maps.Keys(need)) {
		if err := s.have(ctx, it, need[it]); err != nil {
			return err
		}
	}
	pos, pole, err := s.machineSpot(m.Name)
	if err != nil {
		return err
	}
	s.h.Say("%s руками не сделать, кластера ещё нет — временный %s: ×%d", item, m.Name, n)
	var pe *bridge.Entity // свой столб к нему (у стоящих столбов места нет)
	if pole != nil {
		if err := s.have(ctx, smallPole, 1); err != nil {
			return err
		}
		e, err := s.h.Place(ctx, smallPole, *pole, layout.N)
		if err != nil {
			return err
		}
		pe = &e
	}
	e, err := s.h.Place(ctx, mitem, pos, layout.N)
	if err != nil {
		return err
	}
	if err := s.h.SetRecipe(ctx, e, recipe); err != nil {
		return err
	}
	for _, in := range rec.Ingredients {
		if err := s.h.Insert(ctx, e, in.Name, need[in.Name]); err != nil {
			return err
		}
	}
	limit := time.Duration(float64(crafts)*rec.Energy/max(m.Speed, 0.1)*float64(time.Second)) + time.Minute
	deadline := time.Now().Add(limit)
	for {
		st, err := s.h.B.GetEntity(e.Name, e.Position)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("временный %s пропал", e.Name)
		}
		got := 0
		for _, inv := range st.Inventories {
			got += inv[item]
		}
		if got >= n {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("временный %s: %s ×%d не готов за %v (статус %s)", e.Name, item, n, limit, st.Status)
		}
		if err := sleepCtx(ctx, 3*time.Second); err != nil {
			return err
		}
	}
	if _, err := s.h.Take(ctx, e); err != nil {
		return err
	}
	if err := s.h.Pickup(ctx, e); err != nil {
		return err
	}
	if pe != nil {
		return s.h.Pickup(ctx, *pe)
	}
	return nil
}

// smallPole — столб к временному сборщику, если у стоящих столбов места нет.
const smallPole = "small-electric-pole"

// treeNeed — сколько item (руками не делается) уходит на всё недостроенное стадии (s.all), по
// дереву ручных рецептов того, что ещё не в инвентаре; не по участкам — 0.
func (s *worker) treeNeed(item string, researched []string) int {
	if s.all == nil {
		return 0
	}
	left, err := notBuilt(s.h, s.all)
	if err != nil {
		return 0
	}
	count := map[string]int{}
	for _, pl := range left {
		count[s.h.Protos.ItemFor(pl.Name)]++
	}
	av := s.h.Protos.NewAvailability(researched, false)
	need := 0.0
	for it, k := range count {
		if k -= s.inv(it); k > 0 && it != item {
			need += calc.Handcraft(s.h.Protos, av, it, float64(k)).Base[item]
		}
	}
	return int(math.Ceil(need - 1e-9))
}

// machineSpot — место временного сборщика name: у хранения (дальше — у любых своих столбов,
// ближние к хранению первыми) в зоне питания своего столба, все тайлы свободны; нет — в пределах
// провода от столба, и pole — место своего столба рядом с ним.
func (s *worker) machineSpot(name string) (pos bridge.Pos, pole *bridge.Pos, err error) {
	near, err := s.h.B.Player()
	if err != nil {
		return pos, nil, err
	}
	at := near.Position
	if list, err := storageNear(s.h); err == nil && len(list) > 0 {
		at = list[0].Pos
	}
	poles, err := s.h.B.FindNearest(bridge.FindArgs{Type: "electric-pole", Radius: 400, Position: &at, Limit: 400})
	if err != nil {
		return pos, nil, err
	}
	e := s.h.Protos.Entities[name]
	w, hh := max(e.TileWidth, 1), max(e.TileHeight, 1)
	for _, p := range poles {
		if pos, ok := s.h.World.FreeSpot(p.Position, w, hh, 3, &near.Position); ok {
			return pos, nil, nil
		}
	}
	// площадка с полосой в тайл вокруг (там столб), центр не дальше провода от столба
	reach := s.h.Protos.Entities[smallPole].MaxWireDistance
	for _, p := range poles {
		big, ok := s.h.World.FreeSpot(p.Position, w+2, hh+2, int(reach)-2, &near.Position)
		if !ok {
			continue
		}
		q := bridge.Pos{X: big.X - float64(w+2)/2 + 0.5, Y: math.Floor(big.Y) + 0.5} // столб — левая полоса площадки
		if math.Hypot(q.X-p.Position.X, q.Y-p.Position.Y) > reach {
			continue
		}
		return big, &q, nil
	}
	return pos, nil, fmt.Errorf("временный %s: у своих столбов нет места", name)
}

// craft — скрафтить n item: сначала сырьё на всё дерево (пластины, камень, дерево). Уже в
// очереди крафта — только дождаться; с noWait очередь уже вычтена из n (have, pending) — n
// сверх неё (вычитали дважды: крафт частями не ставил ничего, «не набрать transport-belt
// ×4834», стадия 14 на 427965634).
func (s *worker) craft(ctx context.Context, item string, n int) error {
	if !s.noWait && s.queued(item)*s.perCraft(item) >= n {
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
		av := s.h.Protos.NewAvailability(f.Researched, false)
		raw := func(it string) bool {
			return it == "iron-plate" || it == "copper-plate" || it == "stone" || it == "wood"
		}
		crafts := float64((n + per - 1) / per)
		// по ингредиентам: готовое — из инвентаря и буферов выхода своих кластеров, крафт руками
		// (и пластины на него) — только на недостающее (stages.md, инвентарь: шестерни и схемы
		// в буферах лежали тысячами, а бур крафтился из пластин)
		for _, in := range s.h.Protos.Recipes[item].Ingredients {
			k := int(math.Ceil(in.Amount*crafts - 1e-9))
			if raw(in.Name) {
				need[in.Name] += k
				continue
			}
			if s.useStore && s.inv(in.Name) < k {
				if _, err := s.fromProducts(ctx, in.Name, k-s.inv(in.Name)); err != nil {
					return err
				}
			}
			lack := k - s.inv(in.Name)
			if lack <= 0 {
				continue
			}
			for r, v := range calc.Handcraft(s.h.Protos, av, in.Name, float64(lack)).Base {
				if raw(r) {
					need[r] += int(math.Ceil(v - 1e-9))
					continue
				}
				if !s.useStore {
					return fmt.Errorf("крафт %s: нужно %s — руками не сделать", item, r)
				}
				// руками не сделать (сталь) — из буферов выхода кластеров, дождаться (have)
				if err := s.have(ctx, r, s.inv(r)+int(math.Ceil(v-1e-9))); err != nil {
					return err
				}
			}
		}
	}
	// сырьё на всю партию не влезет в инвентарь (стадия 30 на 859088572: 316 буров — ~7 тыс.
	// железа, набрал до полного и встал) — частями: партия, на которую сырью хватит места;
	// остальное — следующим заходом have
	if per := s.perCraft(item); n > per {
		slotsFor := func(it string, k int) int {
			st := max(s.h.Protos.Items[it].StackSize, 1)
			return (k + st - 1) / st
		}
		used := slotsFor(item, n)
		for raw, k := range need {
			if lack := k - s.inv(raw); lack > 0 {
				used += slotsFor(raw, lack)
			}
		}
		if room := max(s.freeSlots()-4, 1); used > room {
			if part := max(per, n*room/used/per*per); part < n {
				s.h.Say("крафт %s ×%d — частями по %d: сырьё на всё не влезет", item, n, part)
				return s.craft(ctx, item, part)
			}
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
	} else if s.all != nil {
		// стройка стадии: в очередь — на всё, что осталось поставить и на что есть сырьё в
		// инвентаре (и место под готовое); ждать — только пачку: остальное крафтится, пока бот
		// ставит (questions.md: «строить, пока крафтится»; крафт стоял 303 мин на двух картах)
		missing := s.remaining(item) - s.queued(item)
		room := max(s.freeSlots()-4, 0) * max(s.h.Protos.Items[item].StackSize, 1)
		total = max(n, min(missing, s.affordable(item), room))
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

// remaining — сколько item ещё поставить на всю стройку стадии (s.all, по участкам) сверх того,
// что в инвентаре; не по участкам — 0.
func (s *worker) remaining(item string) int {
	if s.all == nil {
		return 0
	}
	var these []layout.Placement
	for _, pl := range s.all {
		if s.h.Protos.ItemFor(pl.Name) == item {
			these = append(these, pl)
		}
	}
	left, err := notBuilt(s.h, these)
	if err != nil {
		return 0
	}
	return len(left) - s.inv(item)
}

// affordable — сколько item можно скрафтить руками из сырья, что уже в инвентаре (по дереву
// рецептов, без учёта полуфабрикатов в инвентаре — осторожно).
func (s *worker) affordable(item string) int {
	f, err := s.h.B.Force()
	if err != nil {
		return 0
	}
	per := max(s.perCraft(item), 1)
	base := calc.Handcraft(s.h.Protos, s.h.Protos.NewAvailability(f.Researched, false), item, float64(per)).Base
	crafts := math.Inf(1)
	for raw, v := range base {
		if v > 0 {
			crafts = math.Min(crafts, float64(s.inv(raw))/v)
		}
	}
	if math.IsInf(crafts, 1) {
		return 0
	}
	return int(crafts) * per
}

// remainingRaw — сколько сырья raw (пластины, камень, дерево) нужно на крафт всего, что
// осталось построить в стадии (без того, что уже в инвентаре): за сырьём к хранению — один
// заход на стройку, не на пачку (стадия 18 на 267145981: за 21–74 пластинами на каждые 40
// лент — 30 ходок по 400 тайлов к хранению и обратно).
func (s *worker) remainingRaw(raw string) int {
	if s.all == nil {
		return 0
	}
	left, err := notBuilt(s.h, s.all)
	if err != nil {
		return 0
	}
	count := map[string]int{}
	for _, pl := range left {
		count[s.h.Protos.ItemFor(pl.Name)]++
	}
	f, err := s.h.B.Force()
	if err != nil {
		return 0
	}
	av := s.h.Protos.NewAvailability(f.Researched, false)
	total := 0.0
	for item, n := range count {
		if n -= s.inv(item); n <= 0 || item == raw {
			continue
		}
		if _, ok := s.h.Protos.Recipes[item]; !ok {
			continue
		}
		total += calc.Handcraft(s.h.Protos, av, item, float64(n)).Base[raw]
	}
	return int(math.Ceil(total-1e-9)) - s.inv(raw)
}

// fromProducts — готовое item из буферов выхода своих кластеров (раскладки стадий, тег
// «outbuf/…»; stages.md, инвентарь): сколько не хватает (n) и стопку запаса, не больше, чем
// влезет; с ближних. true — что-то взято.
func (s *worker) fromProducts(ctx context.Context, item string, n int) (bool, error) {
	var bufs []layout.Placement
	seen := map[layout.Placement]bool{}
	names, lists := stateLayouts()
	for _, base := range names {
		for _, pl := range lists[base] {
			if strings.HasPrefix(pl.Tag, "outbuf/") && strings.HasSuffix(pl.Name, "chest") && !seen[pl] {
				seen[pl] = true
				bufs = append(bufs, pl)
			}
		}
	}
	if len(bufs) == 0 {
		return false, nil
	}
	states, err := entityStates(s.h, bufs)
	if err != nil {
		return false, err
	}
	var es []bridge.Entity
	for _, st := range states {
		if st != nil && st.Inventories["chest"][item] > 0 && !s.far[st.Position] {
			es = append(es, st.Entity)
		}
	}
	if len(es) == 0 {
		return false, nil
	}
	p, err := s.h.B.Player()
	if err != nil {
		return false, err
	}
	sort.Slice(es, func(i, j int) bool { return dist2(es[i].Position, p.Position) < dist2(es[j].Position, p.Position) })
	stack := max(s.h.Protos.Items[item].StackSize, 1)
	room := (s.freeSlots() - 2) * stack
	if room <= 0 {
		return false, nil
	}
	before := s.inv(item)
	want := before + min(max(n, 1)+stack, room)
	for _, e := range es {
		if s.inv(item) >= want {
			break
		}
		if err := s.h.TakeItem(ctx, e, item, want-s.inv(item)); err != nil {
			if errors.Is(err, act.ErrNotReached) && ctx.Err() == nil {
				// до буфера не дойти (внутри кольца своих построек: стадия 30 на 859088572) —
				// следующий; нигде — крафт руками
				s.h.Say("до буфера %s не дойти — дальше", item)
				if s.far == nil {
					s.far = map[bridge.Pos]bool{}
				}
				s.far[e.Position] = true
				continue
			}
			return false, err
		}
	}
	return s.inv(item) > before, nil
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
	// не больше, чем влезет (2 ячейки — про запас): из полного сундук отдаёт 0 — ошибка
	room := (s.freeSlots() - 2) * max(s.h.Protos.Items[plate].StackSize, 1)
	if room <= 0 {
		return nil // место освободит have (хранение)
	}
	want = min(want, s.inv(plate)+room)
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

// build — поставить недостающее из list: по видам в порядке первого появления, каждый вид —
// скрафтить разом и поставить.
func (s *worker) build(ctx context.Context, list []layout.Placement) error {
	var names []string
	for _, pl := range list {
		if !slices.Contains(names, pl.Name) {
			names = append(names, pl.Name)
		}
	}
	for _, name := range names {
		var these []layout.Placement
		for _, pl := range list {
			if pl.Name == name {
				these = append(these, pl)
			}
		}
		todo, err := notBuilt(s.h, these) // пачками: по одной — тысячи запросов на стадии 30
		if err != nil {
			return err
		}
		if strings.HasPrefix(name, "underground") {
			// стоящие не тем концом (вход/выход не как в раскладке; беспорядок прошлых заходов) —
			// снять и поставить заново по порядку: новые иначе встают в пары с ними (вход спарен
			// с будущим входом — «встал не тем направлением», стадия 30 на 260647970). Проверять и
			// когда строить нечего: пара прошлой стадии оба конца «входом» — по имени и
			// направлению стоит, лента оборвана (стадия 40 на 427965634: сера на bus-3 не шла)
			if fixed, err := s.unpairWrong(ctx, these); err != nil {
				return err
			} else if fixed {
				if todo, err = notBuilt(s.h, these); err != nil {
					return err
				}
			}
			todo = ugPairs(todo)
		}
		if len(todo) == 0 {
			continue
		}
		// одинаковое — подряд одним вызовом (рука не меняется между постановками), пачками по
		// buildBatch: между пачками — место в инвентаре под срубленное. Добирать — сколько
		// влезает в инвентарь, не всё сразу (1244 лент в него не помещались — 1028292478,
		// стадия 16)
		for len(todo) > 0 {
			n := min(len(todo), buildBatch)
			if s.inv(name) < n && s.useStore {
				// к хранению — за всем, что нужно на оставшуюся стройку, одним заходом (stages.md,
				// инвентарь); чего там нет — have (крафт)
				all, from := list, name
				if s.all != nil { // по участкам — на всю оставшуюся стройку стадии
					all, from = s.all, s.all[0].Name
				}
				if err := s.restock(ctx, all, from); err != nil {
					return err
				}
			}
			if s.inv(name) < n {
				if err := s.have(ctx, name, min(len(todo), max(n, s.roomFor(name)))); err != nil {
					return err
				}
			}
			batch := todo[:min(n, s.inv(name))]
			if len(batch) == 0 {
				return fmt.Errorf("нет %s в инвентаре для стройки", name)
			}
			todo = todo[len(batch):]
			if s.useStore { // место под срубленное на месте постройки (деревья, камни): в
				// хранение — только сырьё с вырубки, постройки и запас стадии остаются
				p, err := s.h.B.Player()
				if err != nil {
					return err
				}
				if err := s.store(ctx, freeSlots, keepBuilt(p)...); err != nil {
					return err
				}
			}
			pos, dirs := make([]bridge.Pos, len(batch)), make([]int, len(batch))
			for i, pl := range batch {
				pos[i], dirs[i] = pl.Pos, pl.Dir
			}
			_, err := s.h.PlaceDirs(ctx, name, pos, dirs)
			for tries := 0; errors.Is(err, act.ErrNoItem) && tries < 3; tries++ {
				// предмет ушёл в хранение по дороге (OnFull: место под подъём своей постройки) —
				// добрать и ещё раз, только не поставленное (260647970, стадия 16)
				pos, dirs = pos[:0], dirs[:0]
				for _, pl := range batch {
					if e, eerr := s.entity(pl); eerr == nil && e == nil {
						pos, dirs = append(pos, pl.Pos), append(dirs, pl.Dir)
					}
				}
				if len(pos) == 0 {
					err = nil
					break
				}
				if herr := s.have(ctx, name, min(len(pos), s.roomFor(name))); herr != nil {
					return herr
				}
				_, err = s.h.PlaceDirs(ctx, name, pos, dirs)
			}
			if err != nil {
				return fmt.Errorf("поставить %s ×%d с (%.1f, %.1f): %w", name, len(batch), pos[0].X, pos[0].Y, err)
			}
		}
		if s.placed != nil {
			if err := s.placed(ctx, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// restock — один заход к хранению за всем, что нужно на оставшуюся стройку list начиная с вида
// from (stages.md, инвентарь; просьба пользователя 27.10: раньше — ходка за каждым видом,
// на стадии 16 восемь): по видам в порядке стройки, недостающее в инвентаре — из сундуков
// хранения, пока есть свободные ячейки (freeSlots — под срубленное). Чего в хранении нет —
// не берёт (have потом скрафтит).
func (s *worker) restock(ctx context.Context, list []layout.Placement, from string) error {
	var names []string
	for _, pl := range list {
		if !slices.Contains(names, pl.Name) {
			names = append(names, pl.Name)
		}
	}
	if i := slices.Index(names, from); i > 0 {
		names = names[i:]
	}
	var rest []layout.Placement
	for _, pl := range list {
		if slices.Contains(names, pl.Name) {
			rest = append(rest, pl)
		}
	}
	left, err := notBuilt(s.h, rest)
	if err != nil {
		return err
	}
	need := map[string]int{}
	for _, pl := range left {
		need[s.h.Protos.ItemFor(pl.Name)]++
	}
	chests, err := storageNear(s.h)
	if err != nil {
		return nil // хранения ещё нет — как раньше (have)
	}
	type stock struct {
		e    bridge.Entity
		have map[string]int
	}
	var store []stock
	for _, pl := range chests {
		st, err := s.h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		store = append(store, stock{st.Entity, st.Inventories["chest"]})
	}
	took := false
	for _, name := range names {
		it := s.h.Protos.ItemFor(name)
		want := need[it] - s.inv(it)
		if want <= 0 {
			continue
		}
		stack := max(s.h.Protos.Items[it].StackSize, 1)
		if room := (s.freeSlots() - freeSlots) * stack; room < want {
			want = room
		}
		for _, c := range store {
			if want <= 0 {
				break
			}
			n := min(want, c.have[it])
			if n <= 0 {
				continue
			}
			if !took { // перед первым взятием — ненужное в хранение (место)
				if err := s.tidy(ctx, it); err != nil && !errors.Is(err, errNoStorage) {
					return err
				}
				took = true
			}
			before := s.inv(it)
			if err := s.h.TakeItem(ctx, c.e, it, n); err != nil {
				return err
			}
			got := s.inv(it) - before
			c.have[it] -= got
			want -= got
			if got == 0 {
				break // инвентарь полон
			}
		}
	}
	return nil
}

// roomFor — сколько item поместится в инвентарь: что уже есть плюс свободные ячейки пачками.
func (s *worker) roomFor(item string) int {
	p, err := s.h.B.Player()
	if err != nil {
		return 0
	}
	stack := max(s.h.Protos.Items[item].StackSize, 1)
	return p.Inventory[item] + (p.InventorySize-len(p.InventorySlots))*stack
}

// unpairWrong — подземки list, что стоят не тем концом (ug_type ≠ вход/выход раскладки), —
// снять; true — снято что-то.
func (s *worker) unpairWrong(ctx context.Context, list []layout.Placement) (bool, error) {
	states, err := entityStates(s.h, list)
	if err != nil {
		return false, err
	}
	var wrong []bridge.Entity
	for i, st := range states {
		if st != nil && list[i].Recipe != "" && st.UGType != "" && st.UGType != list[i].Recipe {
			wrong = append(wrong, st.Entity)
		}
	}
	if len(wrong) == 0 {
		return false, nil
	}
	s.h.Say("%d подземок стоят не тем концом — снимаю и ставлю по порядку", len(wrong))
	return true, s.h.PickupAll(ctx, wrong)
}

// fromStorage — item лежит в сундуке хранения: взять оттуда n (только его, пачками — не
// сундук целиком) и, раз стоим у хранения, выложить ненужное (stages.md, инвентарь). false — в
// хранении нет.
func (s *worker) fromStorage(ctx context.Context, item string, n int) (bool, error) {
	list, err := storageNear(s.h)
	if err != nil {
		return false, err
	}
	for _, pl := range list {
		st, err := s.h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil || st.Inventories["chest"][item] == 0 {
			continue
		}
		if err := s.tidy(ctx, item); err != nil && !errors.Is(err, errNoStorage) {
			return false, err
		}
		// не больше, чем влезет (2 ячейки — про запас): берём теперь на всю стройку стадии
		room := (s.freeSlots() - 2) * max(s.h.Protos.Items[item].StackSize, 1)
		if room <= 0 {
			return false, nil
		}
		before := s.inv(item)
		if err := s.h.TakeItem(ctx, st.Entity, item, min(n, room)); err != nil {
			return false, err
		}
		return s.inv(item) > before, nil
	}
	return false, nil
}
