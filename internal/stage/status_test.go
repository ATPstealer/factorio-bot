package stage

import (
	"testing"
	"time"

	"factorio-bot/internal/bridge"
)

// Сводка раз в 10 минут (stages.md «Общее для всех стадий»): время игры и стадии, оценка,
// исследование, наука по цветам (только сделанная), ракеты.
func TestStatusText(t *testing.T) {
	f := &bridge.Force{
		Current:  "steel-processing",
		Progress: 0.456,
		Produced: map[string]float64{"automation-science-pack": 1200, "logistic-science-pack": 800, "iron-plate": 5000},
	}
	got := statusText(5*time.Hour+7*time.Minute+20*time.Second, Stage{N: 17, Name: "Добыча и плавка"}, 34*time.Minute, 50*time.Minute, f)
	want := "сводка: игра идёт 5 ч 07 мин; стадия 17 «Добыча и плавка» — 34 мин (оценка 50 мин); исследую steel-processing — 45%; пакетов науки: красных 1200, зелёных 800"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	f = &bridge.Force{RocketsLaunched: 1}
	if got, want := statusText(time.Minute, Stage{N: 68, Name: "Ракета"}, 2*time.Hour, 0, f), "сводка: игра идёт 1 мин; стадия 68 «Ракета» — 2 ч 00 мин; ракет запущено: 1"; got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}
