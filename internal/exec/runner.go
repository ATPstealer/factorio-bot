// Package exec — главный цикл бота: состояние из игры → следующий шаг плана → руки.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/plan"
	"factorio-bot/internal/world"
)

// GoalSpec — цель в сохраняемом виде.
type GoalSpec struct {
	Kind     string `json:"kind"` // have | smelt | build | research | start | redscience
	Item     string `json:"item,omitempty"`
	N        int    `json:"n"`
	Fuel     string `json:"fuel,omitempty"`
	Maintain bool   `json:"maintain,omitempty"` // после выполнения — следить и чинить
	// build: раскладка (Item — её метка)
	Layout []layout.Placement `json:"layout,omitempty"`
}

func (g GoalSpec) Goal() (plan.Goal, error) {
	switch g.Kind {
	case "have":
		return plan.HaveItem{Item: g.Item, N: g.N}, nil
	case "smelt":
		fuel := g.Fuel
		if fuel == "" {
			fuel = "coal"
		}
		return plan.SmeltingLine{Ore: g.Item, Fuel: fuel, N: g.N}, nil
	case "build":
		return plan.Built{Tag: g.Item, List: g.Layout}, nil
	case "research":
		return plan.Researched{Tech: g.Item}, nil
	case "start":
		return plan.Start{}, nil
	case "redscience":
		return plan.RedScience{}, nil
	}
	return nil, fmt.Errorf("exec: неизвестная цель %q", g.Kind)
}

// Saved — состояние бота между запусками.
type Saved struct {
	Goals   []GoalSpec    `json:"goals"`
	Owned   []plan.Owned  `json:"owned"`
	Planner *plan.Planner `json:"planner"`
	// Designs — раскладки под задачи (plan.State.Design): раз спроектировав, не менять
	Designs map[string][]layout.Placement `json:"designs,omitempty"`
	// Done — выполненные разовые этапы (plan.State.Done)
	Done map[string]bool `json:"done,omitempty"`
}

func Load(path string) (*Saved, error) {
	s := &Saved{Planner: plan.NewPlanner()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Planner == nil {
		s.Planner = plan.NewPlanner()
	}
	return s, nil
}

// Runner — цикл: состояние → шаг → руки.
type Runner struct {
	H         *act.Hands
	B         *bridge.Bridge
	Saved     *Saved
	StatePath string
	LogPath   string
	Idle      time.Duration // пауза, когда поддерживаемые цели выполнены
	MaxFails  int           // отказов подряд — остановка

	logf *os.File
	// Until — проверяется между шагами: true — Run завершается (тесты, отладка)
	Until    func() bool
	minables map[string]bool
	stores   []plan.Owned // сундуки вне реестра из последнего снимка
	fails    int
	explored map[bridge.ChunkPos]bool // края карты, куда уже ходили на разведку
}

func (r *Runner) Save() error {
	if r.StatePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(r.Saved, "", " ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(r.StatePath), 0o755)
	return os.WriteFile(r.StatePath, data, 0o644)
}

// journal — строка в run/bot.log: время, что, почему, чем кончилось.
func (r *Runner) journal(format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05") + " " + fmt.Sprintf(format, args...)
	r.H.Log("%s", line)
	if r.LogPath == "" {
		return
	}
	if r.logf == nil {
		os.MkdirAll(filepath.Dir(r.LogPath), 0o755)
		f, err := os.OpenFile(r.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		r.logf = f
	}
	fmt.Fprintln(r.logf, line)
}

func (r *Runner) Close() {
	if r.logf != nil {
		r.logf.Close()
	}
}

// Snapshot — состояние из игры: игрок, сила, мир, свои постройки (исчезнувшие — из реестра долой).
func (r *Runner) Snapshot() (*plan.State, error) {
	p, err := r.B.Player()
	if err != nil {
		return nil, err
	}
	f, err := r.B.Force()
	if err != nil {
		return nil, err
	}
	if _, err := r.H.World.Sync(r.B, false); err != nil {
		return nil, err
	}
	inv := map[string]int{}
	for k, v := range p.Inventory {
		inv[k] = v
	}
	if p.Cursor != nil {
		inv[p.Cursor.Name] += p.Cursor.Count
	}
	s := &plan.State{Protos: r.H.Protos, Av: r.H.Protos.NewAvailability(f.Researched, false),
		Inventory: inv, PlayerPos: p.Position, FindSpot: r.findSpot, Minable: r.minable, Produced: f.Produced,
		Researching: map[string]bool{}, Design: r.design, Done: r.Saved.Done}
	if f.Current != "" {
		s.Researching[f.Current] = true
	}
	for _, q := range f.Queue {
		s.Researching[q] = true
	}
	var kept []plan.Owned
	for _, o := range r.Saved.Owned {
		st, err := r.B.GetEntity(o.Name, o.Pos)
		if err != nil {
			return nil, err
		}
		if st == nil {
			r.journal("постройка %s (%s) в (%.1f, %.1f) исчезла", o.Name, o.Role, o.Pos.X, o.Pos.Y)
			r.H.Say("%s (%s) исчезла — восстановлю", o.Name, o.Role)
			continue
		}
		o.Inv, o.Status, o.Dir, o.Recipe = st.Inventories, st.Status, st.Direction, st.Recipe
		if o.Inv == nil {
			o.Inv = map[string]map[string]int{}
		}
		kept = append(kept, o)
	}
	r.Saved.Owned = kept
	if err := r.adopt(); err != nil {
		return nil, err
	}
	for i := range r.Saved.Owned {
		s.Owned = append(s.Owned, &r.Saved.Owned[i])
	}
	r.minables = map[string]bool{}
	if err := r.findStores(s); err != nil {
		return nil, err
	}
	return s, nil
}

// findStores — сундуки и печи рядом вне реестра, в которых есть что забрать (игрок видит их
// и может открыть): поставленные руками, обломки корабля, чужие для реестра печи.
func (r *Runner) findStores(s *plan.State) error {
	var es []bridge.Entity
	for _, typ := range []string{"container", "furnace"} {
		found, err := r.B.FindNearest(bridge.FindArgs{Type: typ, Radius: 64, Limit: 30})
		if err != nil {
			return err
		}
		es = append(es, found...)
	}
	mine := map[bridge.Pos]bool{}
	for _, o := range r.Saved.Owned {
		mine[o.Pos] = true
	}
	r.stores = nil
	for _, e := range es {
		if mine[e.Position] {
			continue
		}
		st, err := r.B.GetEntity(e.Name, e.Position)
		if err != nil {
			return err
		}
		if st == nil || len(st.Inventories["chest"]) == 0 && len(st.Inventories["crafter_output"]) == 0 {
			continue
		}
		r.stores = append(r.stores, plan.Owned{Unit: e.UnitNumber, Name: e.Name, Pos: e.Position,
			Role: plan.StoreRole(e.Position), Inv: st.Inventories})
	}
	for i := range r.stores {
		s.Stores = append(s.Stores, &r.stores[i])
	}
	return nil
}

// minable — можно ли добыть предмет с открытой карты (кэш на цикл).
func (r *Runner) minable(item string) bool {
	if v, ok := r.minables[item]; ok {
		return v
	}
	es, err := r.B.FindMinable(item, 300, 1)
	ok := err == nil && len(es) > 0
	r.minables[item] = ok
	return ok
}

// group — роль без номера: smelt-iron-ore-3 → smelt-iron-ore.
func group(role string) string {
	if i := strings.LastIndex(role, "-"); i > 0 {
		if _, err := fmt.Sscanf(role[i+1:], "%d", new(int)); err == nil {
			return role[:i]
		}
	}
	return role
}

// findSpot — место постройки: рядом с остальными постройками той же группы (в ряд),
// первая печь линии — у ближайшего к игроку края патча её руды; прочее — рядом с игроком.
func (r *Runner) findSpot(s *plan.State, entity, role string, avoid []bridge.Pos) (bridge.Pos, bool) {
	w := r.H.World
	e := r.H.Protos.Entities[entity]
	tw, th := max(e.TileWidth, 1), max(e.TileHeight, 1)
	// занято: забаненные места и свои постройки из состояния (в симуляции плана карта
	// о них ещё не знает)
	taken := append([]bridge.Pos(nil), avoid...)
	for _, o := range s.Owned {
		taken = append(taken, o.Pos)
	}
	bad := func(t world.Tile) bool {
		c := t.Center()
		for _, a := range taken {
			if math.Abs(c.X-a.X) < float64(tw) && math.Abs(c.Y-a.Y) < float64(th) {
				return true
			}
		}
		return false
	}
	noOre := func(t world.Tile) bool { _, ore := w.ResourceAt(t); return !ore && !bad(t) }

	// площадка добычи: бур — на руде, сундук — на его выходе
	if strings.HasPrefix(role, "mine-") {
		if strings.HasSuffix(role, "-out") {
			drill := s.Role(strings.TrimSuffix(role, "-out"))
			if drill == nil {
				return bridge.Pos{}, false
			}
			out := r.dropTile(drill.Name, drill.Pos)
			if w.TileAt(out) != world.Land || w.Blocked(out) || bad(out) {
				return bridge.Pos{}, false
			}
			return out.Center(), true
		}
		ore := strings.TrimPrefix(group(role), "mine-")
		return r.drillSpot(s, entity, ore, bad)
	}

	var anchor bridge.Pos
	var sibling *plan.Owned
	for _, o := range s.Owned {
		if group(o.Role) == group(role) {
			sibling = o
		}
	}
	switch {
	case sibling != nil:
		anchor = bridge.Pos{X: sibling.Pos.X + float64(tw), Y: sibling.Pos.Y}
	case strings.HasPrefix(role, "smelt-") || role == "smelter":
		// линия — у своей руды; печь для плавки пластин — у железа (пластины в основном
		// железные): руда и печь рядом, меньше ходить
		ore := strings.TrimPrefix(group(role), "smelt-")
		if role == "smelter" {
			ore = "iron-ore"
		}
		best := math.Inf(1)
		for _, pt := range w.Patches() {
			if pt.Name != ore || len(pt.Tiles) < 20 {
				continue
			}
			for _, t := range pt.Tiles {
				if d := math.Hypot(t.Center().X-s.PlayerPos.X, t.Center().Y-s.PlayerPos.Y); d < best {
					best, anchor = d, t.Center()
				}
			}
		}
		if math.IsInf(best, 1) {
			if role != "smelter" {
				return bridge.Pos{}, false
			}
			anchor = s.PlayerPos // железа не видно — печь для плавки рядом с игроком
		}
	default:
		anchor = s.PlayerPos
	}
	return w.FreeSpotWhere(anchor, tw, th, 15, &s.PlayerPos, noOre)
}

// dropTile — тайл, куда бур (направление «север») выдаёт добытое.
func (r *Runner) dropTile(entity string, pos bridge.Pos) world.Tile {
	v := r.H.Protos.Entities[entity].VectorToPlaceResult
	if len(v) < 2 {
		v = []float64{0, -1.3}
	}
	return world.TileOf(bridge.Pos{X: pos.X + v[0], Y: pos.Y + v[1]})
}

// drillSpot — центр бура tw×th, все тайлы которого на руде ore, свободны, и тайл выхода
// перед ним тоже свободен (туда сундук); ближайший к игроку.
func (r *Runner) drillSpot(s *plan.State, entity, ore string, bad func(world.Tile) bool) (bridge.Pos, bool) {
	w := r.H.World
	e := r.H.Protos.Entities[entity]
	tw, th := max(e.TileWidth, 1), max(e.TileHeight, 1)
	var tiles []world.Tile
	for _, pt := range w.Patches() {
		if pt.Name == ore {
			tiles = append(tiles, pt.Tiles...)
		}
	}
	sort.Slice(tiles, func(i, j int) bool {
		a, b := tiles[i].Center(), tiles[j].Center()
		return math.Hypot(a.X-s.PlayerPos.X, a.Y-s.PlayerPos.Y) < math.Hypot(b.X-s.PlayerPos.X, b.Y-s.PlayerPos.Y)
	})
	pt := world.TileOf(s.PlayerPos)
	free := func(t world.Tile) bool {
		return w.TileAt(t) == world.Land && !w.Blocked(t) && !bad(t) && !(math.Abs(float64(t.X-pt.X)) <= 1 && math.Abs(float64(t.Y-pt.Y)) <= 1)
	}
	for i, t := range tiles {
		if i > 3000 {
			break
		}
		lt := world.Tile{X: t.X, Y: t.Y}
		ok := true
		for y := 0; y < th && ok; y++ {
			for x := 0; x < tw && ok; x++ {
				ft := world.Tile{X: lt.X + x, Y: lt.Y + y}
				res, isOre := w.ResourceAt(ft)
				ok = isOre && res.Name == ore && free(ft)
			}
		}
		if !ok {
			continue
		}
		c := bridge.Pos{X: float64(lt.X) + float64(tw)/2, Y: float64(lt.Y) + float64(th)/2}
		if out := r.dropTile(entity, c); free(out) {
			return c, true
		}
	}
	return bridge.Pos{}, false
}

// Step — один шаг: первая невыполненная цель → её следующий примитив → исполнить.
// done — все цели выполнены.
func (r *Runner) Step(ctx context.Context) (done bool, err error) {
	s, err := r.Snapshot()
	if err != nil {
		return false, err
	}
	for _, spec := range r.Saved.Goals {
		g, err := spec.Goal()
		if err != nil {
			return false, err
		}
		prim, err := r.Saved.Planner.Next(s, g)
		if err != nil {
			r.journal("цель «%s»: %v", g, err)
			return false, err
		}
		if prim == nil {
			continue
		}
		r.H.Say("%s — %s", prim, strings.Join(prim.Why[:min(2, len(prim.Why))], " ← "))
		r.journal("шаг: %s | почему: %s", prim, prim.Reason())
		t0 := time.Now()
		if err := r.exec(ctx, s, prim); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			banned := r.Saved.Planner.Failed(prim)
			r.fails++
			r.journal("  не удалось (%v)%s", err, map[bool]string{true: " — бан шага", false: ""}[banned])
			r.H.Say("не удалось: %v", err)
			if r.fails >= r.MaxFails {
				return false, fmt.Errorf("exec: %d отказов подряд, последний: %w", r.fails, err)
			}
		} else {
			r.fails = 0
			r.Saved.Planner.Succeeded(prim)
			if prim.Mark != "" {
				if r.Saved.Done == nil {
					r.Saved.Done = map[string]bool{}
				}
				r.Saved.Done[prim.Mark] = true
			}
			r.journal("  готово за %.1f с", time.Since(t0).Seconds())
		}
		return false, r.Save()
	}
	return true, r.Save()
}

// Run — шаги до выполнения всех целей; с поддерживаемыми целями — не выходит, а следит
// (пауза Idle между проверками) до отмены ctx.
func (r *Runner) Run(ctx context.Context) error {
	if r.MaxFails == 0 {
		r.MaxFails = 8
	}
	if r.Idle == 0 {
		r.Idle = 3 * time.Second
	}
	maintain := false
	for _, g := range r.Saved.Goals {
		maintain = maintain || g.Maintain
	}
	announced := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.Until != nil && r.Until() {
			return nil
		}
		done, err := r.Step(ctx)
		if err != nil {
			return err
		}
		if !done {
			announced = false
			continue
		}
		if !announced {
			r.journal("все цели выполнены")
			r.H.Say("все цели выполнены")
			announced = true
		}
		if !maintain {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.Idle):
		}
	}
}

// Satisfied — выполнены ли все цели прямо сейчас (для тестов и CLI).
func (r *Runner) Satisfied() (bool, error) {
	s, err := r.Snapshot()
	if err != nil {
		return false, err
	}
	for _, spec := range r.Saved.Goals {
		g, err := spec.Goal()
		if err != nil {
			return false, err
		}
		if !g.Satisfied(s) {
			return false, nil
		}
	}
	return true, nil
}

func (r *Runner) owned(role string) (*plan.Owned, bridge.Entity, error) {
	var o *plan.Owned
	for i := range r.Saved.Owned {
		if r.Saved.Owned[i].Role == role {
			o = &r.Saved.Owned[i]
		}
	}
	for i := range r.stores {
		if r.stores[i].Role == role {
			o = &r.stores[i]
		}
	}
	if o == nil {
		return nil, bridge.Entity{}, fmt.Errorf("exec: роль %s не в реестре", role)
	}
	st, err := r.B.GetEntity(o.Name, o.Pos)
	if err != nil {
		return nil, bridge.Entity{}, err
	}
	if st == nil {
		return nil, bridge.Entity{}, fmt.Errorf("exec: постройки %s нет", role)
	}
	return o, st.Entity, nil
}

// exec — примитив руками.
func (r *Runner) exec(ctx context.Context, s *plan.State, p *plan.Primitive) error {
	h := r.H
	switch p.Kind {
	case "gather":
		if p.Source != "" {
			return r.gatherFrom(ctx, p.Item, p.N, p.Source)
		}
		return r.gather(ctx, p.Item, p.N)
	case "craft":
		return h.CraftWait(ctx, p.Recipe, p.N)
	case "place":
		e, err := h.Place(ctx, p.Item, p.Pos, act.North)
		if err != nil {
			return err
		}
		r.Saved.Owned = append(r.Saved.Owned, plan.Owned{Unit: e.UnitNumber, Name: e.Name, Pos: e.Position, Role: p.Role})
		return nil
	case "build":
		return r.build(ctx, p)
	case "research":
		return h.StartResearch(ctx, p.Item)
	case "collect", "refuel":
		return r.round(ctx, p)
	case "idle":
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(p.N) * time.Second):
		}
		return nil
	case "recipe":
		_, e, err := r.owned(p.Role)
		if err != nil {
			return err
		}
		return h.SetRecipe(ctx, e, p.Recipe)
	case "rotate":
		o, e, err := r.owned(p.Role)
		if err != nil {
			return err
		}
		if pe := h.Protos.Entities[e.Name]; pe.TileWidth == pe.TileHeight {
			return h.Rotate(ctx, e, p.Dir)
		}
		// прямоугольная: поворот на месте сдвинет центр — поднять и поставить заново
		if err := h.Pickup(ctx, e); err != nil {
			return err
		}
		ne, err := h.Place(ctx, p.Item, p.Pos, p.Dir)
		if err != nil {
			return err
		}
		o.Unit, o.Pos = ne.UnitNumber, ne.Position
		return nil
	case "await":
		return r.await(ctx, p.Item, p.Role)
	case "insert":
		_, e, err := r.owned(p.Role)
		if err != nil {
			return err
		}
		return h.Insert(ctx, e, p.Item, p.N)
	case "take":
		_, e, err := r.owned(p.Role)
		if err != nil {
			return err
		}
		_, err = h.Take(ctx, e)
		return err
	case "explore":
		return r.explore(ctx, p.Item)
	case "wait":
		o, _, err := r.owned(p.Role)
		if err != nil {
			return err
		}
		if _, ore, ok := plan.ModuleOf(p.Role); ok {
			return r.waitOutpost(ctx, o, ore, p.Item, p.N)
		}
		// печь: ~3.2 с на штуку; бур: 0.25/с — ждём кусками не дольше 90 с, дальше
		// планировщик заберёт накопленное и решит снова
		wait := time.Duration(float64(p.N)*3.5+15) * time.Second
		if o.Name == plan.Chest {
			wait = min(time.Duration(float64(p.N)*4.5+10)*time.Second, 90*time.Second)
		}
		deadline := time.Now().Add(wait)
		idle := 0 // опросов подряд, когда печь не работает
		for time.Now().Before(deadline) {
			st, err := r.B.GetEntity(o.Name, o.Pos)
			if err != nil {
				return err
			}
			if st == nil {
				return fmt.Errorf("exec: %s пропала", p.Role)
			}
			if st.Name == plan.Chest {
				// сундук бура: накопилось — готово; бур без топлива — планировщик дозаправит
				if st.Inventories["chest"][p.Item] >= p.N {
					return nil
				}
				if d := strings.TrimSuffix(p.Role, "-out"); d != p.Role {
					if do, _, err := r.owned(d); err == nil {
						if ds, _ := r.B.GetEntity(do.Name, do.Pos); ds == nil || (len(ds.Inventories["fuel"]) == 0 && ds.Burning == "") {
							return nil
						}
					}
				}
			} else if st.Inventories["crafter_output"][p.Item] >= p.N || len(st.Inventories["crafter_input"]) == 0 ||
				(len(st.Inventories["fuel"]) == 0 && st.Burning == "") {
				return nil // готово, нечего плавить или печь без топлива — планировщик дозаправит
			} else if st.Status != "working" {
				if idle++; idle >= 5 {
					return nil // печь стоит (выход занят другим и т. п.) — решит планировщик
				}
			} else {
				idle = 0
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		return nil // по таймауту — планировщик посмотрит, что получилось
	}
	return fmt.Errorf("exec: неизвестный шаг %s", p.Kind)
}

// explore — шаг разведки: дойти до ближайшего края открытой зоны, где ещё не были;
// персонаж открывает карту вокруг себя. Удача — открылись новые чанки (или нашёлся item).
func (r *Runner) explore(ctx context.Context, item string) error {
	if r.explored == nil {
		r.explored = map[bridge.ChunkPos]bool{}
	}
	w := r.H.World
	if _, err := w.Sync(r.B, false); err != nil {
		return err
	}
	before := len(w.Chunks)
	p, err := r.B.Player()
	if err != nil {
		return err
	}
	for _, cp := range w.Frontier(p.Position) {
		if r.explored[cp] {
			continue
		}
		r.explored[cp] = true
		c := bridge.Pos{X: float64(cp.X*world.ChunkSize + world.ChunkSize/2), Y: float64(cp.Y*world.ChunkSize + world.ChunkSize/2)}
		target, ok := w.FreeSpot(c, 1, 1, 12, nil)
		if !ok {
			continue
		}
		r.H.Say("разведка: ищу %s, иду к краю открытой карты (%.0f, %.0f)", item, target.X, target.Y)
		if err := r.H.WalkTo(ctx, target, 3); err != nil && !errors.Is(err, act.ErrNotReached) {
			return err
		}
		time.Sleep(time.Second) // карта вокруг открывается не мгновенно
		if _, err := w.Sync(r.B, false); err != nil {
			return err
		}
		if len(w.Chunks) > before {
			r.journal("  разведка: открыто чанков %d → %d", before, len(w.Chunks))
			return nil
		}
	}
	return fmt.Errorf("exec: разведка не открыла ничего нового (ищу %s)", item)
}

// gather — добыть, пока предмета не прибавится n: руду — единицами за раз, деревья и
// камни — по одному (каждый даёт несколько).
func (r *Runner) gather(ctx context.Context, item string, n int) error {
	p0, err := r.B.Player()
	if err != nil {
		return err
	}
	start := p0.Inventory[item]
	for i := 0; i < 60; i++ {
		p, err := r.B.Player()
		if err != nil {
			return err
		}
		got := p.Inventory[item] - start
		if got >= n {
			return nil
		}
		cands, err := r.B.FindMinable(item, 300, 1)
		if err != nil {
			return err
		}
		if len(cands) == 0 {
			return fmt.Errorf("exec: %s негде добыть", item)
		}
		k := 1
		if cands[0].Type == "resource" {
			k = n - got
		}
		if _, err := r.H.Gather(ctx, item, k, 300); err != nil {
			return err
		}
		if item == "wood" {
			r.rocksNearby(ctx)
		}
	}
	return fmt.Errorf("exec: %s не набралось за 60 заходов", item)
}

// rocksNearby — попутно, пока рубим деревья: разбить камни рядом (20+ камня с каждого,
// с огромного — ещё и уголь), чтобы потом меньше добывать камень отдельно.
func (r *Runner) rocksNearby(ctx context.Context) {
	p, err := r.B.Player()
	if err != nil || p.Inventory["stone"] >= 100 {
		return
	}
	if rocks, err := r.B.FindMinable("rock", 10, 1); err != nil || len(rocks) == 0 {
		return
	}
	r.H.Say("попутно разбиваю камень рядом")
	if _, err := r.H.Gather(ctx, "rock", 1, 10); err != nil && ctx.Err() == nil {
		r.journal("  попутный камень не удался: %v", err)
	}
}

// GoalsString — цели для вывода.
func GoalsString(gs []GoalSpec) string {
	var out []string
	for _, g := range gs {
		goal, err := g.Goal()
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		s := goal.String()
		if g.Maintain {
			s += " (поддерживать)"
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

// build — расчистить места и поставить группу; поставленное — в реестр (даже при сбое
// посередине).
func (r *Runner) build(ctx context.Context, p *plan.Primitive) error {
	h := r.H
	entity := h.Protos.Items[p.Item].PlaceResult
	// уже стоит своя такая же (поставил прерванный запуск) — взять в реестр, не ставить
	var spots []bridge.Pos
	var roles []string
	for i, pos := range p.Spots {
		if e, err := r.B.EntityAt(entity, pos); err == nil && e != nil {
			if st, _ := r.B.GetEntity(e.Name, e.Position); st != nil && st.Own {
				r.Saved.Owned = append(r.Saved.Owned, plan.Owned{Unit: e.UnitNumber, Name: e.Name, Pos: e.Position, Role: p.Roles[i]})
				r.journal("  %s в (%.1f, %.1f) уже стоит — беру как %s", entity, pos.X, pos.Y, p.Roles[i])
				continue
			}
		}
		// своя такая же стоит со сдвигом (повёрнута не так) — поднять, иначе не встанет
		if near, err := r.B.FindNearest(bridge.FindArgs{Name: entity, Radius: 2, Position: &pos, Limit: 3}); err == nil {
			for _, e := range near {
				if st, _ := r.B.GetEntity(e.Name, e.Position); st != nil && st.Own && !r.isOwned(e.Position) {
					h.Say("%s стоит криво на месте стройки — поднимаю", e.Name)
					if err := h.Pickup(ctx, e); err != nil {
						return err
					}
				}
			}
		}
		spots, roles = append(spots, pos), append(roles, p.Roles[i])
	}
	if len(spots) == 0 {
		return nil
	}
	p = &plan.Primitive{Kind: p.Kind, Item: p.Item, Dir: p.Dir, Spots: spots, Roles: roles, Area: p.Area}
	var tiles []world.Tile
	for _, pos := range p.Spots {
		tiles = append(tiles, layout.Tiles(h.Protos, layout.Placement{Name: entity, Pos: pos, Dir: p.Dir})...)
	}
	if _, err := h.World.Sync(r.B, false); err != nil {
		return err
	}
	for _, o := range h.World.Obstacles(tiles) {
		e, err := r.B.EntityAt(o.Name, o.Pos)
		if err != nil {
			return err
		}
		if e == nil {
			continue
		}
		h.Say("расчищаю место: %s", o.Name)
		if err := h.WalkToMine(ctx, *e); err != nil && !errors.Is(err, act.ErrNotReached) {
			return err
		}
		if err := h.Mine(ctx, *e, 15*time.Second); err != nil {
			return err
		}
	}
	// стоим внутри площади раскладки — выйти: иначе можно замуровать себя постройками
	if p.Area != nil {
		if err := h.LeaveArea(ctx, *p.Area); err != nil {
			return err
		}
	}
	es, err := h.PlaceAll(ctx, p.Item, p.Spots, p.Dir)
	for i, e := range es {
		if e.Name != "" {
			r.Saved.Owned = append(r.Saved.Owned, plan.Owned{Unit: e.UnitNumber, Name: e.Name, Pos: e.Position, Role: p.Roles[i]})
		}
	}
	return err
}

// design — раскладка под задачу kind (см. plan.State.Design), спроектированная один раз.
func (r *Runner) design(s *plan.State, kind string) (plan.Built, bool) {
	if l, ok := r.Saved.Designs[kind]; ok {
		return plan.Built{Tag: kind, List: l}, true
	}
	var l *layout.Layout
	var err error
	switch kind {
	case "research":
		// уже стоят свои лаборатории в электросети — их и взять
		if labs := r.poweredLabs(); len(labs) > 0 {
			l = layout.New(r.H.Protos, r.H.World)
			for i, e := range labs {
				l.List = append(l.List, layout.Placement{Name: e.Name, Pos: e.Position, Tag: kind})
				r.Saved.Owned = append(r.Saved.Owned, plan.Owned{Unit: e.UnitNumber, Name: e.Name, Pos: e.Position, Role: plan.BuildRole(kind, i)})
				s.Owned = append(s.Owned, &plan.Owned{Unit: e.UnitNumber, Name: e.Name, Pos: e.Position, Role: plan.BuildRole(kind, i), Inv: map[string]map[string]int{}})
			}
			break
		}
		l, err = ResearchSite(r.H.Protos, r.H.World, world.TileOf(s.PlayerPos), 2)
	case "science":
		l = layout.New(r.H.Protos, r.H.World)
		l.Usable = func(e string) bool { return r.H.Protos.EntityAvailable(e, s.Av) }
		poles, _ := r.B.FindNearest(bridge.FindArgs{Type: "electric-pole", Radius: 200, Limit: 50})
		anchor := s.PlayerPos
		for i, e := range poles {
			l.Anchors = append(l.Anchors, layout.Placement{Name: e.Name, Pos: e.Position})
			if i == 0 {
				anchor = e.Position
			}
		}
		// ~0.4 науки/с: 4 пары сборщиков (0.1/с каждая), железо 0.8/с — 3 печи, медь
		// 0.4/с — 2 печи (каменная 0.31/с), 6 лабораторий: всё по красной (≈9600 лаб·с) —
		// меньше получаса
		err = l.ScienceFactory(anchor, 4, 3, 2, 6)
	default:
		ore, ok := plan.ModuleOre(kind)
		if !ok {
			err = fmt.Errorf("неизвестная задача")
			break
		}
		l, err = r.designModule(s, kind, ore)
	}
	if err != nil {
		r.journal("раскладка %s: %v", kind, err)
		return plan.Built{}, false
	}
	if r.Saved.Designs == nil {
		r.Saved.Designs = map[string][]layout.Placement{}
	}
	r.Saved.Designs[kind] = l.List
	r.journal("раскладка %s: %s", kind, l.Summary())
	return plan.Built{Tag: kind, List: l.List}, true
}

// poweredLabs — свои лаборатории рядом в электросети, которых нет в реестре.
func (r *Runner) poweredLabs() []bridge.Entity {
	es, err := r.B.FindNearest(bridge.FindArgs{Type: "lab", Radius: 150, Limit: 10})
	if err != nil {
		return nil
	}
	mine := map[bridge.Pos]bool{}
	for _, o := range r.Saved.Owned {
		mine[o.Pos] = true
	}
	var res []bridge.Entity
	for _, e := range es {
		if st, _ := r.B.GetEntity(e.Name, e.Position); st != nil && st.Own && st.Network != 0 && !mine[e.Position] {
			res = append(res, e)
		}
	}
	return res
}

// ResearchSite — паровая станция у ближайшей воды, labs лабораторий рядом с двигателем,
// столбы.
func ResearchSite(p *knowledge.Prototypes, w *world.World, near world.Tile, labs int) (*layout.Layout, error) {
	l := layout.New(p, w)
	if _, err := l.SteamPower(near, 1, "research"); err != nil {
		return nil, err
	}
	var engine layout.Placement
	for _, pl := range l.List {
		if pl.Name == "steam-engine" {
			engine = pl
		}
	}
	for i := 0; i < labs; i++ {
		lt, ok := l.FindArea(world.TileOf(engine.Pos), 3, 3, 1, 20, layout.OffOre)
		if !ok {
			return nil, fmt.Errorf("нет места под лабораторию")
		}
		l.Add(layout.Placement{Name: "lab", Pos: layout.CenterAt(p, "lab", layout.N, lt), Tag: "research"})
	}
	l.Poles("small-electric-pole", "research")
	if errs := l.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("раскладка с ошибками: %v", errs)
	}
	return l, nil
}

// await — ждать исследования tech, пока в лаборатории lab есть наука (до 90 с; дальше
// планировщик решит снова).
func (r *Runner) await(ctx context.Context, tech, lab string) error {
	var o *plan.Owned
	if lab != "" {
		var err error
		if o, _, err = r.owned(lab); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		f, err := r.B.Force()
		if err != nil {
			return err
		}
		if f.Current != tech {
			return nil
		}
		if o == nil { // наука идёт с ленты — просто ждать
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}
		st, err := r.B.GetEntity(o.Name, o.Pos)
		if err != nil {
			return err
		}
		if st == nil || len(st.Inventories["lab_input"]) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// adopt — взять в работу свои постройки на карте, которых нет в реестре (поставлены
// раньше, другим запуском или руками): печь — как плавильню, бур на руде с сундуком на
// выходе — как площадку добычи. Иначе бот строит заново то, что уже стоит.
func (r *Runner) adopt() error {
	mine := map[bridge.Pos]bool{}
	roles := map[string]bool{}
	for _, o := range r.Saved.Owned {
		mine[o.Pos] = true
		roles[o.Role] = true
	}
	own := func(e bridge.Entity) (bool, error) {
		if mine[e.Position] {
			return false, nil
		}
		st, err := r.B.GetEntity(e.Name, e.Position)
		return err == nil && st != nil && st.Own, err
	}
	take := func(e bridge.Entity, role string) {
		r.Saved.Owned = append(r.Saved.Owned, plan.Owned{Unit: e.UnitNumber, Name: e.Name, Pos: e.Position, Role: role})
		mine[e.Position], roles[role] = true, true
		r.journal("беру в работу %s в (%.1f, %.1f) как %s", e.Name, e.Position.X, e.Position.Y, role)
	}
	if !roles["smelter"] {
		es, err := r.B.FindNearest(bridge.FindArgs{Name: plan.Furnace, Radius: 100, Limit: 20})
		if err != nil {
			return err
		}
		for _, e := range es {
			if ok, err := own(e); err != nil {
				return err
			} else if ok {
				take(e, "smelter")
				break
			}
		}
	}
	drills, err := r.B.FindNearest(bridge.FindArgs{Name: plan.Drill, Radius: 100, Limit: 30})
	if err != nil {
		return err
	}
	for _, d := range drills {
		// руда под буром (тайл под центром мог выработаться — смотрим все)
		var res world.Resource
		ok := false
		for _, t := range layout.Tiles(r.H.Protos, layout.Placement{Name: d.Name, Pos: d.Position}) {
			if res, ok = r.H.World.ResourceAt(t); ok {
				break
			}
		}
		if !ok || roles[plan.SiteRole(res.Name, 1)] {
			continue
		}
		if ok, err := own(d); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		out := r.dropTile(d.Name, d.Position).Center()
		chest, err := r.B.EntityAt(plan.Chest, out)
		if err != nil {
			return err
		}
		if chest == nil || mine[chest.Position] {
			continue
		}
		take(d, plan.SiteRole(res.Name, 1))
		take(*chest, plan.OutRole(res.Name, 1))
	}
	return nil
}

// designModule — добывающая точка старта на ближайшем месторождении ore: рядом с уже
// спроектированными точками той же руды, не на месте других спроектированных.
func (r *Runner) designModule(s *plan.State, tag, ore string) (*layout.Layout, error) {
	w := r.H.World
	l := layout.New(r.H.Protos, w)
	near := world.TileOf(s.PlayerPos)
	var same []bridge.Pos
	for t, pls := range r.Saved.Designs {
		for _, pl := range pls {
			for _, tl := range layout.Tiles(r.H.Protos, pl) {
				l.Reserved[tl] = true
			}
			if o, ok := plan.ModuleOre(t); ok && o == ore {
				same = append(same, pl.Pos)
			}
		}
	}
	if len(same) > 0 {
		near = world.TileOf(same[len(same)-1])
	}
	var patch []world.Tile
	best := math.Inf(1)
	for _, p := range w.Patches() {
		if p.Name != ore {
			continue
		}
		for _, t := range p.Tiles {
			if d := math.Hypot(float64(t.X-near.X), float64(t.Y-near.Y)); d < best {
				best, patch = d, p.Tiles
			}
		}
	}
	if patch == nil {
		return nil, fmt.Errorf("месторождения %s не видно", ore)
	}
	var err error
	switch ore {
	case "coal":
		err = l.CoalPair(patch, ore, plan.Drill, near, tag)
	case "stone":
		err = l.StoneChest(patch, ore, plan.Drill, plan.Chest, 4, near, tag)
	default:
		err = l.DrillFurnace(patch, ore, plan.Drill, plan.Furnace, near, tag)
	}
	return l, err
}

// gatherFrom — добыть item с объектов группы source ("tree", "rock") до +n в инвентаре;
// рядом нет — разведать.
func (r *Runner) gatherFrom(ctx context.Context, item string, n int, source string) error {
	p0, err := r.B.Player()
	if err != nil {
		return err
	}
	start := p0.Inventory[item]
	for i := 0; i < 400; i++ {
		p, err := r.B.Player()
		if err != nil {
			return err
		}
		if p.Inventory[item]-start >= n {
			return nil
		}
		_, err = r.H.Gather(ctx, source, 1, 150)
		if errors.Is(err, act.ErrNoTargets) {
			if err := r.explore(ctx, item); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("exec: %s с %s не набралось", item, source)
}

// waitOutpost — ждать, пока на точке накопится n предмета (уголь — в топливе бура пары,
// камень — в сундуке, пластины — в печи); не дольше минуты — дальше решит планировщик.
func (r *Runner) waitOutpost(ctx context.Context, o *plan.Owned, ore, item string, n int) error {
	deadline := time.Now().Add(min(time.Duration(float64(n)*4.5+10)*time.Second, time.Minute))
	for time.Now().Before(deadline) {
		st, err := r.B.GetEntity(o.Name, o.Pos)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("exec: %s пропала", o.Role)
		}
		have := st.Inventories["crafter_output"][item]
		switch {
		case ore == "coal":
			have = st.Inventories["fuel"][item]
		case o.Name == plan.Chest:
			have = st.Inventories["chest"][item]
		}
		if have >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil
}

// round — обход построек p.Roles ближайшим соседом: collect — забрать (Ctrl+клик),
// refuel — положить p.Ns[i] предмета. Недоступную пропустить, остальные — дальше.
func (r *Runner) round(ctx context.Context, p *plan.Primitive) error {
	h := r.H
	type stop struct {
		e bridge.Entity
		n int
	}
	var left []stop
	for i, role := range p.Roles {
		_, e, err := r.owned(role)
		if err != nil {
			continue
		}
		n := 0
		if i < len(p.Ns) {
			n = p.Ns[i]
		}
		left = append(left, stop{e, n})
	}
	done := 0
	var last error
	for len(left) > 0 {
		pl, err := r.B.Player()
		if err != nil {
			return err
		}
		bi := 0
		for i := range left {
			if dist(pl.Position, left[i].e.Position) < dist(pl.Position, left[bi].e.Position) {
				bi = i
			}
		}
		st := left[bi]
		left = append(left[:bi], left[bi+1:]...)
		if p.Kind == "collect" {
			_, err = h.Take(ctx, st.e)
		} else {
			err = h.Insert(ctx, st.e, p.Item, st.n)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			last = err
			continue
		}
		done++
	}
	if done == 0 && last != nil {
		return last
	}
	return nil
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// isOwned — постройка в этой точке есть в реестре.
func (r *Runner) isOwned(pos bridge.Pos) bool {
	for _, o := range r.Saved.Owned {
		if o.Pos == pos {
			return true
		}
	}
	return false
}
