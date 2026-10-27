package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/world"
)

// Поиск места под базу квадратами (stages.md, стадии 1 и 2; решение пользователя 27.10):
// квадраты 2000×2000 — центральный вокруг прикинутого центра базы, потом кольцо из 8, потом
// из 16 (всего 25, 10000×10000). После каждого квадрата — быстрая прикидка «встанет ли база»
// (roughOnce с Quick и лимитом времени); стадия 1 на «встанет» заканчивается, стадия 2 после
// прикидки делает полную разметку, не встала — следующий квадрат. Пройдены все — места нет.

const (
	squareSide      = 2000 // сторона квадрата разведки, тайлов
	squareRings     = 2    // колец вокруг центрального квадрата
	squaresFile     = "state-squares.json"
	roughQuickLimit = 3 * time.Minute // прикидка: поиск мест этажам — не дольше
)

// roughQuick — roughPass в режиме прикидки (Base.Quick, Deadline).
var roughQuick bool

// squareState — что уже разведано: центр и пройденные квадраты (общее для стадий 1 и 2).
type squareState struct {
	Center world.Tile     `json:"center"`
	Done   []cluster.Rect `json:"done"`
}

func loadSquares() (*squareState, error) {
	data, err := os.ReadFile(runPath(squaresFile))
	if err != nil {
		return nil, err
	}
	var st squareState
	return &st, json.Unmarshal(data, &st)
}

// squareAt — квадрат со сдвигом (i, j) квадратов от центра.
func squareAt(center world.Tile, i, j int) cluster.Rect {
	x, y := center.X+i*squareSide-squareSide/2, center.Y+j*squareSide-squareSide/2
	return cluster.Rect{Min: world.Tile{X: x, Y: y}, Max: world.Tile{X: x + squareSide, Y: y + squareSide}}
}

// squaresTodo — непройденные квадраты по кольцам; в кольце — ближние к from первыми.
func squaresTodo(st *squareState, from bridge.Pos) []cluster.Rect {
	done := func(r cluster.Rect) bool {
		for _, d := range st.Done {
			if d == r {
				return true
			}
		}
		return false
	}
	var res []cluster.Rect
	for ring := 0; ring <= squareRings; ring++ {
		var rs []cluster.Rect
		for j := -ring; j <= ring; j++ {
			for i := -ring; i <= ring; i++ {
				if max(abs(i), abs(j)) != ring {
					continue
				}
				if r := squareAt(st.Center, i, j); !done(r) {
					rs = append(rs, r)
				}
			}
		}
		sort.Slice(rs, func(a, b int) bool {
			ax, ay := rs[a].Center()
			bx, by := rs[b].Center()
			return math.Hypot(ax-from.X, ay-from.Y) < math.Hypot(bx-from.X, by-from.Y)
		})
		res = append(res, rs...)
	}
	return res
}

// exploreSquare — открыть квадрат r целиком: сначала коридором от персонажа к его центру
// (разведка берёт только край открытого — дальний квадрат с ним не граничит), потом всё
// неоткрытое в нём, куда можно дойти.
func exploreSquare(ctx context.Context, h *act.Hands, r cluster.Rect) error {
	cx, cy := r.Center()
	in := func(c bridge.ChunkPos) bool {
		return c.X*32+32 > r.Min.X && c.X*32 < r.Max.X && c.Y*32+32 > r.Min.Y && c.Y*32 < r.Max.Y
	}
	more := func() bool {
		for c := range h.World.Chunks {
			for _, d := range []bridge.ChunkPos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
				u := bridge.ChunkPos{X: c.X + d.X, Y: c.Y + d.Y}
				if h.World.Chunks[u] == nil && in(u) {
					return true
				}
			}
		}
		return false
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
			q, err := h.B.Player()
			return err != nil || math.Hypot(q.Position.X-cx, q.Position.Y-cy) < 512
		}
		if err := h.ExploreWhere(ctx, corridor, arrived); err != nil && !errors.Is(err, act.ErrNothingToExplore) {
			return err
		}
	}
	if err := h.ExploreWhere(ctx, in, func() bool { return !more() }); err != nil && !errors.Is(err, act.ErrNothingToExplore) {
		return err
	}
	return nil
}

// roughEstimate — прикидка «встанет ли база» по открытой карте (быстрая разметка зон).
func roughEstimate(h *act.Hands) bool {
	roughQuick = true
	defer func() { roughQuick = false }()
	t0 := time.Now()
	err := roughOnce(h)
	if err != nil {
		h.Say("прикидка: база не встаёт (%v; %.0f с)", err, time.Since(t0).Seconds())
		return false
	}
	h.Say("прикидка: база встаёт (%.0f с)", time.Since(t0).Seconds())
	return true
}

// errNoSquares — все 25 квадратов пройдены, места под базу нет.
var errNoSquares = errors.New("места под базу нет ни в одном из квадратов разведки")

// searchBase — следующие квадраты, пока прикидка не скажет «встанет» (full — и полная разметка
// roughOnce не встанет); состояние — в файл после каждого квадрата. Все пройдены — errNoSquares.
func searchBase(ctx context.Context, h *act.Hands, st *squareState, full bool) error {
	for {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		todo := squaresTodo(st, p.Position)
		if len(todo) == 0 {
			return errNoSquares
		}
		r := todo[0]
		cx, cy := r.Center()
		h.Say("разведка: квадрат с центром (%.0f, %.0f) — %d из %d", cx, cy, len(st.Done)+1, (2*squareRings+1)*(2*squareRings+1))
		if err := exploreSquare(ctx, h, r); err != nil {
			return err
		}
		st.Done = append(st.Done, r)
		if err := writeState(squaresFile, st); err != nil {
			return err
		}
		saveWorld(h)
		if !roughEstimate(h) {
			continue
		}
		if !full {
			return nil
		}
		if err := roughOnce(h); err == nil {
			return nil
		} else {
			h.Say("полная разметка не встала там, где прикидка сказала «встанет» (%v) — дальше", err)
		}
	}
}

// squaresProgress — для журнала: сколько квадратов пройдено.
func squaresProgress(st *squareState) string {
	return fmt.Sprintf("%d из %d", len(st.Done), (2*squareRings+1)*(2*squareRings+1))
}
