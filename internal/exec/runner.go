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
	"factorio-bot/internal/plan"
	"factorio-bot/internal/world"
)

// GoalSpec — цель в сохраняемом виде.
type GoalSpec struct {
	Kind     string `json:"kind"` // have | smelt
	Item     string `json:"item,omitempty"`
	N        int    `json:"n"`
	Fuel     string `json:"fuel,omitempty"`
	Maintain bool   `json:"maintain,omitempty"` // после выполнения — следить и чинить
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
	}
	return nil, fmt.Errorf("exec: неизвестная цель %q", g.Kind)
}

// Saved — состояние бота между запусками.
type Saved struct {
	Goals   []GoalSpec    `json:"goals"`
	Owned   []plan.Owned  `json:"owned"`
	Planner *plan.Planner `json:"planner"`
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

	logf     *os.File
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
		Inventory: inv, PlayerPos: p.Position, FindSpot: r.findSpot, Minable: r.minable}
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
		o.Inv, o.Status = st.Inventories, st.Status
		if o.Inv == nil {
			o.Inv = map[string]map[string]int{}
		}
		kept = append(kept, o)
	}
	r.Saved.Owned = kept
	for i := range r.Saved.Owned {
		s.Owned = append(s.Owned, &r.Saved.Owned[i])
	}
	r.minables = map[string]bool{}
	if err := r.findStores(s); err != nil {
		return nil, err
	}
	return s, nil
}

// findStores — сундуки рядом вне реестра, в которых что-то лежит (игрок видит их и может
// открыть): поставленные руками, обломки корабля.
func (r *Runner) findStores(s *plan.State) error {
	es, err := r.B.FindNearest(bridge.FindArgs{Type: "container", Radius: 64, Limit: 30})
	if err != nil {
		return err
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
		if st == nil || len(st.Inventories["chest"]) == 0 {
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
		// печь: ~3.2 с на штуку; бур: 0.25/с — ждём кусками не дольше 90 с, дальше
		// планировщик заберёт накопленное и решит снова
		wait := time.Duration(float64(p.N)*3.5+15) * time.Second
		if o.Name == plan.Chest {
			wait = min(time.Duration(float64(p.N)*4.5+10)*time.Second, 90*time.Second)
		}
		deadline := time.Now().Add(wait)
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
			} else if st.Inventories["crafter_output"][p.Item] >= p.N || len(st.Inventories["crafter_input"]) == 0 {
				return nil
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
