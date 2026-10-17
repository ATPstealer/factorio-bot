package calc

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestMinimizeSimple(t *testing.T) {
	// min x+y при x+2y ≥ 4, 3x+y ≥ 6 → x=1.6, y=1.2
	x, err := Minimize([]float64{1, 1}, [][]float64{{1, 2}, {3, 1}}, []float64{4, 6})
	if err != nil {
		t.Fatal(err)
	}
	if !near(x[0], 1.6) || !near(x[1], 1.2) {
		t.Errorf("x = %v", x)
	}
}

func TestMinimizeZeroRows(t *testing.T) {
	// производство: рецепт r1 делает A из B (1A ← 2B), r2 добывает B; нужно 3 A
	// строки: A: r1 ≥ 3;  B: −2 r1 + r2 ≥ 0
	x, err := Minimize([]float64{0.001, 1}, [][]float64{{1, 0}, {-2, 1}}, []float64{3, 0})
	if err != nil {
		t.Fatal(err)
	}
	if !near(x[0], 3) || !near(x[1], 6) {
		t.Errorf("x = %v", x)
	}
}

func TestMinimizeInfeasible(t *testing.T) {
	// x ≥ 1 и −x ≥ 0
	if _, err := Minimize([]float64{1}, [][]float64{{1}, {-1}}, []float64{1, 0}); err != ErrInfeasible {
		t.Errorf("err = %v", err)
	}
}

func TestMinimizeChoosesCheaper(t *testing.T) {
	// два способа сделать A: дешёвый (стоимость 1) и дорогой (5)
	x, err := Minimize([]float64{5, 1}, [][]float64{{1, 1}}, []float64{2})
	if err != nil {
		t.Fatal(err)
	}
	if !near(x[0], 0) || !near(x[1], 2) {
		t.Errorf("x = %v", x)
	}
}
