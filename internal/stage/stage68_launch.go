package stage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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
	// fastBatch — лент за раз между проверками «не запущена ли» и «не пора ли к шахте» (200 — 7+
	// минут: отсчёт начинался за 3 минуты вместо 10; рвать пачку посреди замены нельзя — разрыв ленты)
	fastBatch = 50
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

// launchFrom — с этой стадии ракета может улететь сама посреди дела: галочка автозапуска стоит
// со стадии 67, площадка встала — ракета улетела сразу (1098794301).
const launchFrom = 67

// launchWatch — как часто смотреть события мода: не улетела ли ракета.
const launchWatch = 2 * time.Second

// errLaunched — причина отмены дела стадии: ракета улетела.
var errLaunched = errors.New("ракета улетела")

// watchLaunch — ракета улетела (событие мода или счёт запусков) — отменить дело стадии (stop):
// больше ни одного нажатия — на экране победа (просьба пользователя: увидеть её; клик или Esc
// бота её закрыли бы).
func watchLaunch(ctx context.Context, h *act.Hands, stop context.CancelCauseFunc) {
	if ok, err := launched(h); err == nil && ok {
		h.Say("ракета уже улетела — ничего не нажимаю")
		stop(errLaunched)
		return
	}
	_, seq, err := h.B.PollEvents(1 << 30)
	if err != nil {
		h.Log("запуск ракеты не отслеживается: %v", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(launchWatch):
		}
		evs, next, err := h.B.PollEvents(seq)
		if err != nil {
			continue
		}
		seq = next
		for _, ev := range evs {
			if ev.Type == "rocket_launched" {
				h.Say("ракета улетела — больше ничего не нажимаю")
				stop(errLaunched)
				return
			}
		}
	}
}

// rocketParts — частей на ракету (прототип rocket-silo, 2.1: 100).
const rocketParts = 100

// countdownAt — до запуска столько — дела бросить, к шахте и отсчёт (stages.md, стадия 68).
const countdownAt = 10 * time.Minute

// launchClock — оценка времени до запуска по частям ракеты в шахте: прирост за последние 10 минут.
// Замеры — в фоне (runLaunch), читает — стадия.
type launchClock struct {
	mu      sync.Mutex
	samples []partSample
	built   bool // ракета собрана: шахта не строит (части при этом обнуляются — 100 не увидеть)
}

// sample — замер шахты s: части и состояние.
func (c *launchClock) sample(h *act.Hands, s layout.Placement) {
	st, err := h.B.GetEntity(s.Name, s.Pos)
	if err != nil || st == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.add(time.Now(), st.RocketParts)
	c.built = st.SiloStatus != "" && st.SiloStatus != "building_rocket"
}

// soon — до запуска не больше countdownAt (или ракета собрана).
func (c *launchClock) soon() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	eta, ok := c.eta()
	return c.built || ok && eta <= countdownAt
}

type partSample struct {
	at    time.Time
	parts int
}

// add — замер: частей в шахте parts в момент at (частей стало меньше — ракета ушла, замеры
// заново).
func (c *launchClock) add(at time.Time, parts int) {
	if n := len(c.samples); n > 0 && parts < c.samples[n-1].parts {
		c.samples = nil
	}
	c.samples = append(c.samples, partSample{at, parts})
	for len(c.samples) > 2 && at.Sub(c.samples[0].at) > 10*time.Minute {
		c.samples = c.samples[1:]
	}
}

// eta — сколько до запуска (ракета собрана — 0); false — не оценить: замеров меньше минуты или
// части не прибывают.
func (c *launchClock) eta() (time.Duration, bool) {
	n := len(c.samples)
	if n == 0 {
		return 0, false
	}
	first, last := c.samples[0], c.samples[n-1]
	if last.parts >= rocketParts {
		return 0, true
	}
	dt, dp := last.at.Sub(first.at), last.parts-first.parts
	if dt < time.Minute || dp <= 0 {
		return 0, false
	}
	return time.Duration(float64(rocketParts-last.parts) * float64(dt) / float64(dp)), true
}

// launchSoon — до запуска не больше countdownAt (runLaunch: дела — бросить); nil — не стадия 68.
var launchSoon func() bool

// silo — ракетная шахта плана.
func silo() (layout.Placement, bool) {
	plan, err := LoadPlan()
	if err != nil {
		return layout.Placement{}, false
	}
	for _, pl := range plan.Layout {
		if pl.Name == "rocket-silo" {
			return pl, true
		}
	}
	return layout.Placement{}, false
}

// inMinutes — «~N мин» (вверх) или «меньше минуты».
func inMinutes(d time.Duration) string {
	if d < time.Minute {
		return "меньше минуты"
	}
	return fmt.Sprintf("~%d мин", int((d+time.Minute-1)/time.Minute))
}

// countdown — до запуска ~10 минут (stages.md, стадия 68, просьба пользователя): сказать, встать
// вплотную к шахте (камера — над ней), раз в минуту — сколько осталось. Запуск — дело отменяет
// watchLaunch (после — ничего не нажимать).
func countdown(ctx context.Context, h *act.Hands, s layout.Placement, clock *launchClock) error {
	// left — что сказать: ракета собрана — "", иначе «~N мин» (не оценить — ok false)
	left := func() (string, bool, bool) {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		eta, ok := clock.eta()
		return inMinutes(eta), ok, clock.built
	}
	if txt, ok, built := left(); built {
		h.Say("ракета собрана — иду к ракетной шахте")
	} else if ok {
		h.Say("до запуска ракеты %s — иду к ракетной шахте", txt)
	}
	if e, err := h.B.EntityAt(s.Name, s.Pos); err == nil && e != nil {
		if err := h.WalkNear(ctx, *e, 1); err != nil {
			if ctx.Err() != nil {
				return err
			}
			h.Say("к ракетной шахте не дошёл: %v", err)
		}
	}
	said, ready := time.Time{}, false
	for {
		if ok, err := launched(h); err != nil || ok {
			return err
		}
		txt, ok, built := left()
		switch {
		case built && !ready:
			h.Say("ракета собрана — старт!")
			ready = true
		case !built && ok && time.Since(said) >= time.Minute:
			h.Say("до запуска ракеты %s", txt)
			said = time.Now()
		}
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func runLaunch(ctx context.Context, h *act.Hands) error {
	s, haveSilo := silo()
	clock := &launchClock{}
	if haveSilo { // замеры шахты — в фоне с начала стадии: отсчёт — вовремя, не между делами
		sctx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			for {
				clock.sample(h, s)
				select {
				case <-sctx.Done():
					return
				case <-time.After(20 * time.Second):
				}
			}
		}()
	}
	launchSoon = func() bool { return haveSilo && clock.soon() }
	defer func() { launchSoon = nil }()
	for _, step := range []func(context.Context, *act.Hands) error{fillModules, fastBelts} {
		if ok, err := launched(h); err != nil || ok {
			return err
		}
		if launchSoon() {
			return countdown(ctx, h, s, clock)
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
		if launchSoon() {
			return countdown(ctx, h, s, clock)
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
			if launchSoon != nil && launchSoon() { // до запуска ~10 минут — к шахте (runLaunch)
				return nil
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
