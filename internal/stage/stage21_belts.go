package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадия 21 (stages.md): кластер лент по плану стадии 18 — завод с шины
// (makeLayout/runMake/checkMake): железо отводом с шины материалов, шестерни — внутри (пары
// сборщиков), выход — в ленту лент шины промежуточного, буфер выхода.

func init() {
	register(Stage{N: 21, Name: "кластер лент",
		Run:   func(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, "transport-belt", 21) },
		Check: func(h *act.Hands) error { return checkMake(h, "transport-belt", 21) }})
}
