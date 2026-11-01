package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадии 3–9 (stages.md): резерв планирования — пустые (весь план — стадия 2): прогон
// (scripts/stage.sh) только копирует снимок прошлой стадии, игру не запускает.

func init() {
	for n := 3; n <= 9; n++ {
		register(Stage{N: n, Name: "резерв планирования (пусто)", Plan: true, Empty: true,
			Run:   func(context.Context, *act.Hands) error { return nil },
			Check: func(*act.Hands) error { return nil }})
	}
}
