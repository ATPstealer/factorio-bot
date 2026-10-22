package stage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/world"
)

// Стадия 17 (stages.md): обслуживание — всегда перед следующим планированием. В самом начале —
// очередь исследований на красной науке, нужных для ракете (по ходу стадии — доставлять);
// деревянные сундуки — на железные (быстрая замена, содержимое остаётся); лишнее из
// инвентаря — в хранение.

const ironChest = "iron-chest"

// s17Keep — что не класть в хранение: понадобится на следующих стадиях (стройка кластеров).
var s17Keep = []string{"transport-belt", "underground-belt", "splitter", "inserter", "small-electric-pole",
	"medium-electric-pole", "assembling-machine-1", "assembling-machine-2", ironChest, "electric-mining-drill",
	// сырьё крафта стройки
	"iron-plate", "copper-plate", "iron-gear-wheel", "copper-cable", "electronic-circuit"}

func init() {
	register(Stage{N: 17, Name: "обслуживание", Run: runMaintenance, Check: checkMaintenance})
}

// s17Techs — нужные для ракеты исследования на одной красной науке, ещё не изученные, по
// порядку пути (предпосылки раньше).
func s17Techs(h *act.Hands) ([]string, error) {
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
		if tech.Trigger == nil && len(tech.UnitIngredients) == 1 && tech.UnitIngredients[0].Name == s7Pack {
			res = append(res, t)
		}
	}
	return res, nil
}

// s17Queue — доставить в очередь исследований, сколько влезет (очередь игры — 7 с текущим).
// Не встало (предпосылки не изучены и не в очереди) — следующее по порядку.
func s17Queue(ctx context.Context, h *act.Hands) error {
	techs, err := s17Techs(h)
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
	if err := s17Queue(ctx, h); err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites, useStore: true}
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
		for i, o := range wooden {
			if _, err := h.Place(ctx, ironChest, o.Pos, 0); err != nil {
				return fmt.Errorf("сундук в (%.1f, %.1f): %w", o.Pos.X, o.Pos.Y, err)
			}
			if i%10 == 9 { // по ходу — доставить очередь исследований
				if err := s17Queue(ctx, h); err != nil {
					return err
				}
			}
		}
		if err := s17RenameChests(); err != nil {
			return err
		}
	}
	// лишнее — в хранение (все сундуки хранения теперь железные)
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	keep := append([]string(nil), s17Keep...)
	h.Say("обслуживание: лишнее из инвентаря — в хранение")
	if err := s.store(ctx, p.InventorySize, keep...); err != nil {
		return err
	}
	return s17Queue(ctx, h)
}

// s17RenameChests — в файлах раскладок (план, стадии) деревянные сундуки — железные: стоят
// теперь они (проверки и хранение ищут сундук по имени и месту).
func s17RenameChests() error {
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
	techs, err := s17Techs(h)
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
