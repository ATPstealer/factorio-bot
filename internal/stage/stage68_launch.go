package stage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 68 (stages.md): ждать запуска — ракета улетит сама, как будут 100 частей (галочка —
// стадия 67). Пока ждём — по порядку: модули продуктивности 1 (сначала шахта, потом машины, что
// стоят без сырья: узкое место — вход, продуктивность даёт больше из того же; не туда, где узкое
// место — сама машина), потом ленты — на быстрые поверх (подачи bus-1, шины, кластеры). Запуск —
// по счёту запущенных ракет (мод, get_force).

const (
	prodModule = "productivity-module"
	launchPoll = 30 * time.Second
	// fastBatch — лент за раз между проверками «не запущена ли»
	fastBatch = 200
)

// fastTier — быстрая лента, подземка, делитель вместо обычных.
var fastTier = map[string]string{"transport-belt": "fast-transport-belt", "underground-belt": "fast-underground-belt",
	"splitter": "fast-splitter"}

func init() {
	register(Stage{N: 68, Name: "запуск", Run: runLaunch, Check: checkLaunch})
}

// launched — ракета запущена.
func launched(h *act.Hands) (bool, error) {
	f, err := h.B.Force()
	return f.RocketsLaunched > 0, err
}

func runLaunch(ctx context.Context, h *act.Hands) error {
	for _, step := range []func(context.Context, *act.Hands) error{fillModules, fastBelts} {
		if ok, err := launched(h); err != nil || ok {
			return err
		}
		// пока ждём — по делу, а не критерий: не вышло — сказать и ждать дальше (стадия 68 на
		// 1098794301: «не сойти с ленты» на быстрых лентах ронял стадию — игра стояла, ракета
		// не собиралась)
		if err := step(ctx, h); err != nil {
			if ctx.Err() != nil {
				return err
			}
			h.Say("пока ждём запуска — не вышло: %v", err)
		}
	}
	h.Say("жду запуска ракеты")
	for {
		if ok, err := launched(h); err != nil || ok {
			return err
		}
		if err := sleepCtx(ctx, launchPoll); err != nil {
			return err
		}
	}
}

func checkLaunch(h *act.Hands) error {
	ok, err := launched(h)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("ракета не запущена")
	}
	return nil
}

// fillModules — модули продуктивности в машины плана с рецептом под продуктивность: шахта —
// первой, потом стоящие без сырья; ячейки — целиком. Модуль — из буфера кластера модулей, нет —
// крафт (have).
func fillModules(ctx context.Context, h *act.Hands) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	var machines []layout.Placement
	for _, pl := range plan.Layout {
		if h.Protos.Entities[pl.Name].ModuleSlots > 0 && pl.Recipe != "" && h.Protos.Recipes[pl.Recipe].Productivity {
			machines = append(machines, pl)
		}
	}
	states, err := entityStates(h, machines)
	if err != nil {
		return err
	}
	type todo struct {
		pl   layout.Placement
		n    int
		silo bool
	}
	var list []todo
	for i, st := range states {
		if st == nil {
			continue
		}
		pl := machines[i]
		n := h.Protos.Entities[pl.Name].ModuleSlots - st.Inventories["crafter_modules"][prodModule]
		silo := pl.Name == "rocket-silo"
		if n > 0 && (silo || starvedStatus[st.Status] || st.Status == "fluid_ingredient_shortage") {
			list = append(list, todo{pl, n, silo})
		}
	}
	slices.SortStableFunc(list, func(a, b todo) int {
		if a.silo != b.silo {
			if a.silo {
				return -1
			}
			return 1
		}
		return 0
	})
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites, useStore: true}
	for _, t := range list {
		e, err := h.B.EntityAt(t.pl.Name, t.pl.Pos)
		if err != nil {
			return err
		}
		if e == nil {
			continue
		}
		if err := s.have(ctx, prodModule, t.n); err != nil {
			return err
		}
		h.Say("модули продуктивности ×%d в %s (%s)", t.n, t.pl.Name, t.pl.Recipe)
		if err := h.Insert(ctx, *e, prodModule, t.n); err != nil {
			return err
		}
	}
	return nil
}

// fastBelts — ленты плана — на быстрые поверх, по порядку (stages.md): подачи bus-1 (ряды блоков
// добычи и подводы от них), шины, кластеры; частями, между ними — не запущена ли ракета.
// Заменённое — в план (сверка видит быстрые); снятое отключением науки — не ставить.
func fastBelts(ctx context.Context, h *act.Hands) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	mine := map[string]bool{} // теги блоков добычи и их подводов в bus-1
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Mine && !c.IsOil() {
			mine[cluster.Tag(c.Name)] = true
			mine[cluster.LogisticsTag+"/bus-1/"+c.Name] = true
		}
	}
	off := loadScienceOff()
	var parts [3][]int // номера в plan.Layout: подачи bus-1, шины, остальное
	for i, pl := range plan.Layout {
		if fastTier[pl.Name] == "" || off.has(pl) {
			continue
		}
		base, _, _ := strings.Cut(strings.TrimPrefix(pl.Tag, "c/"), "/") // c/<блок>/later — тоже блок
		k := 2
		switch {
		case mine[pl.Tag] || mine[cluster.Tag(base)]:
			k = 0
		case strings.HasPrefix(pl.Tag, "c/bus-"):
			k = 1
		}
		parts[k] = append(parts[k], i)
	}
	order := []string{"fast-transport-belt", "fast-underground-belt", "fast-splitter"}
	for _, idx := range parts {
		for from := 0; from < len(idx); from += fastBatch {
			if ok, err := launched(h); err != nil || ok {
				return err
			}
			batch := idx[from:min(from+fastBatch, len(idx))]
			list := make([]layout.Placement, len(batch))
			for j, i := range batch {
				list[j] = plan.Layout[i]
				list[j].Name = fastTier[list[j].Name]
			}
			h.Say("ленты на быстрые: %d (%d–%d из %d)", len(list), from+1, from+len(batch), len(idx))
			if err := buildSorted(ctx, h, list, order); err != nil {
				return err
			}
			for _, i := range batch {
				plan.Layout[i].Name = fastTier[plan.Layout[i].Name]
			}
			if err := writeState(planFile, plan); err != nil {
				return err
			}
		}
	}
	return nil
}
