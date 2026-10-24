package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадия 26 (stages.md): кластер зелёной науки по плану стадии 6 — завод с шины
// (makeLayout/runMake/checkMake): инсертеры и ленты отводами с шины промежуточного, выход — в
// ленту зелёной науки шины конечных; буфера выхода нет. Строится сразу; рецепт — после
// исследования logistic-science-pack (в очереди с обслуживания, стадия 22): не изучено —
// runMake доставляет в очередь и ждёт перед рецептом.

func init() {
	register(Stage{N: 26, Name: "кластер зелёной науки",
		Run:   func(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, greenPack) },
		Check: func(h *act.Hands) error { return checkMake(h, greenPack) }})
}
