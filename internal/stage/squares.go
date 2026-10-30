package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/world"
)

// Поиск места под базу квадратами (stages.md, стадии 1 и 2): квадраты 2000×2000 вокруг точки
// старта — центральный (круг 1), кольцо из 8 (круг 2: с северного по часовой стрелке), кольцо
// из 16 (круг 3; всего 25, 10000×10000). Прикидка «встанет ли база» — только место этажам, без
// зон добычи (roughEstimate: roughPass с Quick, лимитом времени и baseFirst); стадия 1 считает
// её в фоне, стадия 2 после полной разметки, которая не встала, — по квадратам дальше
// (searchBase). Пройдены все — места нет.

const (
	squareSide      = 2000 // сторона квадрата разведки, тайлов
	squareRings     = 2    // колец вокруг центрального квадрата
	squaresFile     = "state-squares.json"
	roughQuickLimit = 3 * time.Minute // прикидка: один проход, поиск мест этажам — не дольше
)

// roughQuick — roughPass в режиме прикидки (Base.Quick, Deadline).
var roughQuick bool

// squareState — что уже разведано (общее для стадий 1 и 2): центр кругов (точка старта),
// пройденные квадраты; Base — центр базы по прикидке, Found — квадрат вокруг него (стадия 2
// размечает от него).
type squareState struct {
	Center world.Tile     `json:"center"`
	Done   []cluster.Rect `json:"done"`
	Found  *cluster.Rect  `json:"found,omitempty"`
	Base   *world.Tile    `json:"base,omitempty"`
}

func loadSquares() (*squareState, error) {
	data, err := os.ReadFile(runPath(squaresFile))
	if err != nil {
		return nil, err
	}
	var st squareState
	return &st, json.Unmarshal(data, &st)
}

func (st *squareState) done(r cluster.Rect) bool {
	for _, d := range st.Done {
		if d == r {
			return true
		}
	}
	return false
}

// squareAt — квадрат со сдвигом (i, j) квадратов от центра.
func squareAt(center world.Tile, i, j int) cluster.Rect {
	x, y := center.X+i*squareSide-squareSide/2, center.Y+j*squareSide-squareSide/2
	return cluster.Rect{Min: world.Tile{X: x, Y: y}, Max: world.Tile{X: x + squareSide, Y: y + squareSide}}
}

// ringSquares — квадраты кольца ring вокруг центра: с северного, дальше по часовой стрелке
// (stages.md, стадия 1); ring 0 — центральный.
func ringSquares(center world.Tile, ring int) []cluster.Rect {
	if ring == 0 {
		return []cluster.Rect{squareAt(center, 0, 0)}
	}
	var rs []cluster.Rect
	add := func(i, j int) { rs = append(rs, squareAt(center, i, j)) }
	for i := 0; i < ring; i++ { // верх: от севера на восток
		add(i, -ring)
	}
	for j := -ring; j < ring; j++ { // правый столбец вниз
		add(ring, j)
	}
	for i := ring; i > -ring; i-- { // низ на запад
		add(i, ring)
	}
	for j := ring; j > -ring; j-- { // левый столбец вверх
		add(-ring, j)
	}
	for i := -ring; i < 0; i++ { // верх до севера
		add(i, -ring)
	}
	return rs
}

// squaresTotal — квадратов всего (25).
const squaresTotal = (2*squareRings + 1) * (2*squareRings + 1)

// squaresTodo — непройденные квадраты по кольцам, в кольце — по часовой стрелке с северного.
func squaresTodo(st *squareState) []cluster.Rect {
	var res []cluster.Rect
	for ring := 0; ring <= squareRings; ring++ {
		for _, r := range ringSquares(st.Center, ring) {
			if !st.done(r) {
				res = append(res, r)
			}
		}
	}
	return res
}

// inRect — чанк задевает прямоугольник r.
func inRect(r cluster.Rect) func(bridge.ChunkPos) bool {
	return func(c bridge.ChunkPos) bool {
		return c.X*32+32 > r.Min.X && c.X*32 < r.Max.X && c.Y*32+32 > r.Min.Y && c.Y*32 < r.Max.Y
	}
}

// inAny — чанк задевает хоть один из прямоугольников.
func inAny(rs []cluster.Rect) func(bridge.ChunkPos) bool {
	var fs []func(bridge.ChunkPos) bool
	for _, r := range rs {
		fs = append(fs, inRect(r))
	}
	return func(c bridge.ChunkPos) bool {
		for _, f := range fs {
			if f(c) {
				return true
			}
		}
		return false
	}
}

// moreIn — есть неоткрытые чанки в области in рядом с открытыми (куда в принципе идти).
func moreIn(w *world.World, in func(bridge.ChunkPos) bool) bool {
	for c := range w.Chunks {
		for _, d := range []bridge.ChunkPos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
			u := bridge.ChunkPos{X: c.X + d.X, Y: c.Y + d.Y}
			if w.Chunks[u] == nil && in(u) {
				return true
			}
		}
	}
	return false
}

// exploreSquare — открыть квадрат r: сначала коридором от персонажа к его центру (разведка
// берёт только край открытого — дальний квадрат с ним не граничит), потом всё неоткрытое в
// нём, куда можно дойти (от ближайшего к персонажу). tick — между походами (прикидка в фоне).
func exploreSquare(ctx context.Context, h *act.Hands, r cluster.Rect, tick func()) error {
	cx, cy := r.Center()
	in := inRect(r)
	if tick == nil {
		tick = func() {}
	}
	if p, err := h.B.Player(); err == nil && math.Hypot(p.Position.X-cx, p.Position.Y-cy) >= 512 {
		ax, ay := p.Position.X, p.Position.Y
		corridor := func(c bridge.ChunkPos) bool { // чанки у отрезка персонаж — центр квадрата
			px, py := float64(c.X*32+16), float64(c.Y*32+16)
			dx, dy := cx-ax, cy-ay
			l2 := dx*dx + dy*dy
			t := 0.0
			if l2 > 0 {
				t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/l2))
			}
			return math.Hypot(px-ax-t*dx, py-ay-t*dy) <= 96
		}
		// дошёл — персонаж в квадрате (не «центральный чанк открыт»: он бывает сгенерирован
		// заранее, отдельно от открытого — коридор кончался сразу, 264771462, 208708719)
		arrived := func() bool {
			tick()
			q, err := h.B.Player()
			return err != nil || math.Hypot(q.Position.X-cx, q.Position.Y-cy) < 512
		}
		if err := h.ExploreWhere(ctx, corridor, arrived); err != nil && !errors.Is(err, act.ErrNothingToExplore) {
			return err
		}
	}
	done := func() bool { tick(); return !moreIn(h.World, in) }
	if err := h.ExploreWhere(ctx, in, done); err != nil && !errors.Is(err, act.ErrNothingToExplore) {
		return err
	}
	return nil
}

// roughEstimate — прикидка «встанет ли база» по открытой карте (stages.md, стадия 1): один
// проход разметки без зон добычи (baseFirst), быстро, с лимитом времени. Встала — центр базы
// (середина рамки её зон без добычи).
func roughEstimate(h *act.Hands) (world.Tile, bool) {
	roughQuick, baseFirst = true, true
	defer func() { roughQuick, baseFirst = false, false }()
	t0 := time.Now()
	if _, err := roughPass(h, nil, nil); err != nil {
		h.Say("прикидка: база не встаёт (%v; %.0f с)", err, time.Since(t0).Seconds())
		return world.Tile{}, false
	}
	rp, err := LoadRough()
	if err != nil {
		h.Say("прикидка: %v", err)
		return world.Tile{}, false
	}
	var box *cluster.Rect
	for _, z := range rp.Zones {
		if z.Kind == cluster.ZoneMine {
			continue
		}
		r := z.Rect
		if box == nil {
			box = &r
			continue
		}
		box.Min.X, box.Min.Y = min(box.Min.X, r.Min.X), min(box.Min.Y, r.Min.Y)
		box.Max.X, box.Max.Y = max(box.Max.X, r.Max.X), max(box.Max.Y, r.Max.Y)
	}
	if box == nil {
		h.Say("прикидка: зон базы нет")
		return world.Tile{}, false
	}
	x, y := box.Center()
	c := world.Tile{X: int(x), Y: int(y)}
	h.Say("прикидка: база встаёт, центр (%d, %d) (%.0f с)", c.X, c.Y, time.Since(t0).Seconds())
	return c, true
}

// errNoSquares — все 25 квадратов пройдены, места под базу нет.
var errNoSquares = errors.New("места под базу нет ни в одном из квадратов разведки")

// searchBase — стадия 2, полная разметка не встала: следующие квадраты по кольцам, после
// каждого — прикидка с центром в нём и полная разметка; состояние — в файл после каждого
// квадрата. Все пройдены — errNoSquares.
func searchBase(ctx context.Context, h *act.Hands, st *squareState) error {
	for {
		todo := squaresTodo(st)
		if len(todo) == 0 {
			return errNoSquares
		}
		r := todo[0]
		cx, cy := r.Center()
		h.Say("разведка: квадрат с центром (%.0f, %.0f) — %d из %d", cx, cy, len(st.Done)+1, squaresTotal)
		if err := exploreSquare(ctx, h, r, nil); err != nil {
			return err
		}
		st.Done = append(st.Done, r)
		if err := writeState(squaresFile, st); err != nil {
			return err
		}
		saveWorld(h)
		// центр базы — в этом квадрате (stages.md): от общего центра поиск мест за предел
		// прикидки до квадрата в 2000 тайлах не доходит (17555262)
		roughRegion = &r
		if _, ok := roughEstimate(h); !ok {
			roughRegion = nil
			continue
		}
		if err := roughOnce(h); err != nil {
			h.Say("полная разметка не встала там, где прикидка сказала «встанет» (%v) — дальше", err)
			roughRegion = nil
			continue
		}
		st.Found = &r
		return writeState(squaresFile, st)
	}
}

// squaresProgress — для журнала: сколько квадратов пройдено.
func squaresProgress(st *squareState) string {
	return fmt.Sprintf("%d из %d", len(st.Done), squaresTotal)
}
