package stage

import (
	"testing"
	"time"
)

// Отсчёт перед запуском (stages.md, стадия 68): время до запуска — по приросту частей ракеты за
// последние минуты; замеров меньше минуты или части не прибывают — не оценить; ракета собрана — 0;
// частей стало меньше (ракета ушла) — замеры заново.
func TestLaunchClock(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	c := &launchClock{}
	c.add(t0, 40)
	if _, ok := c.eta(); ok {
		t.Error("один замер — оценки нет")
	}
	c.add(t0.Add(30*time.Second), 41)
	if _, ok := c.eta(); ok {
		t.Error("замеры за полминуты — оценки нет")
	}
	c.add(t0.Add(2*time.Minute), 44) // 4 части за 2 минуты — 2/мин, осталось 56 — 28 минут
	if eta, ok := c.eta(); !ok || eta != 28*time.Minute {
		t.Errorf("оценка %v (%v), ждал 28 мин", eta, ok)
	}
	if eta, _ := c.eta(); eta <= countdownAt {
		t.Error("28 минут — отсчёт ещё рано")
	}
	c.add(t0.Add(3*time.Minute), 44)
	c.add(t0.Add(4*time.Minute), 44) // встали: прирост за всё окно ещё есть
	if _, ok := c.eta(); !ok {
		t.Error("прирост в окне есть — оценка есть")
	}
	c.add(t0.Add(5*time.Minute), rocketParts)
	if eta, ok := c.eta(); !ok || eta != 0 {
		t.Errorf("собрана: %v (%v), ждал 0", eta, ok)
	}
	c.add(t0.Add(6*time.Minute), 0) // ушла — заново
	if _, ok := c.eta(); ok {
		t.Error("после запуска — заново, оценки нет")
	}
	// ракета собрана — части обнуляются, 100 не увидеть: по состоянию шахты (не строит)
	if c.soon() {
		t.Error("оценки нет, шахта строит — не пора")
	}
	c.built = true
	if !c.soon() {
		t.Error("ракета собрана — пора")
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "меньше минуты", 61 * time.Second: "~2 мин", 10 * time.Minute: "~10 мин"} {
		if got := inMinutes(d); got != want {
			t.Errorf("inMinutes(%v) = %q, ждал %q", d, got, want)
		}
	}
}
