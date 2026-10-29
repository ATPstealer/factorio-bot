package act

import (
	"context"
	"errors"
	"fmt"
	"math"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
)

// exploreHop — длина перехода (путь строится только по открытой карте).
const exploreHop = 48

// exploreRounds — кругов по пропущенным чанкам, когда остались только они (CLAUDE.md, разведка).
const exploreRounds = 3

// ErrNothingToExplore — неоткрытого, куда можно дойти, рядом нет (в пределах 64 чанков или
// области ExploreWhere; недоступное — вода, утёсы — пропущено).
var ErrNothingToExplore = errors.New("act: разведка: неоткрытого, куда дойти, нет")

// Explore — разведка: идти к ближайшему неоткрытому чанку на краю открытой карты
// (World.NearestUnknown), пока done не вернёт true. Дошёл, а чанк не открылся, или не пройти
// (вода, утёсы) — этот чанк пропускается. Неоткрытого рядом нет — ошибка.
func (h *Hands) Explore(ctx context.Context, done func() bool) error {
	return h.ExploreWhere(ctx, nil, done)
}

// ExploreWhere — Explore только к чанкам, для которых inside (nil — любые): обойти область.
func (h *Hands) ExploreWhere(ctx context.Context, inside func(bridge.ChunkPos) bool, done func() bool) error {
	if h.World == nil {
		return fmt.Errorf("act: разведка без модели карты")
	}
	w := h.World
	skip := map[bridge.ChunkPos]bool{}
	tries := map[bridge.ChunkPos]int{} // походов к чанку без новых открытых
	start := len(w.Chunks)
	rounds, mark := 0, -1 // кругов по пропущенным; чанков в начале круга
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := w.Sync(h.B, false); err != nil {
			return err
		}
		if done() {
			h.Log("разведка: открыто чанков %d → %d", start, len(w.Chunks))
			return nil
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		// только чанки, до которых можно дойти (за водой и утёсами — нет: бот стоял, перебирая их)
		front := w.FrontierFrom(p.Position)
		target, ok := w.NearestUnknownWhere(p.Position, 64, skip, func(c bridge.ChunkPos) bool {
			return front[c] && (inside == nil || inside(c))
		})
		if !ok {
			// остались пропущенные — ещё круг по ним (не дошёл раз — бывает достижимо со второй
			// попытки: 17555262, край квадрата, где встала база), пока круг открывает новое
			if len(skip) > 0 && rounds < exploreRounds && len(w.Chunks) != mark {
				rounds, mark = rounds+1, len(w.Chunks)
				h.Log("разведка: ещё круг по %d пропущенным чанкам (%d из %d)", len(skip), rounds, exploreRounds)
				skip, tries = map[bridge.ChunkPos]bool{}, map[bridge.ChunkPos]int{}
				continue
			}
			return ErrNothingToExplore
		}
		tc := world.ChunkOf(world.TileOf(target))
		h.Say("разведка: к неоткрытому чанку (%.0f, %.0f)", target.X, target.Y)
		// шаг к цели по открытой земле
		dist := func(a bridge.Pos) float64 { return math.Hypot(a.X-p.Position.X, a.Y-p.Position.Y) }
		d := dist(target)
		var spot bridge.Pos
		found := false
		for _, hop := range []float64{exploreHop, 32, 16, 8} {
			k := math.Min(1, hop/d)
			pt := bridge.Pos{X: p.Position.X + (target.X-p.Position.X)*k, Y: p.Position.Y + (target.Y-p.Position.Y)*k}
			if spot, found = w.FreeSpot(pt, 1, 1, 6, nil); found {
				break
			}
		}
		// по прямой — вода (озеро между игроком и целью): шага нет или он упёрся в берег у ног
		// (565937761: дыры посреди базы за озером — три «хода» по 0.2 с и «недоступно») — к
		// открытому соседу цели маршрутом в обход (чанк — на краю достижимого: FrontierFrom), а
		// не «недоступно» — верх открытого участка бросался за секунды, 689494172
		if !found || dist(spot) < 4 {
			found = false
			for _, s := range []bridge.ChunkPos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
				n := bridge.ChunkPos{X: tc.X + s.X, Y: tc.Y + s.Y}
				if w.Chunks[n] == nil {
					continue
				}
				c := bridge.Pos{X: float64(n.X*world.ChunkSize + world.ChunkSize/2), Y: float64(n.Y*world.ChunkSize + world.ChunkSize/2)}
				if spot, found = w.FreeSpot(c, 1, 1, 16, nil); found {
					break
				}
			}
		}
		before := len(w.Chunks)
		moved, near := false, false
		if found {
			if err := h.WalkTo(ctx, spot, 3); err != nil && !errors.Is(err, ErrNotReached) {
				if ctx.Err() != nil {
					return err
				}
				h.Log("разведка: %v", err)
			}
			if q, err := h.B.Player(); err == nil {
				moved = dist(q.Position) >= 4
				near = math.Hypot(q.Position.X-target.X, q.Position.Y-target.Y) < exploreHop
			}
		}
		if _, err := w.Sync(h.B, false); err != nil {
			return err
		}
		// не пройти или подошли, а карта не прибавилась — к этому чанку больше не идти; ходим к
		// нему раз за разом без новых чанков (за водой, утёсами) — тоже
		if len(w.Chunks) == before {
			tries[tc]++
		}
		if !moved || len(w.Chunks) == before && (near || tries[tc] >= 3) {
			skip[tc] = true
		}
	}
}
