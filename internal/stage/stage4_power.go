package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 4 (stages.md): начало станции из плана стадии 3 — на её месте и по её раскладке:
// помпа, бойлер, 2 паровых двигателя, столбы. Уголь пока не лентой: на месте ленты у бойлера —
// сундук с s4Coal угля и электрический инсертер из него в бойлер (вместо burner-инсертера с
// ленты). Для запуска бойлер один раз наполнить углём руками.

const (
	s4Coal = 200
	s4Tag  = "s4"
	s4Plan = "run/state-stage4.json" // раскладка стадии: один раз на карту
)

func init() {
	register(Stage{N: 4, Name: "электричество", Run: runPower, Check: checkPower})
}

// s4Layout — что строить: станция плана (бойлеры, двигатели, столбы, трубы), помпа с трубами,
// сундук и электрический инсертер к бойлеру, недостающие столбы (инсертер запитать).
func s4Layout(h *act.Hands) ([]layout.Placement, error) {
	if data, err := os.ReadFile(s4Plan); err == nil {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	s, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	var power *cluster.Cluster
	for _, c := range s.Clusters {
		if c.Kind == cluster.Power {
			power = c
			break
		}
	}
	if power == nil {
		return nil, fmt.Errorf("в плане нет станции")
	}
	all := layout.New(h.Protos, nil)
	for _, pl := range s.Layout {
		all.Add(pl)
	}
	l := layout.New(h.Protos, h.World)
	own := cluster.Tag(power.Name)
	water := cluster.LogisticsTag + "/" + power.Name
	var feed []layout.Placement // burner-инсертеры с ленты в бойлер
	for _, pl := range s.Layout {
		switch {
		case pl.Tag == water && (pl.Name == "offshore-pump" || strings.HasPrefix(pl.Name, "pipe")):
			l.Add(pl)
		case pl.Tag != own:
		case pl.Name == "boiler", pl.Name == "steam-engine", pl.Name == "small-electric-pole", strings.HasPrefix(pl.Name, "pipe"):
			l.Add(pl)
		case pl.Name == "burner-inserter":
			t := world.TileOf(pl.Pos)
			a, b := all.At(layout.Step(t, pl.Dir)), all.At(layout.Step(t, (pl.Dir+8)%16))
			if a != nil && b != nil && (a.Name == "transport-belt" && b.Name == "boiler" || a.Name == "boiler" && b.Name == "transport-belt") {
				feed = append(feed, pl)
			}
		}
	}
	if len(feed) == 0 {
		return nil, fmt.Errorf("в раскладке станции нет подачи угля в бойлер")
	}
	f := feed[0] // один бойлер: сундук на месте ленты, инсертер — электрический
	t := world.TileOf(f.Pos)
	belt := layout.Step(t, f.Dir)
	if b := all.At(belt); b == nil || b.Name != "transport-belt" {
		belt = layout.Step(t, (f.Dir+8)%16)
	}
	l.Add(layout.Placement{Name: chest, Pos: belt.Center(), Tag: s4Tag})
	l.Add(layout.Placement{Name: "inserter", Pos: f.Pos, Dir: f.Dir, Tag: s4Tag})
	l.Poles("small-electric-pole", s4Tag)
	data, err := json.MarshalIndent(l.List, "", " ")
	if err != nil {
		return nil, err
	}
	return l.List, os.WriteFile(s4Plan, data, 0o644)
}

// s4Order — порядок стройки: вода, бойлер, двигатели, столбы, сундук, инсертер.
var s4Order = []string{"offshore-pump", "pipe-to-ground", "pipe", "boiler", "steam-engine", "small-electric-pole", chest, "inserter"}

func runPower(ctx context.Context, h *act.Hands) error {
	list, err := s4Layout(h)
	if err != nil {
		return err
	}
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites}
	for _, name := range s4Order {
		var todo []layout.Placement
		for _, pl := range list {
			if pl.Name != name {
				continue
			}
			if e, err := s.entity(pl); err != nil {
				return err
			} else if e == nil {
				todo = append(todo, pl)
			}
		}
		if len(todo) == 0 {
			continue
		}
		if err := s.have(ctx, name, len(todo)); err != nil {
			return err
		}
		for _, pl := range todo {
			if _, err := h.Place(ctx, pl.Name, pl.Pos, pl.Dir); err != nil {
				return fmt.Errorf("поставить %s в (%.1f, %.1f): %w", pl.Name, pl.Pos.X, pl.Pos.Y, err)
			}
		}
	}
	boiler, box := s4Find(list, "boiler"), s4Find(list, chest)
	// запуск: бойлер полностью углём руками, потом сундук
	if st, err := h.B.GetEntity("boiler", boiler.Pos); err != nil || st == nil {
		return fmt.Errorf("нет бойлера: %v", err)
	} else if fuel := st.Inventories["fuel"]["coal"]; fuel < 50 {
		if err := s.have(ctx, "coal", 50-fuel); err != nil {
			return err
		}
		if _, err := h.InsertAll(ctx, st.Entity, "coal"); err != nil {
			return err
		}
	}
	st, err := h.B.GetEntity(chest, box.Pos)
	if err != nil || st == nil {
		return fmt.Errorf("нет сундука: %v", err)
	}
	if n := s4Coal - st.Inventories["chest"]["coal"]; n > 0 {
		if err := s.have(ctx, "coal", n); err != nil {
			return err
		}
		h.Say("кладу %d угля в сундук бойлера", n)
		if err := h.Insert(ctx, st.Entity, "coal", n); err != nil {
			return err
		}
	}
	// двигатели заработали (инсертер тратит электричество)
	deadline := time.Now().Add(30 * time.Second)
	for checkPower(h) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func s4Find(list []layout.Placement, name string) layout.Placement {
	for _, pl := range list {
		if pl.Name == name {
			return pl
		}
	}
	return layout.Placement{}
}

func checkPower(h *act.Hands) error {
	list, err := s4Layout(h)
	if err != nil {
		return err
	}
	count := map[string]int{}
	state := map[string]*bridge.EntityState{}
	for _, pl := range list {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("не стоит %s в (%.1f, %.1f)", pl.Name, pl.Pos.X, pl.Pos.Y)
		}
		count[pl.Name]++
		state[pl.Name] = st
	}
	switch {
	case count["offshore-pump"] < 1, count["boiler"] < 1, count["steam-engine"] < 2, count["small-electric-pole"] < 1:
		return fmt.Errorf("станция неполная: %v", count)
	}
	if c := state[chest].Inventories["chest"]["coal"] + state["boiler"].Inventories["fuel"]["coal"]; c < s4Coal {
		return fmt.Errorf("угля в сундуке и бойлере %d, нужно не меньше %d", c, s4Coal)
	}
	ins := state["inserter"]
	if ins.Network == 0 || ins.Status == "no_power" {
		return fmt.Errorf("инсертер без электричества (%s)", ins.Status)
	}
	working := false
	for _, pl := range list {
		if pl.Name == "steam-engine" {
			if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && st.Status == "working" {
				working = true
			}
		}
	}
	if !working {
		return fmt.Errorf("паровые двигатели не работают (%s)", state["steam-engine"].Status)
	}
	return nil
}
