package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадии 8–10 (stages.md): резерв под будущие стадии планирования — пока пустые: прогон
// (scripts/stage.sh) только копирует снимок прошлой стадии, игру не запускает.

func init() {
	for n := 8; n <= 10; n++ {
		register(Stage{N: n, Name: "резерв планирования (пусто)", Plan: true, Empty: true,
			Run:   func(context.Context, *act.Hands) error { return nil },
			Check: func(*act.Hands) error { return nil }})
	}
}
