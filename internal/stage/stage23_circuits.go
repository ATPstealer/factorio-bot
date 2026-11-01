package stage

import (
	"context"

	"factorio-bot/internal/act"
)

// Стадия 23 (stages.md): кластер провода и схем — сборка с шины (makeLayout/runMake).

const circuitProduct = "electronic-circuit"

func init() {
	register(Stage{N: 23, Name: "кластер провода и схем", Run: runCircuits, Check: checkCircuits})
}

func runCircuits(ctx context.Context, h *act.Hands) error { return runMake(ctx, h, circuitProduct) }

func checkCircuits(h *act.Hands) error { return checkMake(h, circuitProduct) }
