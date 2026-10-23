package stage

import (
	"context"
	"errors"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// Стадия 3 (stages.md): разведка — открыть квадрат 1536×1536 тайлов с центром в месте старта
// (0, 0): персонаж обходит его, игра открывает чанки вокруг; недоступное (вода, утёсы)
// пропускается, тогда разведка шире — пока открыто меньше 1500×1500 тайлов (чтобы влезли
// зоны стадии 4). Дальше план базы — по открытой карте.

const (
	exploreHalf    = 24   // чанков от центра: квадрат 48×48 чанков = 1536×1536 тайлов
	exploreMaxHalf = 32   // не дойти до части квадрата — разведка шире, до 64×64 чанков
	exploreNeed    = 2198 // открыть не меньше площади 1500×1500 тайлов (2198 чанков)
)

func init() {
	register(Stage{N: 3, Name: "разведка", Run: runExplore, Check: checkExplore})
}

// exploreIn — чанк в квадрате half чанков от центра.
func exploreIn(half int) func(bridge.ChunkPos) bool {
	return func(c bridge.ChunkPos) bool { return c.X >= -half && c.X < half && c.Y >= -half && c.Y < half }
}

// exploreCharted — открытых чанков в квадрате half и всего в нём.
func exploreCharted(h *act.Hands, half int) (int, int) {
	n := 0
	in := exploreIn(half)
	for c := range h.World.Chunks {
		if in(c) {
			n++
		}
	}
	return n, 4 * half * half
}

func runExplore(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	// квадрат 1536×1536; часть недоступна — шире, пока не откроется 1500×1500 тайлов
	for half := exploreHalf; half <= exploreMaxHalf; half += 4 {
		n, all := exploreCharted(h, half)
		h.Say("разведка: квадрат %d×%d тайлов, открыто %d из %d чанков", 2*half*32, 2*half*32, n, all)
		err := h.ExploreWhere(ctx, exploreIn(half), func() bool {
			n, all := exploreCharted(h, half)
			return n == all || half > exploreHalf && n >= exploreNeed
		})
		if err != nil && !errors.Is(err, act.ErrNothingToExplore) {
			return err
		}
		if n, _ := exploreCharted(h, half); n >= exploreNeed {
			return nil
		}
	}
	return nil
}

func checkExplore(h *act.Hands) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	if n, _ := exploreCharted(h, exploreMaxHalf); n < exploreNeed {
		return fmt.Errorf("открыто %d чанков вокруг старта, нужно %d (1500×1500 тайлов)", n, exploreNeed)
	}
	return nil
}
