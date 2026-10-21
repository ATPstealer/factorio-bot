package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадия 14 (stages.md): кластер красной науки по плану стадии 4 полностью — завод с шины
// (makeLayout/runMake/checkMake): медь и шестерни отводами с шины, буферы, сборщики с
// рецептом, пакеты — в ленту шины, буфер выхода, питание по коридорам.

func init() {
	register(Stage{N: 14, Name: "кластер красной науки",
		Run:   func(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, "automation-science-pack", 14) },
		Check: func(h *act.Hands) error { return checkMake(h, "automation-science-pack", 14) }})
}
