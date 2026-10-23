package stage

import (
	"context"
	"fmt"
	"slices"
	"time"

	"factorio-bot/internal/act"
)

// Стадия 26 (stages.md): кластер зелёной науки по плану стадии 6 — завод с шины
// (makeLayout/runMake/checkMake): инсертеры и ленты отводами с шины промежуточного, выход — в
// ленту зелёной науки шины конечных; буфера выхода нет. Рецепт — после исследования
// logistic-science-pack (в очереди с обслуживания, стадия 17): не изучено — доставить в
// очередь и ждать.

// s22Wait — сколько ждать исследования рецепта зелёной науки.
const s22Wait = 40 * time.Minute

func init() {
	register(Stage{N: 26, Name: "кластер зелёной науки", Run: runGreen,
		Check: func(h *act.Hands) error { return checkMake(h, s18Green) }})
}

func runGreen(ctx context.Context, h *act.Hands) error {
	deadline := time.Now().Add(s22Wait)
	for said := false; ; said = true {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		if slices.Contains(f.Researched, s18Green) {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s не изучено за %v (идёт %s, %.0f%%)", s18Green, s22Wait, f.Current, f.Progress*100)
		}
		if !said {
			h.Say("жду исследования %s", s18Green)
		}
		if err := s17Queue(ctx, h); err != nil {
			return err
		}
		if err := sleepCtx(ctx, 30*time.Second); err != nil {
			return err
		}
	}
	return runMake(ctx, h, s18Green)
}
