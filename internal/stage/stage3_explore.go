package stage

import (
	"context"
	"errors"
	"fmt"
	"math"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/world"
)

// Стадия 3 (stages.md): разведка места под базу. Бот прикидывает центр базы (как стадия 4 —
// где ближе всего друг к другу крупные пятна угля, железа и меди) и идёт открывать: нет ещё
// всех трёх — к краю открытого, где видна руда или ровная суша; есть — пятна у центра,
// уходящие за край, и рамку под базу вокруг центра. Центр пересчитывается после каждого похода.

const (
	scoutBox   = 32 // чанков от центра базы: рамка 2048×2048 тайлов (1000×1000 часто не хватало базе)
	scoutHop   = 24 // новых чанков за один поход к краю (пока крупных пятен нет)
	scoutTrips = 60 // походов не больше
)

// scoutRes — ресурсы ядра базы.
var scoutRes = []string{"coal", "iron-ore", "copper-ore"}

func init() {
	register(Stage{N: 3, Name: "разведка места под базу", Run: runExplore, Check: checkExplore})
}

// roughNeed — сколько угля, железной и медной руды нужно за игру (как стадия 4).
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

// scoutEdge — неоткрытый чанк c у края открытого (сосед открыт): очки — руда ядра в открытых
// соседях (пятно уходит за край) и ровная суша, минус даль от игрока.
func scoutEdge(h *act.Hands, from bridge.Pos) (bridge.ChunkPos, bool) {
	w := h.World
	best, bs := bridge.ChunkPos{}, math.Inf(-1)
	seen := map[bridge.ChunkPos]bool{}
	for c := range w.Chunks {
		for _, d := range []bridge.ChunkPos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
			u := bridge.ChunkPos{X: c.X + d.X, Y: c.Y + d.Y}
			if w.Chunks[u] != nil || seen[u] {
				continue
			}
			seen[u] = true
			ore, land := 0, 0
			for _, n := range []bridge.ChunkPos{{X: u.X + 1, Y: u.Y}, {X: u.X - 1, Y: u.Y}, {X: u.X, Y: u.Y + 1}, {X: u.X, Y: u.Y - 1}} {
				if w.Chunks[n] == nil {
					continue
				}
				for y := 0; y < 32; y++ {
					for x := 0; x < 32; x++ {
						t := world.Tile{X: n.X*32 + x, Y: n.Y*32 + y}
						if r, ok := w.ResourceAt(t); ok && (r.Name == "coal" || r.Name == "iron-ore" || r.Name == "copper-ore") {
							ore++
						}
						if w.TileAt(t) == world.Land {
							land++
						}
					}
				}
			}
			cx, cy := float64(u.X*32+16), float64(u.Y*32+16)
			s := float64(ore)/4 + float64(land)/64 - math.Hypot(cx-from.X, cy-from.Y)/32
			if s > bs {
				best, bs = u, s
			}
		}
	}
	return best, !math.IsInf(bs, -1)
}

// scoutBoxAround — чанк в рамке scoutBox вокруг центра или у крупного пятна ядра, которое
// уходит за край (на 2 чанка вокруг рамки пятна).
func scoutBoxAround(h *act.Hands, center world.Tile, need map[string]float64) func(bridge.ChunkPos) bool {
	c0 := world.ChunkOf(center)
	var near [][4]int
	for _, p := range h.World.Patches() {
		if float64(p.Amount) < need[p.Name]/3 {
			continue
		}
		a, b := world.ChunkOf(p.Min), world.ChunkOf(p.Max)
		if abs(a.X-c0.X) > scoutBox+4 || abs(a.Y-c0.Y) > scoutBox+4 {
			continue
		}
		near = append(near, [4]int{a.X - 2, a.Y - 2, b.X + 2, b.Y + 2})
	}
	return func(c bridge.ChunkPos) bool {
		if abs(c.X-c0.X) <= scoutBox && abs(c.Y-c0.Y) <= scoutBox {
			return true
		}
		for _, r := range near {
			if c.X >= r[0] && c.X <= r[2] && c.Y >= r[1] && c.Y <= r[3] {
				return true
			}
		}
		return false
	}
}

func runExplore(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	need, err := roughNeed(h)
	if err != nil {
		return err
	}
	var last world.Tile
	for trip := 0; trip < scoutTrips; trip++ {
		if _, err := h.World.Sync(h.B, false); err != nil {
			return err
		}
		if !scoutFound(h, need) {
			// крупных пятен всех трёх нет — к лучшему краю: руда за краем, ровная суша
			p, err := h.B.Player()
			if err != nil {
				return err
			}
			goal, ok := scoutEdge(h, p.Position)
			if !ok {
				return fmt.Errorf("разведка: края открытой карты нет")
			}
			h.Say("разведка: крупных пятен угля, железа и меди ещё нет — к краю (%d, %d)", goal.X*32+16, goal.Y*32+16)
			n0 := len(h.World.Chunks)
			in := func(c bridge.ChunkPos) bool { return abs(c.X-goal.X) <= 3 && abs(c.Y-goal.Y) <= 3 }
			err = h.ExploreWhere(ctx, in, func() bool { return len(h.World.Chunks) >= n0+scoutHop })
			if err != nil && !errors.Is(err, act.ErrNothingToExplore) {
				return err
			}
			continue
		}
		center := roughAnchor(h, need)
		if trip > 0 && math.Hypot(float64(center.X-last.X), float64(center.Y-last.Y)) < 64 && !scoutMore(h, center, need) {
			h.Say("разведка: место под базу у (%d, %d) открыто", center.X, center.Y)
			return nil
		}
		last = center
		h.Say("разведка: центр базы у (%d, %d) — открываю место вокруг и пятна за краем", center.X, center.Y)
		in := scoutBoxAround(h, center, need)
		err := h.ExploreWhere(ctx, in, func() bool { return !scoutMore(h, center, need) })
		if errors.Is(err, act.ErrNothingToExplore) {
			// в рамке дойти больше некуда (за водой, утёсами) — место открыто, сколько можно
			// (иначе те же недоступные чанки — по кругу до лимита стадии: 238178543)
			h.Say("разведка: место под базу у (%d, %d) открыто (остальное недоступно)", center.X, center.Y)
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// scoutMore — у центра есть неоткрытое, куда можно дойти (сосед открыт).
func scoutMore(h *act.Hands, center world.Tile, need map[string]float64) bool {
	in := scoutBoxAround(h, center, need)
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

func checkExplore(h *act.Hands) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
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
