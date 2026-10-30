package stage

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадии 32–41 (stages.md): стройка проекта синей науки по одной части — исследования
// (поставить заранее, дождаться), постройки части из плана стадии 5, стройка, проверка (всё
// стоит и настроено — сверка с планом).

// blueStage — описание стадии стройки синей.
type blueStage struct {
	n      int
	name   string
	file   string                      // раскладка стадии: один раз на карту
	wait   []string                    // исследования: изучены (иначе — ждать)
	queue  []string                    // исследования: в очередь в самом начале (на следующие стадии)
	pick   func(*cluster.Cluster) bool // кластеры части
	more   func(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error)
	before func(ctx context.Context, h *act.Hands) error // до раскладки стадии (переделка построенного)
	after  func(ctx context.Context, h *act.Hands) error // после стройки
	// fact — проверка по факту работы (stages.md: машины работают, жидкость дошла, лаборатории
	// работают на синей): ждать до factWait, не подтвердилось — стадия не выполнена (решение
	// пользователя 28.10: жёстко все)
	fact func(h *act.Hands, list []layout.Placement) error
}

// factWait — сколько ждать подтверждения по факту работы; factPoll — шаг опроса.
const (
	factWait = 10 * time.Minute
	factPoll = 15 * time.Second
)

// waitFact — ждать, пока fact подтвердится, до factWait; возвращает последнюю причину.
func waitFact(ctx context.Context, h *act.Hands, list []layout.Placement, fact func(*act.Hands, []layout.Placement) error) error {
	h.Say("жду подтверждения по факту работы — до %v", factWait) // ожидание без строк в журнале принимали за зависание
	deadline := time.Now().Add(factWait)
	for {
		err := fact(h, list)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(factPoll):
		}
	}
}

// working — хоть одна постройка из list с именем из names работает (статус working).
func working(names ...string) func(h *act.Hands, list []layout.Placement) error {
	return func(h *act.Hands, list []layout.Placement) error {
		seen := map[string]int{}
		for _, pl := range list {
			if !slices.Contains(names, pl.Name) {
				continue
			}
			st, err := h.B.GetEntity(pl.Name, pl.Pos)
			if err != nil || st == nil {
				continue
			}
			if st.Status == "working" {
				return nil
			}
			seen[st.Status]++
		}
		return fmt.Errorf("%s не работают: %v", strings.Join(names, "/"), seen)
	}
}

// holds — хоть в одной постройке из list с именем из names есть item (жидкость в трубе, пакет в
// лаборатории, нефть у качалки).
func holds(item string, names ...string) func(h *act.Hands, list []layout.Placement) error {
	return func(h *act.Hands, list []layout.Placement) error {
		n := 0
		for _, pl := range list {
			if !slices.Contains(names, pl.Name) {
				continue
			}
			st, err := h.B.GetEntity(pl.Name, pl.Pos)
			if err != nil || st == nil {
				continue
			}
			n++
			if st.Fluids[item] > 0 {
				return nil
			}
			for _, inv := range st.Inventories {
				if inv[item] > 0 {
					return nil
				}
			}
		}
		return fmt.Errorf("%s нет ни в одной из %d %s", item, n, strings.Join(names, "/"))
	}
}

// inTanks — fluid дошла до склада: есть в цистернах складов (раскладка стадии 33), а не только у
// качалок (стадия 34 на 859088572: трубы месторождений не строились — до склада не дошло ничего).
func inTanks(fluid string) func(h *act.Hands, list []layout.Placement) error {
	return func(h *act.Hands, _ []layout.Placement) error {
		tanks, err := blueStages[33].layout(h)
		if err != nil {
			return err
		}
		return holds(fluid, "storage-tank")(h, tanks)
	}
}

// labsOn — лаборатории плана работают на item: хоть одна в статусе «работает», а текущее
// исследование требует item (пакет в лаборатории тратится сразу — его наличие опрос не застаёт;
// в раскладке стадии 41 лабораторий нет — они со стадий 12–21).
func labsOn(item string) func(h *act.Hands, list []layout.Placement) error {
	return func(h *act.Hands, _ []layout.Placement) error {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		needs := false
		for _, in := range h.Protos.Technologies[f.Current].UnitIngredients {
			needs = needs || in.Name == item
		}
		if !needs {
			return fmt.Errorf("текущее исследование %q не на %s", f.Current, item)
		}
		plan, err := LoadBluePlan()
		if err != nil {
			return err
		}
		var labs []layout.Placement
		for _, pl := range plan.Layout {
			if h.Protos.Entities[pl.Name].Type == "lab" {
				labs = append(labs, pl)
			}
		}
		return working("lab")(h, labs)
	}
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
	// буферы выхода (cluster.md §6а: у каждого кластера с продуктом-предметом) — вместе с
	// кластерами стадии
	more := []func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error){endBuffers(h, pick)}
	if s.more != nil {
		more = append(more, s.more(h))
	}
	return blueAddedFor(h, s.file, pick, false, more...)
}

func (s blueStage) run(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, s.queue...); err != nil {
		return err
	}
	if err := waitResearch(ctx, h, s.wait...); err != nil {
		return err
	}
	if s.before != nil {
		if err := s.before(ctx, h); err != nil {
			return err
		}
	}
	list, err := s.layout(h)
	if err != nil {
		return err
	}
	h.Say("%s: %d построек по плану стадии 5", s.name, len(list))
	if err := buildSorted(ctx, h, list, blueOrder); err != nil {
		return err
	}
	if s.fact != nil {
		if err := waitFact(ctx, h, list, s.fact); err != nil {
			return fmt.Errorf("%s: по факту не работает за %v: %w", s.name, factWait, err)
		}
		h.Say("%s: по факту работает", s.name)
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
			}, fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 33, name: "склады цистерн", file: "state-tanks.json", queue: []string{"fluid-handling", "oil-gathering", "engine"},
			wait: []string{"fluid-handling"}, pick: func(c *cluster.Cluster) bool { return c.Kind == cluster.Tank }},
		{n: 34, name: "нефть: качалки и трасса", file: "state-oil.json", wait: []string{"oil-gathering", "engine"}, // engine — насосы трассы
			pick: func(c *cluster.Cluster) bool { return c.IsOil() }, fact: inTanks("crude-oil")},
		{n: 35, name: "вода для химии", file: "state-water.json",
			more: tagsPrefix(cluster.PipeLaneTag("bus-1", "water")), fact: holds("water", "pipe", "pipe-to-ground")},
		{n: 36, name: "НПЗ", file: "state-refinery.json", wait: []string{"oil-processing"},
			queue: []string{"plastics", "sulfur-processing"}, pick: func(c *cluster.Cluster) bool { return c.IsRefinery() },
			before: oilReady,
			more:   tagsPrefix(cluster.PipeLaneTag("bus-1", "crude-oil"), cluster.PipeLaneTag("bus-2", "petroleum-gas")),
			fact:   working("oil-refinery")},
		{n: 37, name: "пластик и сера", file: "state-chem.json", wait: []string{"plastics", "sulfur-processing"},
			queue: []string{"advanced-circuit", "engine"}, pick: func(c *cluster.Cluster) bool { return c.IsChem() },
			fact: working("chemical-plant")},
		{n: 38, name: "двигатели", file: "state-engines.json", wait: []string{"engine", "automation-2"},
			pick: product("engine-unit"), more: pipeChest, fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 39, name: "красные схемы", file: "state-adv-circuits.json", wait: []string{"advanced-circuit"},
			pick: product("advanced-circuit"), fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 40, name: "синяя наука", file: "state-blue-science.json", wait: []string{bluePack},
			pick: product(bluePack), fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 41, name: "синяя в лаборатории", file: "state-labs-blue.json",
			pick: func(c *cluster.Cluster) bool { return c.Kind == cluster.Lab }, fact: labsOn(bluePack),
			// синяя в лабораториях — исследования и на ней (rocketTechs; решение пользователя 26.10):
			// до стройки и на синей — первыми, иначе к проверке по факту текущее — не на синей
			// (очередь пуста или забита красными и зелёными: стадия 41 на 267145981)
			before: func(ctx context.Context, h *act.Hands) error {
				if err := os.WriteFile(runPath(blueOnFile), []byte("{}\n"), 0o644); err != nil {
					return err
				}
				return queueResearch(ctx, h, bluePack)
			},
			after: func(ctx context.Context, h *act.Hands) error { return queueResearch(ctx, h) }},
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
