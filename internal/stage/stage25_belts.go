package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадия 25 (stages.md): кластер лент по плану стадии 6 — завод с шины
// (makeLayout/runMake/checkMake): железо отводом с шины материалов, шестерни — внутри (пары
// сборщиков), выход — в ленту лент шины промежуточного, буфер выхода.

func init() {
	register(Stage{N: 25, Name: "кластер лент",
		Run:   func(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, "transport-belt") },
		Check: func(h *act.Hands) error { return checkMake(h, "transport-belt") }})
}
