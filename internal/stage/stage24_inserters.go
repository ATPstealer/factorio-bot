package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадия 24 (stages.md): кластер инсертеров по плану стадии 6 — завод с шины
// (makeLayout/runMake/checkMake): железо отводом с шины материалов снизу, схемы — с шины
// промежуточного сверху, шестерни — внутри (пары сборщиков), выход — в ленту инсертеров шины,
// буфер выхода.

func init() {
	register(Stage{N: 24, Name: "кластер инсертеров",
		Run:   func(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, "inserter") },
		Check: func(h *act.Hands) error { return checkMake(h, "inserter") }})
}
