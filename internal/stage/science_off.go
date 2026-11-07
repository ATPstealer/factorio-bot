package stage

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Наука на ракету — отключить подачу (stages.md, общее; со стадии 62): пакета на лентах хватает
// на то, что осталось исследовать до ракеты, — у кластеров этого пакета на каждом входе снять
// одну ленту — первую после делителя отвода на шине. Снятые — в state-science-off.json: чертёж
// (blueprint) их не возвращает. Проверка — в начале стадии и в ожиданиях (исследование, проверка
// по факту, крафт), не чаще раза в scienceEvery; пока не отключены все пакеты.

const (
	scienceOffFrom = 62
	scienceOffFile = "state-science-off.json"
	scienceEvery   = 10 * time.Minute
)

// sciencePacks — пакеты до ракеты.
var sciencePacks = []string{redPack, greenPack, bluePack, purplePack, yellowPack}

// scienceOff — отключённые пакеты и снятые постройки.
type scienceOff struct {
	Packs   []string           `json:"packs"`
	Removed []layout.Placement `json:"removed"`
}

// scienceAt — когда проверяли в последний раз (процесс бота — одна стадия: в начале — всегда).
var scienceAt time.Time

func loadScienceOff() *scienceOff {
	var s scienceOff
	if data, err := os.ReadFile(runPath(scienceOffFile)); err == nil {
		json.Unmarshal(data, &s)
	}
	return &s
}

// has — снята ли pl отключением науки (по имени и месту).
func (s *scienceOff) has(pl layout.Placement) bool {
	return slices.ContainsFunc(s.Removed, func(q layout.Placement) bool { return q.Name == pl.Name && q.Pos == pl.Pos })
}

// packsLeft — сколько каждого пакета ещё нужно на исследования до ракеты: у каждого неизученного
// на пути к rocket-silo — стоимость × (1 − доля сделанного).
func packsLeft(h *act.Hands, f bridge.Force) (map[string]float64, error) {
	done := map[string]bool{}
	for _, t := range f.Researched {
		done[t] = true
	}
	path, err := h.Protos.TechPath([]string{"rocket-silo"}, done)
	if err != nil {
		return nil, err
	}
	left := map[string]float64{}
	for _, t := range path {
		tech := h.Protos.Technologies[t]
		part := f.Saved[t]
		if f.Current == t {
			part = f.Progress
		}
		for _, in := range tech.UnitIngredients {
			left[in.Name] += tech.UnitCount * in.Amount * (1 - part)
		}
	}
	return left, nil
}

// scienceTaps — у кластеров пакета pack первые постройки после делителей отвода с шины (по одной
// на отвод): лента или подземка с тегом подвода кластера на выходе делителя.
func scienceTaps(h *act.Hands, plan *ClusterPlan, pack string) []layout.Placement {
	var res []layout.Placement
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Make || c.Product != pack {
			continue
		}
		tag := cluster.LogisticsTag + "/" + c.Name
		at := map[world.Tile]layout.Placement{}
		var splitters []layout.Placement
		for _, pl := range plan.Layout {
			switch {
			case pl.Tag != tag:
			case pl.Name == "splitter" || pl.Name == "fast-splitter":
				splitters = append(splitters, pl)
			default:
				for _, t := range layout.Tiles(h.Protos, pl) {
					at[t] = pl
				}
			}
		}
		for _, sp := range splitters {
			for _, t := range layout.Tiles(h.Protos, sp) {
				if q, ok := at[layout.Step(t, sp.Dir)]; ok {
					res = append(res, q)
					break
				}
			}
		}
	}
	return res
}

// lightCrackFrom — с этой стадии крекинг лёгкой нефти — без рецепта (stages.md, общее).
const lightCrackFrom = 62

// stopLightCracking — химзаводы крекинга лёгкой нефти — без рецепта (stages.md, общее; решение
// пользователя): ракете лёгкая нужна вся (ракетное топливо), газа ей хватает от НПЗ‑2; лёгкая
// уходила в газ, газ полон — НПЗ стояли, ракетному топливу не было лёгкой (1098794301,
// 3462254828). Поднять и поставить на место — поставленный стоит без рецепта.
func stopLightCracking(ctx context.Context, h *act.Hands, n int) error {
	if n < lightCrackFrom || paper != nil || h.B == nil {
		return nil
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	for _, pl := range plan.Layout {
		if pl.Recipe != "light-oil-cracking" {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil {
			return err
		}
		if st == nil || st.Recipe != "light-oil-cracking" {
			continue
		}
		h.Say("крекинг лёгкой нефти в (%.1f, %.1f) — без рецепта: поднимаю и ставлю на место", pl.Pos.X, pl.Pos.Y)
		if err := h.Pickup(ctx, st.Entity); err != nil {
			return err
		}
		if _, err := h.Place(ctx, h.Protos.ItemFor(pl.Name), pl.Pos, pl.Dir); err != nil {
			return err
		}
	}
	return nil
}

// scienceCut — отключить подачу пакетов, которых на лентах уже хватает до ракеты (раз в
// scienceEvery, со стадии scienceOffFrom; на бумаге — нет).
func scienceCut(ctx context.Context, h *act.Hands) error {
	if curStage < scienceOffFrom || paper != nil || h.B == nil || time.Since(scienceAt) < scienceEvery {
		return nil
	}
	scienceAt = time.Now()
	off := loadScienceOff()
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	left, err := packsLeft(h, f)
	if err != nil {
		return err
	}
	belts, err := h.B.BeltItems()
	if err != nil {
		return err
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	// отключённого стало меньше нужного (на лентах и в лабораториях) — подачу вернуть: снятое — на
	// место (3462254828: синюю сняли при 718 на лентах, заполнители «лаборатории не стоят» её
	// съели — rocket-silo встал на 50%)
	inLabs := map[string]int{}
	var labs []layout.Placement
	for _, pl := range plan.Layout {
		if pl.Name == "lab" {
			labs = append(labs, pl)
		}
	}
	if states, err := entityStates(h, labs); err == nil {
		for _, st := range states {
			if st != nil {
				for _, inv := range st.Inventories {
					for it, n := range inv {
						inLabs[it] += n
					}
				}
			}
		}
	}
	for _, pack := range slices.Clone(off.Packs) {
		if float64(belts[pack]+inLabs[pack]) >= left[pack] {
			continue
		}
		taps := scienceTaps(h, plan, pack)
		h.Say("наука на ракету: %s на лентах и в лабораториях %d, нужно ещё %.0f — возвращаю подачу (%d лент)",
			pack, belts[pack]+inLabs[pack], left[pack], len(taps))
		if err := buildSorted(ctx, h, taps, nil); err != nil {
			return err
		}
		off.Packs = slices.DeleteFunc(off.Packs, func(p string) bool { return p == pack })
		off.Removed = slices.DeleteFunc(off.Removed, func(q layout.Placement) bool {
			return slices.ContainsFunc(taps, func(t layout.Placement) bool { return t.Name == q.Name && t.Pos == q.Pos })
		})
		if err := writeState(scienceOffFile, off); err != nil {
			return err
		}
	}
	for _, pack := range sciencePacks {
		if slices.Contains(off.Packs, pack) || float64(belts[pack]) < left[pack] {
			continue
		}
		taps := scienceTaps(h, plan, pack)
		h.Say("наука на ракету: %s на лентах %d, нужно ещё %.0f — снимаю подачу (%d лент)", pack, belts[pack], left[pack], len(taps))
		var es []bridge.Entity
		for _, pl := range taps {
			if e, err := h.B.EntityAt(pl.Name, pl.Pos); err == nil && e != nil {
				es = append(es, *e)
			}
		}
		if err := h.PickupAll(ctx, es); err != nil {
			return err
		}
		off.Packs = append(off.Packs, pack)
		off.Removed = append(off.Removed, taps...)
		if err := writeState(scienceOffFile, off); err != nil {
			return err
		}
	}
	return nil
}
