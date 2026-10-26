package stage

import (
	"context"
	"fmt"
	"os"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадии 32–41 (stages.md): стройка проекта синей науки по одной части — исследования
// (поставить заранее, дождаться), постройки части из плана стадии 7, стройка, проверка (всё
// стоит и настроено — сверка с планом).

// blueStage — описание стадии стройки синей.
type blueStage struct {
	n     int
	name  string
	file  string                      // раскладка стадии: один раз на карту
	wait  []string                    // исследования: изучены (иначе — ждать)
	queue []string                    // исследования: в очередь в самом начале (на следующие стадии)
	pick  func(*cluster.Cluster) bool // кластеры части
	more  func(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error)
	after func(ctx context.Context, h *act.Hands) error // после стройки
}

// blueOrder — порядок стройки: машины, жидкости, инсертеры, ленты, столбы.
var blueOrder = []string{"offshore-pump", "pumpjack", "storage-tank", "oil-refinery", "chemical-plant",
	"assembling-machine-2", "assembling-machine-1", "stone-furnace", "pump", "pipe-to-ground", "pipe", chest,
	"inserter", "long-handed-inserter", "fast-inserter", "transport-belt", "underground-belt", "splitter",
	"small-electric-pole"}

func (s blueStage) layout(h *act.Hands) ([]layout.Placement, error) {
	pick := s.pick
	if pick == nil {
		pick = func(*cluster.Cluster) bool { return false }
	}
	if s.more == nil {
		return blueAddedFor(h, s.file, pick, false)
	}
	return blueAddedFor(h, s.file, pick, false, s.more(h))
}

func (s blueStage) run(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, s.queue...); err != nil {
		return err
	}
	if err := waitResearch(ctx, h, s.wait...); err != nil {
		return err
	}
	list, err := s.layout(h)
	if err != nil {
		return err
	}
	h.Say("%s: %d построек по плану стадии 7", s.name, len(list))
	if err := buildSorted(ctx, h, list, blueOrder); err != nil {
		return err
	}
	if s.after != nil {
		return s.after(ctx, h)
	}
	return nil
}

// check — всё стоит и настроено (направление, рецепт, фильтр) — сверка с планом.
func (s blueStage) check(h *act.Hands) error {
	list, err := s.layout(h)
	if err != nil {
		return err
	}
	diffs, err := planAudit(h, list)
	if err != nil {
		return err
	}
	if len(diffs) > 0 {
		return fmt.Errorf("%s: %d расхождений с планом, первое — %v", s.name, len(diffs), diffs[0])
	}
	return nil
}

// tagsPrefix — постройки плана с тегом, начинающимся с одного из prefixes (трубы шин, помпа).
func tagsPrefix(prefixes ...string) func(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error) {
	return func(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error) {
		return func(plan *ClusterPlan, _ *layout.Layout) ([]layout.Placement, error) {
			added, _, err := blueAdded()
			if err != nil {
				return nil, err
			}
			var res []layout.Placement
			for _, pl := range added {
				for _, p := range prefixes {
					if strings.HasPrefix(pl.Tag, p) {
						res = append(res, pl)
						break
					}
				}
			}
			return res, nil
		}
	}
}

func product(p string) func(*cluster.Cluster) bool {
	return func(c *cluster.Cluster) bool { return c.Kind == cluster.Make && c.Product == p }
}

func init() {
	for _, s := range []blueStage{
		{n: 32, name: "быстрые инсертеры и ленты", file: "state-fast.json", wait: []string{"logistics-2", "fast-inserter"},
			pick: func(c *cluster.Cluster) bool {
				return product("fast-transport-belt")(c) || product("fast-inserter")(c)
			}},
		{n: 33, name: "склады цистерн", file: "state-tanks.json", queue: []string{"fluid-handling", "oil-gathering"},
			wait: []string{"fluid-handling"}, pick: func(c *cluster.Cluster) bool { return c.Kind == cluster.Tank }},
		{n: 34, name: "нефть: качалки и трасса", file: "state-oil.json", wait: []string{"oil-gathering"},
			pick: func(c *cluster.Cluster) bool { return c.IsOil() }},
		{n: 35, name: "вода для химии", file: "state-water.json",
			more: tagsPrefix(cluster.PipeLaneTag("bus-1", "water"))},
		{n: 36, name: "НПЗ", file: "state-refinery.json", wait: []string{"oil-processing"},
			queue: []string{"plastics", "sulfur-processing"}, pick: func(c *cluster.Cluster) bool { return c.IsRefinery() },
			more: tagsPrefix(cluster.PipeLaneTag("bus-1", "crude-oil"), cluster.PipeLaneTag("bus-2", "petroleum-gas"))},
		{n: 37, name: "пластик и сера", file: "state-chem.json", wait: []string{"plastics", "sulfur-processing"},
			queue: []string{"advanced-circuit", "engine"}, pick: func(c *cluster.Cluster) bool { return c.IsChem() }},
		{n: 38, name: "двигатели", file: "state-engines.json", wait: []string{"engine", "automation-2"},
			pick: product("engine-unit"), more: pipeChest},
		{n: 39, name: "красные схемы", file: "state-adv-circuits.json", wait: []string{"advanced-circuit"},
			pick: product("advanced-circuit")},
		{n: 40, name: "синяя наука", file: "state-blue-science.json", wait: []string{bluePack},
			pick: product(bluePack)},
		{n: 41, name: "синяя в лаборатории", file: "state-labs-blue.json",
			pick: func(c *cluster.Cluster) bool { return c.Kind == cluster.Lab },
			after: func(ctx context.Context, h *act.Hands) error {
				// синяя в лабораториях — исследования и на ней (rocketTechs; решение пользователя 26.10)
				if err := os.WriteFile(runPath(blueOnFile), []byte("{}\n"), 0o644); err != nil {
					return err
				}
				return queueResearch(ctx, h)
			}},
	} {
		register(Stage{N: s.n, Name: s.name, Run: s.run, Check: s.check})
		blueStages[s.n] = s
	}
}

// blueStages — стадии 32–41 по номеру: их раскладка — план стадии для проверки перед стройкой
// и «нужного» (prepare.go planOf).
var blueStages = map[int]blueStage{}

// pipeChest — из одного сборщика труб кластера двигателей — инсертер в сундук: буфер бота
// (stages.md, стадия 38: трубы на стройку нефти и химии).
func pipeChest(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error) {
	return func(plan *ClusterPlan, all *layout.Layout) ([]layout.Placement, error) {
		for _, c := range plan.Clusters {
			if !product("engine-unit")(c) {
				continue
			}
			for _, pl := range all.List {
				if pl.Tag != cluster.Tag(c.Name) || pl.Recipe != "pipe" || !strings.HasPrefix(pl.Name, "assembling-machine") {
					continue
				}
				m := world.TileOf(pl.Pos) // центр сборщика 3×3
				for _, d := range []int{layout.N, layout.S, layout.E, layout.W} {
					ins := layout.Step(layout.Step(m, d), d)
					box := layout.Step(ins, d)
					if all.At(ins) != nil || all.At(box) != nil {
						continue
					}
					if ok, _ := h.World.Buildable(ins); !ok {
						continue
					}
					if ok, _ := h.World.Buildable(box); !ok {
						continue
					}
					tag := cluster.Tag(c.Name) + "/pipes"
					in := layout.Placement{Name: "inserter", Pos: ins.Center(),
						Dir: layout.InserterDir(h.Protos, "inserter", ins, layout.Step(ins, (d+8)%16)), Tag: tag, Recipe: "pipe"}
					return []layout.Placement{in, {Name: "iron-chest", Pos: box.Center(), Tag: tag}}, nil
				}
			}
		}
		return nil, fmt.Errorf("у сборщиков труб двигателей нет места под сундук")
	}
}
