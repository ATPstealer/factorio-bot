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
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 2 (stages.md): добывающие точки на твердотопливных бурах — outpostCoal угольных пар
// (буры носом друг в друга), outpostIron пар «бур → печь» на железе, outpostCopper на меди, 4
// бура носом в сундук на камне.

// Точки стадии 2: железа на стройку добычи, плавки и шины нужно ~11 000 — 30 пар копят его за
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
	coalBack     = 5  // забрав уголь с пары, вернуть каждому буру
)

// chest — сундук стадий: деревянный до обслуживания, с него (стадия 17) — железный (stage.Do).
var chest = "wooden-chest"

// outpostSite — точка: вид (coal, iron-ore, copper-ore, stone) и её постройки по порядку стройки.
type outpostSite struct {
	Ore   string             `json:"ore"`
	Items []layout.Placement `json:"items"`
}

func init() {
	register(Stage{N: 2, Name: "добывающие точки на твердотопливных бурах", Run: runOutposts, Check: checkOutposts})
}

// outpostsLayout — раскладка всех точек у ближайших месторождений; один раз на карту (файл).
func outpostsLayout(h *act.Hands) ([]outpostSite, error) {
	if data, err := os.ReadFile(runPath(outpostsFile)); err == nil {
		var sites []outpostSite
		if json.Unmarshal(data, &sites) == nil && len(sites) > 0 {
			return sites, nil
		}
	}
	if _, err := h.World.Sync(h.B, true); err != nil {
		return nil, err
	}
	p, err := h.B.Player()
	if err != nil {
		return nil, err
	}
	near := world.TileOf(p.Position)
	// patches — пятна ore (не меньше min тайлов) по близости к старту: сначала в пределах
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
			d := math.Hypot(pt.Center.X-p.Position.X, pt.Center.Y-p.Position.Y)
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
	if data, err := json.MarshalIndent(sites, "", " "); err == nil {
		os.WriteFile(runPath(outpostsFile), data, 0o644)
	}
	return sites, nil
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
	useStore bool // брать из кластера хранения (стадии после 8)
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
			if e == nil {
				if err := s.have(ctx, pl.Name, 1); err != nil {
					return err
				}
				placed, err := h.Place(ctx, pl.Name, pl.Pos, pl.Dir)
				if err != nil {
					return fmt.Errorf("поставить %s в (%.1f, %.1f): %w", pl.Name, pl.Pos.X, pl.Pos.Y, err)
				}
				e = &placed
			}
			if pl.Name != chest {
				if err := s.fuel(ctx, *e); err != nil {
					return err
				}
			}
		}
	}
	// ранние точки за время стройки сожгли топливо — в конце обойти все и долить до полного
	// (пользователь: по 50); во время стройки — по fuelLevel, угля в начале мало
	for _, si := range outpostOrder(sites) {
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
	for tries := 0; s.inv(item) < n; tries++ {
		if tries > 40+n/10 { // много — дольше (за круг с точек — десятки)
			return fmt.Errorf("не набрать %s ×%d (есть %d)", item, n, s.inv(item))
		}
		lack := n - s.inv(item)
		if s.useStore {
			if got, err := s.fromStorage(ctx, item); err != nil || got {
				if err != nil {
					return err
				}
				continue
			}
		}
		var err error
		switch item {
		case "iron-plate", "copper-plate":
			if s.useStore { // точек стадии 2 уже нет — из буферов выхода плавки, ждать их
				err = s.fromBuffers(ctx, item)
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
		return s.waitInv(ctx, item, s.inv(item)+n, s.craftLimit(item, 0))
	}
	need := map[string]int{}
	switch item {
	case drill: // 3 шестерни (6 железа) + 3 железа + печь (5 камня)
		need["iron-plate"], need["stone"] = 9*n, 5*n
	case furnace:
		need["stone"] = 5 * n
	case chest:
		need["wood"] = 2 * n
	default: // по дереву ручных рецептов
		f, err := s.h.B.Force()
		if err != nil {
			return err
		}
		hc := calc.Handcraft(s.h.Protos, s.h.Protos.NewAvailability(f.Researched, false), item, float64(n))
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

// queued — сколько item уже в очереди крафта.
func (s *worker) queued(item string) int {
	p, err := s.h.B.Player()
	if err != nil {
		return 0
	}
	n := 0
	for _, q := range p.CraftingQueue {
		if q.Recipe == item {
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
	if ds := s.built("coal", drill); len(ds) > 0 {
		got, err := s.takeAll(ctx, ds, "fuel")
		if err != nil {
			return err
		}
		if got > 0 { // пары без топлива встанут — каждому буру немного назад (поровну из того,
			// что есть: забрали мало — меньше coalBack каждому, хоть по одному)
			have := s.inv("coal")
			for i, d := range ds {
				back := min(coalBack, have/len(ds))
				if back == 0 && i < have {
					back = 1
				}
				if back == 0 {
					continue
				}
				if err := s.h.Insert(ctx, d, "coal", back); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if _, err := s.h.Gather(ctx, "huge-rock", 1, 60); err == nil {
		return nil
	}
	_, err := s.h.Gather(ctx, "coal", lack, 300)
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
				st, err := h.B.GetEntity(pl.Name, pl.Pos)
				if err != nil {
					return err
				}
				if st == nil || len(st.Inventories["fuel"]) == 0 && st.Status != "working" {
					return fmt.Errorf("%s точки %s в (%.1f, %.1f) без топлива", pl.Name, site.Ore, pl.Pos.X, pl.Pos.Y)
				}
			}
		}
	}
	want := map[string]int{"coal": 2 * outpostCoal, "iron-ore": outpostIron, "copper-ore": outpostCopper, "stone": 4}
	for ore, n := range want {
		if count[ore] != n {
			return fmt.Errorf("буров на %s: %d из %d", ore, count[ore], n)
		}
	}
	return nil
}

// fromBuffers — пластины из буферов выхода плавки (стадия 12): забрать со всех, где есть;
// нигде нет — подождать.
func (s *worker) fromBuffers(ctx context.Context, plate string) error {
	data, err := os.ReadFile(runPath(smeltingFile))
	if err != nil {
		return fmt.Errorf("%s: нет буферов выхода плавки (стадия 12): %w", plate, err)
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
	_, err = s.takeAll(ctx, es, "chest")
	return err
}
