package stage

import (
	"testing"
	"time"

	"factorio-bot/internal/layout"
)

// Ждать, пока предмет пройдёт ленты стадии: короткие — не меньше заданного, длинные — по длине
// (стадия 16 на 778631482: 400 тайлов ленты угля к бойлеру, ждали 3 минуты — мало).
func TestBeltWait(t *testing.T) {
	var list []layout.Placement
	for range 400 {
		list = append(list, layout.Placement{Name: "transport-belt"})
	}
	if got := beltWait(list, 3*time.Minute); got < time.Duration(400/beltSpeed*1.5*float64(time.Second)) {
		t.Errorf("400 тайлов ленты: ждать %v — меньше полутора пути", got)
	}
	if got := beltWait(list[:10], 3*time.Minute); got != 3*time.Minute {
		t.Errorf("10 тайлов ленты: ждать %v, ждал 3 мин", got)
	}
}
