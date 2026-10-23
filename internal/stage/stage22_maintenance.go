package stage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 22 (stages.md): обслуживание — всегда перед следующим планированием. В самом начале —
// очередь исследований на красной науке, нужных для ракете (по ходу стадии — доставлять);
// деревянные сундуки — на железные (быстрая замена, содержимое остаётся); лишнее из
// инвентаря — в хранение.

const ironChest = "iron-chest"

// keepItems — что не класть в хранение: понадобится на следующих стадиях (стройка кластеров).
var keepItems = []string{"transport-belt", "underground-belt", "splitter", "inserter", "small-electric-pole",
	"medium-electric-pole", "assembling-machine-1", "assembling-machine-2", ironChest, "electric-mining-drill",
	// сырьё крафта стройки
	"iron-plate", "copper-plate", "iron-gear-wheel", "copper-cable", "electronic-circuit"}

func init() {
	register(Stage{N: 22, Name: "обслуживание", Run: runMaintenance, Check: checkMaintenance})
}

// rocketRedTechs — нужные для ракеты исследования на одной красной науке, ещё не изученные, по
// порядку пути (предпосылки раньше).
func rocketRedTechs(h *act.Hands) ([]string, error) {
	f, err := h.B.Force()
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, t := range f.Researched {
		done[t] = true
	}
	path, err := h.Protos.TechPath([]string{"rocket-silo"}, done)
	if err != nil {
		return nil, err
	}
	var res []string
	for _, t := range path {
		tech := h.Protos.Technologies[t]
		if tech.Trigger == nil && len(tech.UnitIngredients) == 1 && tech.UnitIngredients[0].Name == redPack {
			res = append(res, t)
		}
	}
	return res, nil
}

// queueResearch — доставить в очередь исследований, сколько влезет (очередь игры — 7 с текущим).
// Не встало (предпосылки не изучены и не в очереди) — следующее по порядку.
func queueResearch(ctx context.Context, h *act.Hands) error {
	techs, err := rocketRedTechs(h)
	if err != nil {
		return err
	}
	for _, t := range techs {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		busy := len(f.Queue)
		if f.Current != "" && !slices.Contains(f.Queue, f.Current) {
			busy++
		}
		if busy >= 7 {
			return nil
		}
		if f.Current == t || slices.Contains(f.Queue, t) {
			continue
		}
		if err := h.StartResearch(ctx, t); err != nil {
			if ctx.Err() != nil {
				return err
			}
			h.Log("обслуживание: %s в очередь не встало: %v", t, err)
		}
	}
	return nil
}

func runMaintenance(ctx context.Context, h *act.Hands) error {
	// в самом начале — очередь исследований: лаборатории учат, пока идёт остальное
	if err := queueResearch(ctx, h); err != nil {
		return err
	}
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites, useStore: true}
	// деревянные сундуки — на железные
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	var wooden []world.Object
	for _, o := range h.World.Objects() {
		if o.Own && o.Name == "wooden-chest" {
			wooden = append(wooden, o)
		}
	}
	if len(wooden) > 0 {
		h.Say("обслуживание: %d деревянных сундуков — на железные", len(wooden))
		if err := s.have(ctx, ironChest, len(wooden)); err != nil {
			return err
		}
		for i := 0; len(wooden) > 0; i++ {
			// следующий — ближайший к персонажу
			p, err := h.B.Player()
			if err != nil {
				return err
			}
			k := 0
			for j, w := range wooden {
				if dist2(w.Pos, p.Position) < dist2(wooden[k].Pos, p.Position) {
					k = j
				}
			}
			o := wooden[k]
			wooden = slices.Delete(wooden, k, k+1)
			if _, err := h.Place(ctx, ironChest, o.Pos, 0); err != nil {
				return fmt.Errorf("сундук в (%.1f, %.1f): %w", o.Pos.X, o.Pos.Y, err)
			}
			if i%10 == 9 { // по ходу — доставить очередь исследований
				if err := queueResearch(ctx, h); err != nil {
					return err
				}
			}
		}
		if err := renameChests(); err != nil {
			return err
		}
	}
	// сверка с планом: недостроенное — достроить, стоящее не так — исправить
	if err := planFix(ctx, h, s); err != nil {
		return err
	}
	if err := queueResearch(ctx, h); err != nil {
		return err
	}
	// лишнее — в хранение (все сундуки хранения теперь железные)
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	keep := append([]string(nil), keepItems...)
	h.Say("обслуживание: лишнее из инвентаря — в хранение")
	if err := s.store(ctx, p.InventorySize, keep...); err != nil {
		return err
	}
	return queueResearch(ctx, h)
}

// planDiff — расхождение плана и построенного: чего нет (st == nil) или что стоит не так.
type planDiff struct {
	pl  layout.Placement
	st  *bridge.EntityState
	why string
}

func (d planDiff) String() string {
	return fmt.Sprintf("%s в (%.1f, %.1f): %s", d.pl.Name, d.pl.Pos.X, d.pl.Pos.Y, d.why)
}

// directed — у постройки важно направление (ленты, подземки, делители, инсертеры).
func directed(name string) bool {
	return name == "transport-belt" || name == "underground-belt" || name == "splitter" || name == "inserter" || name == "burner-inserter"
}

// planAudit — сверка всего плана с построенным: стоит ли, направление, рецепт сборщика, фильтр
// инсертера.
func planAudit(h *act.Hands) ([]planDiff, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	// дописанное проектом зелёной науки (стадия 6) строят стадии после обслуживания — не сверять
	later := map[layout.Placement]bool{}
	if added, _, err := greenAdded(); err == nil {
		for _, pl := range added {
			later[pl] = true
		}
	}
	var res []planDiff
	for _, pl := range plan.Layout {
		if later[pl] {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil {
			return nil, err
		}
		switch {
		case st == nil:
			res = append(res, planDiff{pl, nil, "нет"})
		case directed(pl.Name) && st.Direction != nil && *st.Direction != pl.Dir:
			res = append(res, planDiff{pl, st, fmt.Sprintf("направление %d, в плане %d", *st.Direction, pl.Dir)})
		case strings.HasPrefix(pl.Name, "assembling-machine") && pl.Recipe != "" && st.Recipe != pl.Recipe:
			res = append(res, planDiff{pl, st, fmt.Sprintf("рецепт %q, в плане %s", st.Recipe, pl.Recipe)})
		case strings.HasSuffix(pl.Name, "inserter") && pl.Recipe != "" && !slices.Contains(st.Filters, pl.Recipe):
			res = append(res, planDiff{pl, st, "нет фильтра " + pl.Recipe})
		}
	}
	return res, nil
}

// planFix — достроить недостающее и исправить стоящее не так.
func planFix(ctx context.Context, h *act.Hands, s *worker) error {
	diffs, err := planAudit(h)
	if err != nil {
		return err
	}
	if len(diffs) == 0 {
		return nil
	}
	h.Say("сверка с планом: %d расхождений", len(diffs))
	var missing []layout.Placement
	for _, d := range diffs {
		h.Log("сверка: %v", d)
		if d.st == nil {
			missing = append(missing, d.pl)
		}
	}
	if len(missing) > 0 {
		order := []string{"electric-mining-drill", furnace, "assembling-machine-1", "lab", "transport-belt", "underground-belt",
			"splitter", chest, "inserter", "burner-inserter", "small-electric-pole"}
		if err := buildSorted(ctx, h, missing, order); err != nil {
			return err
		}
	}
	for _, d := range diffs {
		if d.st == nil {
			continue
		}
		var err error
		switch {
		case d.pl.Name == "underground-belt", d.pl.Name == "splitter" && d.st.Direction != nil && *d.st.Direction%8 != d.pl.Dir%8:
			// подземку R не поворачивает (меняет вход/выход), делитель поперёк — сдвинет центр:
			// поднять и поставить заново
			if err = h.Pickup(ctx, d.st.Entity); err == nil {
				_, err = h.PlaceDirs(ctx, d.pl.Name, []bridge.Pos{d.pl.Pos}, []int{d.pl.Dir})
			}
		case directed(d.pl.Name):
			err = h.Rotate(ctx, d.st.Entity, d.pl.Dir)
		case strings.HasPrefix(d.pl.Name, "assembling-machine"):
			err = h.SetRecipe(ctx, d.st.Entity, d.pl.Recipe)
		default:
			err = h.SetFilter(ctx, d.st.Entity, d.pl.Recipe)
		}
		if err != nil {
			return fmt.Errorf("сверка: %v: %w", d, err)
		}
	}
	return nil
}

// renameChests — в файлах раскладок (план, стадии) деревянные сундуки — железные: стоят
// теперь они (проверки и хранение ищут сундук по имени и месту).
func renameChests() error {
	files, err := filepath.Glob(runPath("state*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if s := string(data); strings.Contains(s, `"wooden-chest"`) {
			if err := os.WriteFile(f, []byte(strings.ReplaceAll(s, `"wooden-chest"`, `"`+ironChest+`"`)), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMaintenance — нет своих деревянных сундуков; каждое нужное исследование изучено или в
// очереди.
func checkMaintenance(h *act.Hands) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	for _, o := range h.World.Objects() {
		if o.Own && o.Name == "wooden-chest" {
			return fmt.Errorf("деревянный сундук в (%.1f, %.1f)", o.Pos.X, o.Pos.Y)
		}
	}
	diffs, err := planAudit(h)
	if err != nil {
		return err
	}
	if len(diffs) > 0 {
		return fmt.Errorf("сверка с планом: %d расхождений, первое — %v", len(diffs), diffs[0])
	}
	techs, err := rocketRedTechs(h)
	if err != nil {
		return err
	}
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	var miss []string
	for _, t := range techs {
		if f.Current != t && !slices.Contains(f.Queue, t) {
			miss = append(miss, t)
		}
	}
	if len(miss) > 0 {
		return fmt.Errorf("не в очереди исследований: %s", strings.Join(miss, ", "))
	}
	return nil
}
