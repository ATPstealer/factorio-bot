package stage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/world"
)

// Стадия 1 (stages.md): разведка места под базу. Круги квадратов 2000×2000 вокруг точки
// старта (squares.go), в каждом — все достижимые чанки; после круга — повтор недоступного.
// Прикидка «встанет ли база» — в фоне по копии карты, раз в roughEvery, ресурсы при этом не
// смотрятся. Встала — квадрат дооткрывается, прикидка заново, вокруг базы открывается
// 2000×2000, потом — крупные пятна угля, железа и меди как можно ближе к базе.

const (
	roughEvery  = 5 * time.Minute // прикидка в фоне — не чаще
	scoutGrow   = squareSide / 2  // доразведка ресурсов: рамка вокруг базы растёт на столько
	scoutGrowTo = squareSide      // и не дальше (от квадрата вокруг базы)
)

// scoutRes — ресурсы ядра базы.
var scoutRes = []string{"coal", "iron-ore", "copper-ore"}

func init() {
	register(Stage{N: 1, Name: "разведка места под базу", Run: runExplore, Check: checkExplore})
}

// roughNeed — сколько угля, железной и медной руды нужно за игру (как стадия 2).
func roughNeed(h *act.Hands) (map[string]float64, error) {
	targets, _, err := roughTargets(h)
	if err != nil {
		return nil, err
	}
	plan, err := calc.Solve(h.Protos, targets, calc.Options{All: true,
		Machines: map[string]string{"smelting": "steel-furnace", "crafting": "assembling-machine-2"}})
	if err != nil {
		return nil, err
	}
	need := map[string]float64{}
	for it, rate := range plan.Raw {
		if it != "water" {
			need[it] = rate * roughHours * 3600
		}
	}
	return need, nil
}

// scoutFound — крупные (не меньше трети нужного) пятна всех трёх ресурсов открыты.
func scoutFound(h *act.Hands, need map[string]float64) bool {
	have := map[string]bool{}
	for _, p := range h.World.Patches() {
		if float64(p.Amount) >= need[p.Name]/3 {
			have[p.Name] = true
		}
	}
	for _, r := range scoutRes {
		if !have[r] {
			return false
		}
	}
	return true
}

// estimator — прикидка «встанет ли база» в фоне (stages.md, стадия 1): по копии карты, одна
// за раз, не чаще roughEvery; tick — между походами разведки: забрать результат, запустить
// следующую. Прикидка пишет state-zones.json и картинку зон — стадия 2 их перепишет.
type estimator struct {
	h     *act.Hands
	last  time.Time
	res   chan estResult // идёт прикидка — её результат
	found *world.Tile    // прикидка сказала «встанет»: центр базы
}

type estResult struct {
	center world.Tile
	ok     bool
}

func newEstimator(h *act.Hands) *estimator {
	return &estimator{h: h, last: time.Now()} // первая — через roughEvery: на пустой карте нечего смотреть
}

func (e *estimator) tick() {
	if e.res != nil {
		select {
		case r := <-e.res:
			e.take(r)
		default:
			return
		}
	}
	if e.found != nil || time.Since(e.last) < roughEvery {
		return
	}
	w, err := e.h.World.Clone()
	e.last = time.Now()
	if err != nil {
		e.h.Log("прикидка в фоне: копия карты: %v", err)
		return
	}
	hc := *e.h
	hc.World, hc.Console, hc.B = w, false, nil // только журнал: RCON из двух потоков нельзя
	e.res = make(chan estResult, 1)
	e.h.Log("прикидка в фоне: %d чанков", len(w.Chunks))
	go func(h *act.Hands, res chan estResult) {
		c, ok := roughEstimate(h)
		res <- estResult{c, ok}
	}(&hc, e.res)
}

func (e *estimator) take(r estResult) {
	e.res = nil
	if r.ok {
		e.found = &r.center
	}
}

// wait — дождаться идущей прикидки (перед прикидкой в основном потоке и на выходе).
func (e *estimator) wait() {
	if e.res != nil {
		e.take(<-e.res)
	}
}

func runExplore(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	st, err := loadSquares()
	if err != nil {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		st = &squareState{Center: world.TileOf(p.Position)}
		h.Say("разведка: центр кругов — точка старта (%d, %d)", st.Center.X, st.Center.Y)
	}
	if st.Base == nil {
		if err := exploreCircles(ctx, h, st); err != nil {
			return err
		}
	}
	return exploreAroundBase(ctx, h, st)
}

// exploreCircles — круги квадратов, пока прикидка не скажет «встанет» (st.Base); все 25
// пройдены — errNoSquares.
func exploreCircles(ctx context.Context, h *act.Hands, st *squareState) error {
	h.ExploreOnce = true
	defer func() { h.ExploreOnce = false }()
	roughOrigin = &st.Center
	defer func() { roughOrigin = nil }()
	est := newEstimator(h)
	defer est.wait()
	// settle — конец квадрата или круга: прикидка в фоне сказала «встанет» — прикидка заново по
	// всему открытому в основном потоке (база могла встать лучше); нет — дальше
	settle := func() (bool, error) {
		est.wait()
		if est.found == nil {
			return false, nil
		}
		h.Say("разведка: прикидка в фоне нашла место у (%d, %d) — проверяю по всему открытому", est.found.X, est.found.Y)
		c, ok := roughEstimate(h)
		if !ok {
			est.found = nil
			return false, nil
		}
		st.Base = &c
		found := squareAt(c, 0, 0)
		st.Found = &found
		return true, writeState(squaresFile, st)
	}
	for ring := 0; ring <= squareRings; ring++ {
		for _, r := range ringSquares(st.Center, ring) {
			if st.done(r) {
				continue
			}
			cx, cy := r.Center()
			h.Say("разведка: круг %d, квадрат с центром (%.0f, %.0f) — %d из %d", ring+1, cx, cy, len(st.Done)+1, squaresTotal)
			if err := exploreSquare(ctx, h, r, est.tick); err != nil {
				return err
			}
			st.Done = append(st.Done, r)
			if err := writeState(squaresFile, st); err != nil {
				return err
			}
			saveWorld(h)
			if ok, err := settle(); ok || err != nil {
				return err
			}
		}
		// после круга — повтор недоступного (stages.md): открылся обход — чанки у края
		// достижимого открываются, от ближайшего к персонажу
		h.Say("разведка: круг %d пройден — проверяю, не открылись ли проходы к недоступному", ring+1)
		in := inAny(st.Done)
		err := h.ExploreWhere(ctx, in, func() bool { est.tick(); return !moreIn(h.World, in) })
		if err != nil && !errors.Is(err, act.ErrNothingToExplore) {
			return err
		}
		saveWorld(h)
		if ok, err := settle(); ok || err != nil {
			return err
		}
	}
	return errNoSquares
}

// exploreAroundBase — база встала: открыть 2000×2000 вокруг её центра (все достижимые чанки),
// потом крупные пятна угля, железа и меди как можно ближе к базе — нет на открытом — рамка
// вокруг базы растёт на scoutGrow до scoutGrowTo (stages.md).
func exploreAroundBase(ctx context.Context, h *act.Hands, st *squareState) error {
	h.ExploreOnce = true
	defer func() { h.ExploreOnce = false }()
	h.Say("разведка: база встаёт у (%d, %d) — открываю 2000×2000 вокруг неё", st.Base.X, st.Base.Y)
	if err := exploreSquare(ctx, h, *st.Found, nil); err != nil {
		return err
	}
	saveWorld(h)
	need, err := roughNeed(h)
	if err != nil {
		return err
	}
	for grow := scoutGrow; !scoutFound(h, need); grow += scoutGrow {
		if grow > scoutGrowTo {
			return fmt.Errorf("крупные пятна угля, железа и меди (не меньше трети нужного на игру) не найдены в %d тайлах от базы", scoutGrowTo+squareSide/2)
		}
		h.Say("разведка: крупных пятен угля, железа и меди у базы нет — доразведка до %d тайлов от неё", grow+squareSide/2)
		in := inRect(st.Found.Grow(grow))
		err := h.ExploreWhere(ctx, in, func() bool { return scoutFound(h, need) || !moreIn(h.World, in) })
		if err != nil && !errors.Is(err, act.ErrNothingToExplore) {
			return err
		}
		saveWorld(h)
	}
	h.Say("разведка: место под базу у (%d, %d) и ресурсы найдены", st.Base.X, st.Base.Y)
	return nil
}

func checkExplore(h *act.Hands) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	st, err := loadSquares()
	if err != nil || st.Base == nil {
		return fmt.Errorf("место под базу не найдено")
	}
	need, err := roughNeed(h)
	if err != nil {
		return err
	}
	if !scoutFound(h, need) {
		return fmt.Errorf("крупные пятна угля, железа и меди (не меньше трети нужного на игру) не найдены")
	}
	return nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
