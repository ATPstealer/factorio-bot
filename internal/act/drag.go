package act

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
)

// Протяжка лент: зажатая ЛКМ с лентой в руке, ведомая по тайлам ряда, ставит ленту на каждый
// тайл одним движением (игра так строит сама; ввод честный — мышь). Одиночные клики с
// подтверждением стоили ~0.5 с на ленту: стадия 30 на 267145981 — 14 тыс. лент, ~2 ч кликов.

// dragMin — ряд короче протяжкой не ставится (клики).
const dragMin = 3

// dragRuns — среди индексов here ряды лент одного направления, соседних по ходу: кандидаты
// на протяжку, каждый — по ходу ленты.
func dragRuns(positions []bridge.Pos, dirs []int, here []int) [][]int {
	along := func(i int) float64 {
		switch dirs[i] {
		case North:
			return -positions[i].Y
		case South:
			return positions[i].Y
		case East:
			return positions[i].X
		}
		return -positions[i].X
	}
	type key struct{ dir, line int }
	groups := map[key][]int{}
	var keys []key
	for _, i := range here {
		k := key{dirs[i], int(math.Floor(positions[i].Y))}
		if dirs[i]%8 == 0 {
			k.line = int(math.Floor(positions[i].X))
		}
		if groups[k] == nil {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], i)
	}
	sort.Slice(keys, func(a, b int) bool {
		return keys[a].dir < keys[b].dir || keys[a].dir == keys[b].dir && keys[a].line < keys[b].line
	})
	var runs [][]int
	for _, k := range keys {
		g := groups[k]
		sort.Slice(g, func(a, b int) bool { return along(g[a]) < along(g[b]) })
		run := []int{g[0]}
		for j := 1; j < len(g); j++ {
			if along(g[j])-along(g[j-1]) == 1 {
				run = append(run, g[j])
				continue
			}
			if len(run) >= dragMin {
				runs = append(runs, run)
			}
			run = []int{g[j]}
		}
		if len(run) >= dragMin {
			runs = append(runs, run)
		}
	}
	return runs
}

// dragBuild — протяжка: лента entity уже в руке и повёрнута по ходу ряда pos (соседние тайлы
// по порядку). Возвращает вставшие (пустая запись — не встало: вызывающий ставит кликом).
func (h *Hands) dragBuild(ctx context.Context, entity string, pos []bridge.Pos) ([]bridge.Entity, error) {
	res := make([]bridge.Entity, len(pos))
	_, seq0, err := h.B.PollEvents(1 << 30)
	if err != nil {
		return res, err
	}
	p, err := h.B.Player()
	if err != nil {
		return res, err
	}
	if h.World != nil && h.World.OnBelt(p.Position) {
		if err := h.stepOffBelt(ctx, p.Position); err != nil {
			return res, err
		}
		if err := h.waitStill(ctx); err != nil {
			return res, err
		}
		if p, err = h.B.Player(); err != nil {
			return res, err
		}
	}
	if item := h.Protos.ItemFor(entity); item != "" && cursorName(p) != item {
		return res, fmt.Errorf("%w %s: в руке не лента", ErrPlace, entity)
	}
	for _, q := range pos {
		if !h.clickable(p, p.Position, q) {
			return res, nil // весь ряд — кликами, с подходом
		}
	}
	cam := h.Camera(p)
	if err := h.In.Move(cam.ToScreen(pos[0].X, pos[0].Y)); err != nil {
		return res, err
	}
	if err := sleep(ctx, 40*time.Millisecond); err != nil {
		return res, err
	}
	if err := h.In.ButtonDown(input.Left); err != nil {
		return res, err
	}
	// первый и последний тайл — с паузой: игра ставит ленту на тайл, когда курсор на нём
	// успел побыть тик (протяжка ×7 ставила 6 — стадия 30 на 427965634)
	if err := sleep(ctx, 60*time.Millisecond); err != nil {
		h.In.ButtonUp(input.Left)
		return res, err
	}
	for _, q := range pos[1:] {
		if err := h.In.Move(cam.ToScreen(q.X, q.Y)); err != nil {
			h.In.ButtonUp(input.Left)
			return res, err
		}
		if err := sleep(ctx, 30*time.Millisecond); err != nil {
			h.In.ButtonUp(input.Left)
			return res, err
		}
	}
	if err := sleep(ctx, 60*time.Millisecond); err != nil {
		h.In.ButtonUp(input.Left)
		return res, err
	}
	if err := h.In.ButtonUp(input.Left); err != nil {
		return res, err
	}
	// подтверждение — события постановки по тайлам ряда
	deadline := time.Now().Add(1500 * time.Millisecond)
	got := 0
	for got < len(pos) && time.Now().Before(deadline) {
		if err := sleep(ctx, pollEvery); err != nil {
			return res, err
		}
		evs, _, err := h.B.PollEvents(seq0)
		if err != nil {
			return res, err
		}
		got = 0
		for k := range res {
			res[k] = bridge.Entity{}
		}
		for _, e := range evs {
			if e.Type != "built_entity" || e.Entity == nil || e.Entity.Name != entity {
				continue
			}
			for k, q := range pos {
				if math.Abs(e.Entity.Position.X-q.X) <= 0.5 && math.Abs(e.Entity.Position.Y-q.Y) <= 0.5 {
					res[k] = *e.Entity
				}
			}
		}
		for k := range res {
			if res[k].Name != "" {
				got++
			}
		}
	}
	if got < len(pos) {
		var miss []string
		for k, q := range pos {
			if res[k].Name == "" {
				miss = append(miss, fmt.Sprintf("(%.1f, %.1f)", q.X, q.Y))
			}
		}
		h.Log("протяжка %s ×%d от (%.1f, %.1f): встало %d, не встало %s — кликами", entity, len(pos), pos[0].X, pos[0].Y, got, strings.Join(miss, " "))
	}
	return res, nil
}
