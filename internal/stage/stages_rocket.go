package stage

import (
	"context"
	"errors"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
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

// Стадия 67 — спутник: крафт руками из буферов; в груз шахты (Ctrl+клик) и галочка «запуск по
// готовности» в окне шахты.
func runSatellite(ctx context.Context, h *act.Hands) error {
	silo, err := siloEntity(h)
	if err != nil {
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

// checkSatellite — спутник в грузе шахты, галочка автозапуска стоит (мод).
func checkSatellite(h *act.Hands) error {
	silo, err := siloEntity(h)
	if err != nil {
		return err
	}
	if silo.Inventories["rocket_silo_rocket"]["satellite"] == 0 {
		return errors.New("спутника в грузе шахты нет")
	}
	if !silo.AutoLaunch {
		return errors.New("галочка автозапуска не стоит")
	}
	return nil
}
