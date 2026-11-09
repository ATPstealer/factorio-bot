package stage

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
)

// Стадии 32–41 (stages.md): стройка проекта синей науки по одной части — исследования
// (поставить заранее, дождаться), постройки плана этой стадии (plan_stages.go), стройка,
// проверка (всё стоит и настроено — сверка с планом).

// blueStage — описание стадии стройки по плану (синей — 32–41, фиолетовой — 44–48, жёлтой — 50–61).
type blueStage struct {
	n     int
	name  string
	file  string   // раскладка стадии: один раз на карту
	wait  []string // исследования: изучены (иначе — ждать; до подготовки — Stage.Techs)
	queue []string // исследования: в очередь в самом начале (на следующие стадии)
	order []string // порядок стройки по видам (нет — blueOrder)
	// place — раскладка стадии не из построек плана своей стадии (замена на второй уровень)
	place func(h *act.Hands) ([]layout.Placement, error)
	// before — до раскладки стадии (переделка построенного)
	before func(ctx context.Context, h *act.Hands) error
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
		t0 := time.Now()
		if err := waitHooks(ctx, h); err != nil { // в ожидании — наука на ракету, лаборатории (stages.md, общее)
			return err
		}
		deadline = deadline.Add(time.Since(t0))
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
		short := map[string]int{} // ингредиент → у скольких машин его нет
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
			if r, ok := h.Protos.Recipes[st.Recipe]; ok && strings.HasSuffix(st.Status, "ingredient_shortage") {
				for _, it := range shortOf(r, st) {
					short[it]++
				}
			}
		}
		if len(short) > 0 { // чего не хватает — в причину (стадия 54 на 24996062: не узнать, железа или серы)
			return fmt.Errorf("%s не работают: %v; не хватает: %v", strings.Join(names, "/"), seen, short)
		}
		return fmt.Errorf("%s не работают: %v", strings.Join(names, "/"), seen)
	}
}

// shortOf — ингредиенты рецепта r, которых в машине st меньше, чем на один крафт (жидкости — в
// ящиках машины, предметы — в инвентарях).
func shortOf(r knowledge.Recipe, st *bridge.EntityState) []string {
	var res []string
	for _, in := range r.Ingredients {
		have := float64(st.Count(in.Name))
		if in.Type == "fluid" {
			have = st.Fluids[in.Name]
		}
		if have < in.Amount {
			res = append(res, in.Name)
		}
	}
	return res
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
		plan, err := LoadPlan()
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

// layout — постройки плана стадии (или place), один раз на карту.
func (s blueStage) layout(h *act.Hands) ([]layout.Placement, error) {
	if s.place != nil {
		return onceLayout(s.file, func() ([]layout.Placement, error) { return s.place(h) })
	}
	return addedFor(h, s.file, s.n)
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
	h.Say("%s: %d построек плана", s.name, len(list))
	order := blueOrder
	if s.order != nil {
		order = s.order
	}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	// стоящее не так (подземка не тем концом, направление, рецепт, фильтр) — исправить сейчас,
	// а не на следующем обслуживании: лента с такой подземкой оборвана (стадия 43 на 427965634)
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	if err := planFix(ctx, h, &worker{h: h, sites: sites, useStore: true}, list); err != nil {
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

// tanksThenOil — до НПЗ: склад нефти — как в плане (направление насоса на выходе: был на вход и
// качал из трубы в склад — нефть до НПЗ не шла, стадия 36 на 1114315936; план исправлен, стоящий
// насос — развернуть), потом oilReady.
func tanksThenOil(ctx context.Context, h *act.Hands) error {
	tanks, err := blueStages[33].layout(h)
	if err != nil {
		return err
	}
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	if err := planFix(ctx, h, &worker{h: h, sites: sites, useStore: true}, tanks); err != nil {
		return err
	}
	return oilReady(ctx, h)
}

func init() {
	for _, s := range []blueStage{
		{n: 32, name: "быстрые инсертеры и ленты", file: "state-fast.json", wait: []string{"logistics-2", "fast-inserter"},
			fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 33, name: "склады цистерн", file: "state-tanks.json", queue: []string{"fluid-handling", "oil-gathering", "engine"},
			wait: []string{"fluid-handling"}},
		{n: 34, name: "нефть: качалки и трасса", file: "state-oil.json", wait: []string{"oil-gathering", "engine"}, // engine — насосы трассы
			fact: inTanks("crude-oil")},
		{n: 35, name: "вода для химии", file: "state-water.json", fact: holds("water", "pipe", "pipe-to-ground")},
		{n: 36, name: "НПЗ", file: "state-refinery.json", wait: []string{"oil-processing"},
			queue: []string{"plastics", "sulfur-processing"}, before: tanksThenOil, fact: working("oil-refinery")},
		{n: 37, name: "пластик и сера", file: "state-chem.json", wait: []string{"plastics", "sulfur-processing"},
			queue: []string{"advanced-circuit", "engine"}, fact: working("chemical-plant")},
		{n: 38, name: "двигатели", file: "state-engines.json", wait: []string{"engine", "automation-2"},
			fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 39, name: "красные схемы", file: "state-adv-circuits.json", wait: []string{"advanced-circuit"},
			fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 40, name: "синяя наука", file: "state-blue-science.json", wait: []string{bluePack},
			fact: working("assembling-machine-1", "assembling-machine-2")},
		{n: 41, name: "синяя в лаборатории", file: "state-labs-blue.json", fact: labsOn(bluePack),
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
