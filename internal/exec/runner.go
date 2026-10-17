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
	fails    int
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
	return s, nil
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
	case strings.HasPrefix(role, "smelt-"):
		ore := strings.TrimPrefix(group(role), "smelt-")
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
			return bridge.Pos{}, false
		}
	default:
		anchor = s.PlayerPos
	}
	return w.FreeSpotWhere(anchor, tw, th, 15, &s.PlayerPos, noOre)
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
	for i := range r.Saved.Owned {
		o := &r.Saved.Owned[i]
		if o.Role != role {
			continue
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
	return nil, bridge.Entity{}, fmt.Errorf("exec: роль %s не в реестре", role)
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
	case "wait":
		o, _, err := r.owned(p.Role)
		if err != nil {
			return err
		}
		deadline := time.Now().Add(time.Duration(float64(p.N)*3.5+15) * time.Second)
		for time.Now().Before(deadline) {
			st, err := r.B.GetEntity(o.Name, o.Pos)
			if err != nil {
				return err
			}
			if st == nil {
				return fmt.Errorf("exec: %s пропала", p.Role)
			}
			if st.Inventories["crafter_output"][p.Item] >= p.N || len(st.Inventories["crafter_input"]) == 0 {
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
	}
	return fmt.Errorf("exec: %s не набралось за 60 заходов", item)
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
