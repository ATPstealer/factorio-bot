package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадия 50 (stages.md): замена всех печей на стальные и сборщиков 1 на сборщики 2 на месте
// (размер тот же, рецепт сохраняется). Механики замены поверх стоящего ещё нет — стадия пустая
// (refactor.md, шаг 6).

func init() {
	register(Stage{N: 50, Name: "замена печей и сборщиков на второй уровень (ещё не сделано)", Empty: true,
		Run:   func(context.Context, *act.Hands) error { return nil },
		Check: func(*act.Hands) error { return nil }})
}
