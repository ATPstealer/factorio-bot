package stage

import (
	"context"
	"errors"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// Стадия 3 (stages.md): разведка — открыть квадрат не меньше 1000×1000 тайлов с центром в месте
// старта (0, 0): персонаж обходит его, игра открывает чанки вокруг; недоступное (вода, утёсы)
// пропускается. Дальше план базы — по открытой карте.

const (
	s3Half  = 16   // чанков от центра: квадрат 32×32 чанка = 1024×1024 тайла
	s3Share = 0.95 // открыть не меньше этой доли квадрата (остальное — недоступное)
)

func init() {
	register(Stage{N: 3, Name: "разведка", Run: runExplore, Check: checkExplore})
}

func s3Inside(c bridge.ChunkPos) bool {
	return c.X >= -s3Half && c.X < s3Half && c.Y >= -s3Half && c.Y < s3Half
}

// s3Charted — открытых чанков квадрата и всего в квадрате.
func s3Charted(h *act.Hands) (int, int) {
	n := 0
	for c := range h.World.Chunks {
		if s3Inside(c) {
			n++
		}
	}
	return n, 4 * s3Half * s3Half
}

func runExplore(ctx context.Context, h *act.Hands) error {
	if _, err := h.World.Sync(h.B, true); err != nil {
		return err
	}
	n, all := s3Charted(h)
	h.Say("разведка: квадрат %d×%d тайлов, открыто %d из %d чанков", 2*s3Half*32, 2*s3Half*32, n, all)
	err := h.ExploreWhere(ctx, s3Inside, func() bool { n, all := s3Charted(h); return n == all })
	if errors.Is(err, act.ErrNothingToExplore) {
		err = nil // остальное недоступно
	}
	return err
}

func checkExplore(h *act.Hands) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	n, all := s3Charted(h)
	if float64(n) < s3Share*float64(all) {
		return fmt.Errorf("открыто %d из %d чанков квадрата (нужно %.0f%%)", n, all, s3Share*100)
	}
	return nil
}
