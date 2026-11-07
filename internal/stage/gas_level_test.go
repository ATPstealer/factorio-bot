package stage

import (
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
)

// Газ по уровню (stages.md, общее): газ полон, а лёгкая нет — выключить один НПЗ простой
// переработки; газа нет, а лёгкая полна — включить; выключать нечего, газ полон и лёгкой нет —
// слить; иначе — ничего (пороги не пересекаются: туда-сюда не ходит).
func TestGasDecide(t *testing.T) {
	for _, c := range []struct {
		gas, light float64
		on, off    int
		want       gasStep
	}{
		{0.95, 0.20, 3, 0, gasTurnOff},
		{0.90, 0.89, 1, 2, gasTurnOff},
		{0.95, 0.95, 3, 0, gasKeep},   // обе полны — расхода нет, выключать незачем
		{0.50, 0.20, 3, 0, gasKeep},   // газу есть место
		{0.05, 0.95, 0, 3, gasTurnOn}, // газа нет, лёгкая стоит
		{0.05, 0.95, 3, 0, gasKeep},   // включать нечего
		{0.05, 0.50, 0, 3, gasKeep},   // лёгкой есть куда
		{0.97, 0.02, 0, 3, gasFlush},  // выключены все — слить
		{0.92, 0.02, 0, 3, gasKeep},   // ещё не полон для слива
		{0.97, 0.02, 1, 2, gasTurnOff},
	} {
		if got := gasDecide(c.gas, c.light, c.on, c.off); got != c.want {
			t.Errorf("газ %.2f, лёгкая %.2f, с рецептом %d, без %d: %d, ждали %d", c.gas, c.light, c.on, c.off, got, c.want)
		}
	}
}

// Без рецепта по правилу: крекинг лёгкой — с lightCrackFrom; НПЗ простой переработки — только
// выключенные по уровню газа (по месту); остальное — с рецептом.
func TestRecipeOff(t *testing.T) {
	defer func(n int) { curStage = n }(curStage)
	crack := layout.Placement{Name: "chemical-plant", Recipe: "light-oil-cracking", Pos: bridge.Pos{X: 1.5, Y: 1.5}}
	ref := layout.Placement{Name: "oil-refinery", Recipe: basicOil, Pos: bridge.Pos{X: 10.5, Y: 2.5}}
	ref2 := layout.Placement{Name: "oil-refinery", Recipe: basicOil, Pos: bridge.Pos{X: 16.5, Y: 2.5}}
	off := &gasOff{Off: []bridge.Pos{ref.Pos}}
	curStage = lightCrackFrom - 1
	if recipeOff(crack, off) {
		t.Errorf("стадия %d: крекинг лёгкой уже без рецепта", curStage)
	}
	curStage = lightCrackFrom
	if !recipeOff(crack, off) {
		t.Errorf("стадия %d: крекинг лёгкой с рецептом", curStage)
	}
	if !recipeOff(ref, off) || recipeOff(ref2, off) {
		t.Errorf("НПЗ: выключенный %v, другой %v — ждали true, false", recipeOff(ref, off), recipeOff(ref2, off))
	}
}
