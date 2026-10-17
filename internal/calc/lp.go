// Package calc — калькулятор производства: сколько чего крафтить, машин, сырья, энергии.
package calc

import (
	"errors"
	"math"
)

const eps = 1e-9

var (
	ErrInfeasible = errors.New("calc: задача несовместна (цели не произвести)")
	ErrUnbounded  = errors.New("calc: задача не ограничена")
)

// Minimize решает min c·x при A·x ≥ b, x ≥ 0 двухфазным симплексом (правило Бленда —
// без зацикливания). Задачи калькулятора маленькие (сотни переменных), плотной таблицы хватает.
func Minimize(c []float64, A [][]float64, b []float64) ([]float64, error) {
	m, n := len(A), len(c)
	// столбцы: x (n) | s — по одному на строку (m) | искусственные (для b>0)
	var artRows []int
	for i := range b {
		if b[i] > eps {
			artRows = append(artRows, i)
		}
	}
	N := n + m + len(artRows)
	T := make([][]float64, m)
	basis := make([]int, m)
	rhs := make([]float64, m)
	ai := 0
	for i := 0; i < m; i++ {
		T[i] = make([]float64, N)
		if b[i] > eps {
			// A·x − s + a = b
			copy(T[i], A[i])
			T[i][n+i] = -1
			T[i][n+m+ai] = 1
			basis[i] = n + m + ai
			ai++
			rhs[i] = b[i]
		} else {
			// A·x ≥ b (b ≤ 0)  ⇔  −A·x + s = −b
			for j := 0; j < n; j++ {
				T[i][j] = -A[i][j]
			}
			T[i][n+i] = 1
			basis[i] = n + i
			rhs[i] = -b[i]
		}
	}

	// фаза 1: минимизировать сумму искусственных
	cost1 := make([]float64, N)
	for j := n + m; j < N; j++ {
		cost1[j] = 1
	}
	if err := simplex(T, rhs, basis, cost1, N); err != nil {
		return nil, err
	}
	sum := 0.0
	for i, bv := range basis {
		if bv >= n+m {
			sum += rhs[i]
		}
	}
	if sum > 1e-7 {
		return nil, ErrInfeasible
	}
	// вывести искусственные из базиса (они на нуле)
	for i, bv := range basis {
		if bv < n+m {
			continue
		}
		for j := 0; j < n+m; j++ {
			if math.Abs(T[i][j]) > eps {
				pivot(T, rhs, basis, i, j)
				break
			}
		}
	}

	// фаза 2: исходная цель, искусственные запрещены
	cost2 := make([]float64, N)
	copy(cost2, c)
	if err := simplex(T, rhs, basis, cost2, n+m); err != nil {
		return nil, err
	}
	x := make([]float64, n)
	for i, bv := range basis {
		if bv < n {
			x[bv] = rhs[i]
		}
	}
	return x, nil
}

// simplex — минимизация cost по таблице; входить в базис могут только столбцы < allowed.
func simplex(T [][]float64, rhs []float64, basis []int, cost []float64, allowed int) error {
	m := len(T)
	for iter := 0; iter < 50000; iter++ {
		// приведённые стоимости: r_j = c_j − c_B·T_j
		enter := -1
		for j := 0; j < allowed; j++ {
			r := cost[j]
			for i := 0; i < m; i++ {
				if T[i][j] != 0 {
					r -= cost[basis[i]] * T[i][j]
				}
			}
			if r < -eps {
				enter = j // Бленд: первый подходящий
				break
			}
		}
		if enter < 0 {
			return nil
		}
		leave, best := -1, math.Inf(1)
		for i := 0; i < m; i++ {
			if T[i][enter] > eps {
				ratio := rhs[i] / T[i][enter]
				if ratio < best-eps || (math.Abs(ratio-best) <= eps && basis[i] < basis[leave]) {
					leave, best = i, ratio
				}
			}
		}
		if leave < 0 {
			return ErrUnbounded
		}
		pivot(T, rhs, basis, leave, enter)
	}
	return errors.New("calc: симплекс не сошёлся")
}

func pivot(T [][]float64, rhs []float64, basis []int, r, c int) {
	pv := T[r][c]
	row := T[r]
	for j := range row {
		row[j] /= pv
	}
	rhs[r] /= pv
	for i := range T {
		if i == r {
			continue
		}
		f := T[i][c]
		if f == 0 {
			continue
		}
		ti := T[i]
		for j := range ti {
			if row[j] != 0 {
				ti[j] -= f * row[j]
			}
		}
		rhs[i] -= f * rhs[r]
	}
	basis[r] = c
}
