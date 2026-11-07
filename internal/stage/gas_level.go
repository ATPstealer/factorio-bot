package stage

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
)

// Газ по уровню в цистернах (stages.md, общее; со стадии gasFrom — как встали цистерны газа у
// НПЗ‑2 и лёгкой у крекинга тяжёлого): газ, которому некуда деться, останавливает НПЗ‑2 — а с
// ним лёгкую, тяжёлое и смазку (затор на 53 у 366255770: наука стоит; тупик ракеты на 68 у
// 3462254828: шахта ждёт топливо, процессоры и ЛПС не тратятся). Раз в gasEvery (в начале
// стадии и в ожиданиях) — по одному НПЗ простой переработки за проверку: газ полон, а лёгкая нет
// — выключить; газа нет, а лёгкая полна — включить; выключать нечего, а газ полон и лёгкой нет —
// слить сеть газа. Выключенные — в gasOffFile: чертёж (blueprint) их рецепт не возвращает.

const (
	gasFrom    = 51
	gasEvery   = 10 * time.Minute
	gasOffFile = "state-gas-off.json"
	// tankVolume — ёмкость цистерны (storage-tank, 2.x)
	tankVolume = 25000.0
	basicOil   = "basic-oil-processing"
	gasFluid   = "petroleum-gas"
	lightFluid = "light-oil"
)

// gasAt — когда проверяли в последний раз (процесс бота — одна стадия: в начале — всегда).
var gasAt time.Time

// gasOff — выключенные НПЗ простой переработки (по месту).
type gasOff struct {
	Off []bridge.Pos `json:"off"`
}

func loadGasOff() *gasOff {
	var s gasOff
	if data, err := os.ReadFile(runPath(gasOffFile)); err == nil {
		json.Unmarshal(data, &s)
	}
	return &s
}

// recipeOff — постройка плана стоит без рецепта по правилу (stages.md, общее): крекинг лёгкой —
// с lightCrackFrom, НПЗ простой переработки — выключенные по уровню газа. Чертёж их рецепт не
// возвращает.
func recipeOff(pl layout.Placement, off *gasOff) bool {
	switch pl.Recipe {
	case "light-oil-cracking":
		return curStage >= lightCrackFrom
	case basicOil:
		return slices.Contains(off.Off, pl.Pos)
	}
	return false
}

// gasStep — что сделать по уровням (доли 0…1) газа и лёгкой: on, off — сколько НПЗ простой
// переработки с рецептом и без.
type gasStep int

const (
	gasKeep gasStep = iota
	gasTurnOff
	gasTurnOn
	gasFlush
)

func gasDecide(gas, light float64, on, off int) gasStep {
	switch {
	case gas >= 0.9 && light < 0.9 && on > 0:
		return gasTurnOff
	case gas < 0.1 && light >= 0.9 && off > 0:
		return gasTurnOn
	case on == 0 && gas >= 0.95 && light < 0.05:
		return gasFlush
	}
	return gasKeep
}

// fluidTanks — цистерны плана жидкости fluid у кластеров (тег — …/<жидкость>; склад нефти — без
// жидкости в теге).
func fluidTanks(list []layout.Placement, fluid string) []layout.Placement {
	var res []layout.Placement
	for _, pl := range list {
		if pl.Name == "storage-tank" && strings.HasSuffix(pl.Tag, "/"+fluid) {
			res = append(res, pl)
		}
	}
	return res
}

// tankLevel — доля заполнения стоящих цистерн жидкостью fluid; стоящих нет — false.
func tankLevel(h *act.Hands, tanks []layout.Placement, fluid string) (float64, *bridge.EntityState, bool, error) {
	states, err := entityStates(h, tanks)
	if err != nil {
		return 0, nil, false, err
	}
	sum, n := 0.0, 0
	var first *bridge.EntityState
	for _, st := range states {
		if st == nil {
			continue
		}
		if first == nil {
			first = st
		}
		sum += st.Fluids[fluid]
		n++
	}
	if n == 0 {
		return 0, nil, false, nil
	}
	return sum / (float64(n) * tankVolume), first, true, nil
}

// gasLevel — газ по уровню в цистернах (раз в gasEvery, со стадии gasFrom; на бумаге — нет).
func gasLevel(ctx context.Context, h *act.Hands) error {
	if curStage < gasFrom || paper != nil || h.B == nil || time.Since(gasAt) < gasEvery {
		return nil
	}
	gasAt = time.Now()
	plan, err := LoadPlan()
	if err != nil {
		return nil
	}
	gas, gasTank, ok, err := tankLevel(h, fluidTanks(plan.Layout, gasFluid), gasFluid)
	if err != nil || !ok {
		return err
	}
	light, _, ok, err := tankLevel(h, fluidTanks(plan.Layout, lightFluid), lightFluid)
	if err != nil || !ok {
		return err
	}
	var refs []layout.Placement
	for _, pl := range plan.Layout {
		if pl.Name == "oil-refinery" && pl.Recipe == basicOil {
			refs = append(refs, pl)
		}
	}
	states, err := entityStates(h, refs)
	if err != nil {
		return err
	}
	var on, off []int // номера в refs: с рецептом и стоящие без
	for i, st := range states {
		switch {
		case st == nil:
		case st.Recipe == basicOil:
			on = append(on, i)
		case st.Recipe == "":
			off = append(off, i)
		}
	}
	step := gasDecide(gas, light, len(on), len(off))
	h.Log("газ по уровню: газ %.0f%%, лёгкая %.0f%%, НПЗ простой переработки %d с рецептом, %d без", gas*100, light*100, len(on), len(off))
	state := loadGasOff()
	switch step {
	case gasTurnOff:
		pl, st := refs[on[0]], states[on[0]]
		h.Say("газ %.0f%%, лёгкая %.0f%% — НПЗ простой переработки в (%.1f, %.1f) — без рецепта: поднимаю и ставлю на место", gas*100, light*100, pl.Pos.X, pl.Pos.Y)
		if err := h.Pickup(ctx, st.Entity); err != nil {
			return err
		}
		if _, err := h.Place(ctx, h.Protos.ItemFor(pl.Name), pl.Pos, pl.Dir); err != nil {
			return err
		}
		if !slices.Contains(state.Off, pl.Pos) {
			state.Off = append(state.Off, pl.Pos)
		}
		return writeState(gasOffFile, state)
	case gasTurnOn:
		pl, st := refs[off[0]], states[off[0]]
		h.Say("газ %.0f%%, лёгкая %.0f%% — НПЗ простой переработки в (%.1f, %.1f) — рецепт обратно", gas*100, light*100, pl.Pos.X, pl.Pos.Y)
		if err := h.SetRecipe(ctx, st.Entity, basicOil); err != nil {
			return err
		}
		state.Off = slices.DeleteFunc(state.Off, func(p bridge.Pos) bool { return p == pl.Pos })
		return writeState(gasOffFile, state)
	case gasFlush:
		h.Say("газ %.0f%%, лёгкая %.0f%%, НПЗ простой переработки выключены — сливаю сеть газа", gas*100, light*100)
		return h.FlushPipeline(ctx, gasTank.Entity, gasFluid)
	}
	return nil
}
