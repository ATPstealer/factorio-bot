package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 9 (stages.md): первый кластер добычи угля по плану стадии 3 — уголь в ленту шины;
// станция берёт уголь отводом с шины вместо сундука стадии 4 (у бойлера — burner-инсертер с
// ленты, как в плане). Питание — столбами по коридорам от сети (connectPower).

const s9Plan = "state-stage9.json" // раскладка стадии: один раз на карту

func init() {
	register(Stage{N: 9, Name: "добыча угля", Run: runCoal, Check: checkCoal})
}

// s9Layout — s9Base и столбы питания; один раз на карту (файл).
func s9Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(s9Plan)); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	res := s9Base(h, plan)
	if len(res) == 0 {
		return nil, fmt.Errorf("в плане нет добычи угля или станции")
	}
	coal, power := clusterOf(plan, cluster.Mine, "coal"), clusterOf(plan, cluster.Power, "")
	allow := []string{coal.Name, power.Name}
	if bus := busOf(plan); bus != nil {
		allow = append(allow, bus.Name)
	}
	// питание кластера — столбами по коридорам от сети
	poles, err := connectPower(h, plan, res, allow)
	if err != nil {
		return nil, err
	}
	res = append(res, poles...)
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(runPath(s9Plan), data, 0o644)
}

func runCoal(ctx context.Context, h *act.Hands) error {
	list, err := s9Layout(h)
	if err != nil {
		return err
	}
	station, err := s4Layout(h)
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites, useStore: true}
	// сундук и электроинсертер стадии 4 стоят на месте ленты и burner-инсертера — их в конце
	tmp := []layout.Placement{s4Find(station, chest), s4Find(station, "inserter")}
	var first []layout.Placement
	for _, pl := range list {
		if pl.Pos == tmp[0].Pos || pl.Pos == tmp[1].Pos {
			continue
		}
		first = append(first, pl)
	}
	if err := s.build(ctx, first); err != nil {
		return err
	}
	if err := s.store(ctx, 10); err != nil { // в сундуке — уголь: при подборе — в инвентарь
		return err
	}
	for _, pl := range tmp {
		if e, err := s.entity(pl); err != nil {
			return err
		} else if e != nil {
			h.Say("убираю %s стадии 4 — уголь пойдёт лентой", pl.Name)
			if err := h.Pickup(ctx, *e); err != nil {
				return err
			}
		}
	}
	if err := s.build(ctx, list); err != nil {
		return err
	}
	// burner-инсертерам — топливо на запуск
	for _, pl := range list {
		if pl.Name != "burner-inserter" {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет %s в (%.1f, %.1f): %v", pl.Name, pl.Pos.X, pl.Pos.Y, err)
		}
		if st.Count("coal") == 0 && st.RemainingFuel == 0 {
			if err := s.have(ctx, "coal", 1); err != nil {
				return err
			}
			if err := h.Insert(ctx, st.Entity, "coal", 1); err != nil {
				return err
			}
		}
	}
	// уголь дошёл до бойлера
	deadline := time.Now().Add(3 * time.Minute)
	for checkCoal(h) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func checkCoal(h *act.Hands) error {
	list, err := s9Layout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	station, err := s4Layout(h)
	if err != nil {
		return err
	}
	if e, err := h.B.EntityAt(chest, s4Find(station, chest).Pos); err != nil || e != nil {
		return fmt.Errorf("сундук стадии 4 у бойлера ещё стоит (%v)", err)
	}
	working := 0
	for _, pl := range list {
		if pl.Name == "electric-mining-drill" {
			if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && st.Status == "working" {
				working++
			}
		}
	}
	if working == 0 {
		return fmt.Errorf("буры угля не работают")
	}
	// лента у бойлера несёт уголь
	belt := s4Find(station, chest).Pos
	st, err := h.B.GetEntity("transport-belt", belt)
	if err != nil || st == nil {
		return fmt.Errorf("нет ленты угля у бойлера: %v", err)
	}
	if st.Belt["coal"] == 0 {
		return fmt.Errorf("уголь не дошёл лентой до бойлера")
	}
	return nil
}
