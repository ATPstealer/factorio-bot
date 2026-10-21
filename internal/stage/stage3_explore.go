package stage

import (
	"context"
	"errors"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// Стадия 3 (stages.md): разведка — открыть квадрат 1024×1024 тайла с центром в месте старта
// (0, 0): персонаж обходит его, игра открывает чанки вокруг; недоступное (вода, утёсы)
// пропускается, тогда разведка шире — пока открыто меньше 1000×1000 тайлов. Дальше план
// базы — по открытой карте.

const (
	s3Half    = 16  // чанков от центра: квадрат 32×32 чанка = 1024×1024 тайла
	s3MaxHalf = 24  // не дойти до части квадрата — разведка шире, до 48×48 чанков
	s3Need    = 977 // открыть не меньше площади 1000×1000 тайлов (977 чанков)
)

func init() {
	register(Stage{N: 3, Name: "разведка", Run: runExplore, Check: checkExplore})
}

// s3In — чанк в квадрате half чанков от центра.
func s3In(half int) func(bridge.ChunkPos) bool {
	return func(c bridge.ChunkPos) bool { return c.X >= -half && c.X < half && c.Y >= -half && c.Y < half }
}

// s3Charted — открытых чанков в квадрате half и всего в нём.
func s3Charted(h *act.Hands, half int) (int, int) {
	n := 0
	in := s3In(half)
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
	// квадрат 1024×1024; часть недоступна — шире, пока не откроется 1000×1000 тайлов
	for half := s3Half; half <= s3MaxHalf; half += 4 {
		n, all := s3Charted(h, half)
		h.Say("разведка: квадрат %d×%d тайлов, открыто %d из %d чанков", 2*half*32, 2*half*32, n, all)
		err := h.ExploreWhere(ctx, s3In(half), func() bool {
			n, all := s3Charted(h, half)
			return n == all || half > s3Half && n >= s3Need
		})
		if err != nil && !errors.Is(err, act.ErrNothingToExplore) {
			return err
		}
		if n, _ := s3Charted(h, half); n >= s3Need {
			return nil
		}
	}
	return nil
}

func checkExplore(h *act.Hands) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	if n, _ := s3Charted(h, s3MaxHalf); n < s3Need {
		return fmt.Errorf("открыто %d чанков вокруг старта, нужно %d (1000×1000 тайлов)", n, s3Need)
	}
	return nil
}
