package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадии 32–41 (stages.md): стройка проекта синей науки по одной части — исследования
// (поставить заранее, дождаться), постройки части из плана стадии 5, стройка, проверка (всё
// стоит и настроено — сверка с планом).

// blueStage — описание стадии стройки по проекту (синей — 32–41, фиолетовой — 43–48).
type blueStage struct {
	n     int
	name  string
	file  string                      // раскладка стадии: один раз на карту
	src   planSource                  // проект (пусто — синей)
	wait  []string                    // исследования: изучены (иначе — ждать; до подготовки — Stage.Techs)
	queue []string                    // исследования: в очередь в самом начале (на следующие стадии)
	pick  func(*cluster.Cluster) bool // кластеры части
	// buffers — чьи буферы выхода (cluster.md §6а) ставит стадия: в конце ленты продукта на
	// шине; нет — у всех кластеров части (pick). Старый буфер кластера не на этом месте (лента
	// продлена проектом, кирпич — с выходной ленты на bus-2) — снимается (staleBuffers)
	buffers func(*cluster.Cluster) bool
	more    func(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error)
	before  func(ctx context.Context, h *act.Hands) error // до раскладки стадии (переделка построенного)
	// built — сразу после стройки, до проверки по факту (запуск: уголь в новые бойлеры)
	built func(ctx context.Context, h *act.Hands, list []layout.Placement) error
	after func(ctx context.Context, h *act.Hands) error // после стройки и проверки по факту
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

// blueOrder — порядок стройки: машины (станция, добыча, печи, сборщики), жидкости, сундуки,
// инсертеры, ленты, столбы; обоих уровней (фиолетовые стадии — второй).
var blueOrder = []string{"offshore-pump", "pumpjack", "storage-tank", "oil-refinery", "chemical-plant",
	"boiler", "steam-engine", "electric-mining-drill", "assembling-machine-2", "assembling-machine-1",
	"steel-furnace", "stone-furnace", "pump", "pipe-to-ground", "pipe", ironChest, "steel-chest",
	"burner-inserter", "inserter", "long-handed-inserter", "fast-inserter", "transport-belt", "fast-transport-belt",
	"underground-belt", "fast-underground-belt", "splitter", "fast-splitter", "small-electric-pole", "medium-electric-pole"}

func (s blueStage) source() planSource {
	if s.src.stage == 0 {
		return blueSource
	}
	return s.src
}

func (s blueStage) layout(h *act.Hands) ([]layout.Placement, error) {
	pick := s.pick
	if pick == nil {
		pick = func(*cluster.Cluster) bool { return false }
	}
	// буферы выхода (cluster.md §6а: у каждого кластера с продуктом-предметом) — вместе с
	// кластерами стадии
	more := []func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error){endBuffers(h, s.bufferPick())}
	if s.more != nil {
		more = append(more, s.more(h))
	}
	return addedFor(h, s.file, s.source(), pick, false, more...)
}

// bufferPick — чьи буферы выхода ставит стадия (buffers, иначе pick).
func (s blueStage) bufferPick() func(*cluster.Cluster) bool {
	if s.buffers != nil {
		return s.buffers
	}
	if s.pick != nil {
		return s.pick
	}
	return func(*cluster.Cluster) bool { return false }
}

// staleBuffers — буферы выхода кластеров стадии (bufferPick) из раскладок прошлых стадий, которых
// нет в раскладке этой: лента продукта продлена проектом — буфер в конце переезжает; кирпич — с
// выходной ленты кластера в конец ленты bus-2 (stages.md, стадия 43). Файл раскладки → список.
func (s blueStage) staleBuffers(h *act.Hands) (map[string][]layout.Placement, error) {
	list, err := s.layout(h)
	if err != nil {
		return nil, err
	}
	plan, err := s.source().load()
	if err != nil {
		return nil, err
	}
	pick := s.bufferPick()
	tags := map[string]bool{}
	for _, c := range plan.Clusters {
		if (c.Kind == cluster.Make || c.Kind == cluster.Smelt) && pick(c) {
			tags["outbuf/"+c.Name] = true
		}
	}
	now := map[world.Tile]bool{}
	for _, pl := range list {
		if strings.HasPrefix(pl.Tag, "outbuf/") {
			now[world.TileOf(pl.Pos)] = true
		}
	}
	res := map[string][]layout.Placement{}
	names, lists := stateLayouts()
	for _, name := range names {
		if name == s.file {
			continue
		}
		for _, pl := range lists[name] {
			if tags[pl.Tag] && !now[world.TileOf(pl.Pos)] {
				res[name] = append(res[name], pl)
			}
		}
	}
	return res, nil
}

// dropStale — старые буферы выхода не на новом месте (staleBuffers) снять: содержимое и сами —
// в хранение; из файлов раскладок убрать (проверки и буферы бота ищут их по файлам).
func (s blueStage) dropStale(ctx context.Context, h *act.Hands) error {
	stale, err := s.staleBuffers(h)
	if err != nil || len(stale) == 0 {
		return err
	}
	var es []bridge.Entity
	for _, list := range stale {
		for _, pl := range list {
			if e, err := h.B.EntityAt(pl.Name, pl.Pos); err == nil && e != nil {
				es = append(es, *e)
			}
		}
	}
	if len(es) > 0 {
		h.Say("%s: старые буферы выхода не на месте — снимаю %d построек", s.name, len(es))
		if err := h.PickupAll(ctx, es); err != nil {
			return err
		}
		sites, err := outpostsLayout(h)
		if err != nil {
			return err
		}
		w := &worker{h: h, sites: sites, useStore: true}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		if err := w.store(ctx, p.InventorySize-10); err != nil {
			return err
		}
	}
	for name, list := range stale {
		if err := dropFromLayout(name, list); err != nil {
			return err
		}
	}
	return nil
}

// dropFromLayout — убрать list из файла раскладки file.
func dropFromLayout(file string, list []layout.Placement) error {
	data, err := os.ReadFile(runPath(file))
	if err != nil {
		return err
	}
	var all []layout.Placement
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	gone := map[layout.Placement]bool{}
	for _, pl := range list {
		gone[pl] = true
	}
	all = slices.DeleteFunc(all, func(pl layout.Placement) bool { return gone[pl] })
	return writeState(file, all)
}

// projectStage — стадия стройки по проекту (синей 32–41, фиолетовой 43–48).
func projectStage(n int) (blueStage, bool) {
	if s, ok := blueStages[n]; ok {
		return s, true
	}
	s, ok := purpleStages[n]
	return s, ok
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
	if err := s.dropStale(ctx, h); err != nil {
		return err
	}
	list, err := s.layout(h)
	if err != nil {
		return err
	}
	h.Say("%s: %d построек по плану стадии %d", s.name, len(list), s.source().stage)
	if err := buildSorted(ctx, h, list, blueOrder); err != nil {
		return err
	}
	if s.built != nil {
		if err := s.built(ctx, h, list); err != nil {
			return err
		}
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
