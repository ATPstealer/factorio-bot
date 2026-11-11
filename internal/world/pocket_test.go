package world

import (
	"testing"

	"factorio-bot/internal/bridge"
)

// Закрытый карман — длинный, в тайл шириной (стадия 63 на 24996062: между столбцами труб, концы —
// подземные трубы): из него не выйти, Pocket его видит; Enclosed — нет (выход дальше 4 тайлов).
// Открытое место — не карман.
func TestPocket(t *testing.T) {
	w := grid(t,
		"................",
		"..wwwwwwwwwwww..",
		"..w..........w..",
		"..wwwwwwwwwwww..",
		"................",
	)
	in := bridge.Pos{X: 4.5, Y: 2.5}
	if !w.Pocket(in) {
		t.Error("карман 10×1 не виден")
	}
	if w.Enclosed(in) {
		t.Log("Enclosed тоже видит")
	}
	if w.Pocket(bridge.Pos{X: 8.5, Y: 4.5}) {
		t.Error("открытое место — карман")
	}
}
