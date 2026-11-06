package stage

import (
	"context"
	"errors"
	"fmt"
	"math"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадии 62–68 (stages.md): обслуживание после жёлтой, бетон, ракетное топливо, шахта, спутник,
// запуск.

const (
	// lastStage — последняя стадия игры: весь план — в раскладках стадий до неё.
	lastStage = 68
	// satelliteStage — спутник (крафт руками из буферов): в расчёте ограничения буферов — как
	// постройка этой стадии (cluster.md §6а).
	satelliteStage = 67
)

// rocketStages — стадии 63, 64, 66 по номеру: их раскладка — постройки плана своей стадии
// (prepare.go planOf).
var rocketStages = map[int]blueStage{}

// stageCrafts — что стадия крафтит руками без постройки (шахта, спутник): нужное стадии в
// инвентаре (neededItems) — с деревом рецептов.
var stageCrafts = map[int][]string{65: {"rocket-silo"}, satelliteStage: {"satellite"}}

// rocketOrder — порядок стройки ракетных стадий: шахта первой (по ней — ленты подачи).
var rocketOrder = append([]string{"rocket-silo"}, blueOrder...)

func init() {
	register(Stage{N: 62, Name: "обслуживание: привести к чертежу жёлтой", Run: runBlueprintYellow, Check: checkBlueprintYellow})
	for _, s := range []blueStage{
		{n: 63, name: "бетон", file: "state-concrete.json", wait: []string{"concrete"}, fact: inBuffer("concrete")},
		{n: 64, name: "ракетное топливо", file: "state-rocket-fuel.json", wait: []string{"rocket-fuel"},
			fact: inBuffer("rocket-fuel")},
		{n: 66, name: "ракетная шахта", file: "state-silo.json", wait: []string{"rocket-silo"}, order: rocketOrder,
			fact: working("rocket-silo")},
	} {
		register(Stage{N: s.n, Name: s.name, Run: s.run, Check: s.check})
		rocketStages[s.n] = s
	}
	register(Stage{N: 65, Name: "ракетная шахта — крафт", Run: runSiloCraft, Check: checkSiloCraft})
	register(Stage{N: satelliteStage, Name: "спутник", Run: runSatellite, Check: checkSatellite})
}

// Стадия 62 — как 49: чертёж (постройки плана стадий до 62) — обновить текущим кодом, сверка,
// недостающее — достроить, лишнее — разобрать; очередь исследований (бонус захвата, rocket-silo
// и всё до неё) — в начале обслуживания (maintain → queueResearch). Второй уровень — добивка:
// стоящее ниже уровня плана — поверх в начале каждой стадии (tiersAt).
func runBlueprintYellow(ctx context.Context, h *act.Hands) error {
	if err := refreshLayouts(h, lastYellowStage); err != nil {
		return err
	}
	return maintain(ctx, h, func() ([]layout.Placement, error) { return blueprint(h, planBefore(62)) }, true)
}

// checkBlueprintYellow — первого уровня у плана не осталось (печи, сборщики, инсертеры),
// построенное — по чертежу.
func checkBlueprintYellow(h *act.Hands) error {
	if err := noBaseTier(h, 62, "inserter", "stone-furnace", "assembling-machine-1"); err != nil {
		return err
	}
	return checkDrawing(h, planBefore(62))
}

// Стадия 65 — крафт шахты руками: исследование rocket-silo — ждать; материалы — из буферов
// выхода и хранения (have: нет — ждать в буферах), трубы — крафт.
func runSiloCraft(ctx context.Context, h *act.Hands) error {
	if n, err := carried(h, "rocket-silo"); err != nil || n > 0 {
		return err
	}
	if err := waitResearch(ctx, h, "rocket-silo"); err != nil {
		return err
	}
	return craftFromBuffers(ctx, h, "rocket-silo")
}

func checkSiloCraft(h *act.Hands) error {
	n, err := carried(h, "rocket-silo")
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("ракетной шахты нет ни в инвентаре, ни в хранении")
	}
	return nil
}

// craftFromBuffers — item ×1 руками: каждый ингредиент — сначала набрать (хранение, буферы выхода,
// ждать их; что делается руками — крафт), потом крафт.
func craftFromBuffers(ctx context.Context, h *act.Hands, item string) error {
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites, useStore: true}
	for _, in := range h.Protos.Recipes[item].Ingredients {
		h.Say("%s: набираю %s ×%v", item, in.Name, in.Amount)
		if err := s.have(ctx, in.Name, int(in.Amount)); err != nil {
			return err
		}
	}
	return s.have(ctx, item, 1)
}

// carried — сколько item в инвентаре и в хранении.
func carried(h *act.Hands, item string) (int, error) {
	p, err := h.B.Player()
	if err != nil {
		return 0, err
	}
	n := p.Inventory[item]
	list, err := storageLayout()
	if err != nil {
		return n, nil
	}
	states, err := entityStates(h, list)
	if err != nil {
		return 0, err
	}
	for _, st := range states {
		if st != nil {
			n += st.Inventories["chest"][item]
		}
	}
	return n, nil
}

// errNoMechanics — нужного действия или сенсора ещё нет (plan.md, часть Г).
var errNoMechanics = errors.New("нет механики (plan.md, часть Г)")

// siloEntity — стоящая ракетная шахта плана.
func siloEntity(h *act.Hands) (*bridge.EntityState, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	for _, pl := range plan.Layout {
		if pl.Name == "rocket-silo" {
			st, err := h.B.GetEntity(pl.Name, pl.Pos)
			if err != nil {
				return nil, err
			}
			if st == nil {
				return nil, fmt.Errorf("шахта в (%.1f, %.1f) не стоит", pl.Pos.X, pl.Pos.Y)
			}
			return st, nil
		}
	}
	return nil, errors.New("шахты нет в плане")
}

// landingPad — посадочная площадка: без неё ракета со спутником не летит (итог запуска — 1000
// космических пакетов — игре некуда опустить; 1098794301: rocket_ready, галочка стоит — ждёт).
const landingPad = "cargo-landing-pad"

// padRadius — площадка — не дальше от шахты.
const padRadius = 40

// padEntity — стоящая площадка (одна на поверхность) у шахты.
func padEntity(h *act.Hands, silo bridge.Entity) (*bridge.Entity, error) {
	es, err := h.B.FindNearest(bridge.FindArgs{Name: landingPad, Radius: 4 * padRadius, Position: &silo.Position, Limit: 1})
	if err != nil || len(es) == 0 {
		return nil, err
	}
	return &es[0], nil
}

// placePad — площадка: крафт руками из буферов, постановка рядом с шахтой на свободное место не
// из плана, с тайлом прохода вокруг.
func placePad(ctx context.Context, h *act.Hands, silo bridge.Entity) error {
	if e, err := padEntity(h, silo); err != nil || e != nil {
		return err
	}
	if n, err := carried(h, landingPad); err != nil {
		return err
	} else if n == 0 {
		if err := craftFromBuffers(ctx, h, landingPad); err != nil {
			return err
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	taken := map[world.Tile]bool{}
	for _, pl := range plan.Layout {
		for _, t := range layout.Tiles(h.Protos, pl) {
			taken[t] = true
		}
	}
	// площадка 8×8 и проход вокруг; ставится по сетке 2×2 (build_grid_size = 2: центр — на чётных
	// координатах, клик в (173, -211) ставил в (174, -210) — 1098794301): место на тайл шире, центр
	// — чётный из двух
	size := 8 + 2 + 1
	spot, ok := h.World.FreeSpotWhere(silo.Position, size, size, padRadius, nil, func(t world.Tile) bool { return !taken[t] })
	if !ok {
		return fmt.Errorf("площадка: нет свободного места %d×%d в %d тайлах от шахты", size, size, padRadius)
	}
	even := func(v float64) float64 { return 2 * math.Floor(v/2) } // v — x.5: чётный из v±0.5
	spot = bridge.Pos{X: even(spot.X + 0.5), Y: even(spot.Y + 0.5)}
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites, useStore: true}
	if err := s.have(ctx, landingPad, 1); err != nil { // в хранении — в руки
		return err
	}
	h.Say("посадочная площадка — в (%.1f, %.1f)", spot.X, spot.Y)
	_, err = h.Place(ctx, landingPad, spot, 0)
	return err
}

// Стадия 67 — спутник: площадка; спутник — крафт руками из буферов; в груз шахты (Ctrl+клик) и
// галочка «запуск по готовности» в окне шахты.
func runSatellite(ctx context.Context, h *act.Hands) error {
	// ракета уже улетела — второй спутник не нужен (1098794301: площадка встала — ракета улетела
	// со спутником, стадия пошла крафтить новый)
	if ok, err := launched(h); err != nil || ok {
		return err
	}
	silo, err := siloEntity(h)
	if err != nil {
		return err
	}
	if err := placePad(ctx, h, silo.Entity); err != nil {
		return err
	}
	if silo.Inventories["rocket_silo_rocket"]["satellite"] == 0 {
		sites, err := outpostsLayout(h)
		if err != nil {
			return err
		}
		if n, err := carried(h, "satellite"); err != nil {
			return err
		} else if n == 0 {
			if err := craftFromBuffers(ctx, h, "satellite"); err != nil {
				return err
			}
		}
		s := &worker{h: h, sites: sites, useStore: true}
		if err := s.have(ctx, "satellite", 1); err != nil { // в хранении — в руки
			return err
		}
		h.Say("спутник — в груз шахты")
		if err := h.SiloCargo(ctx, silo.Entity, "satellite"); err != nil {
			return err
		}
	}
	return h.SiloAutoLaunch(ctx, silo.Entity)
}

// checkSatellite — площадка стоит, спутник в грузе шахты, галочка автозапуска стоит (мод).
func checkSatellite(h *act.Hands) error {
	if ok, err := launched(h); err != nil || ok {
		return err
	}
	silo, err := siloEntity(h)
	if err != nil {
		return err
	}
	if e, err := padEntity(h, silo.Entity); err != nil {
		return err
	} else if e == nil {
		return errors.New("посадочной площадки нет")
	}
	if silo.Inventories["rocket_silo_rocket"]["satellite"] == 0 {
		return errors.New("спутника в грузе шахты нет")
	}
	if !silo.AutoLaunch {
		return errors.New("галочка автозапуска не стоит")
	}
	return nil
}
