package stage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// statusPeriod — как часто сводка в консоль игры (stages.md «Общее для всех стадий»).
const statusPeriod = 10 * time.Minute

// packNames — пакеты науки по порядку и как их называть в сводке.
var packNames = []struct{ item, name string }{
	{"automation-science-pack", "красных"},
	{"logistic-science-pack", "зелёных"},
	{"chemical-science-pack", "синих"},
	{"production-science-pack", "фиолетовых"},
	{"utility-science-pack", "жёлтых"},
	{"space-science-pack", "белых"},
}

// statusEvery — сводка раз в statusPeriod, пока идёт стадия s (с start): сколько идёт игра и
// стадия, исследование, наука, ракеты (просьба пользователя: где бот — видно без журнала).
// Только чтение (RCON); сбой чтения — сводки нет до следующего раза.
func statusEvery(ctx context.Context, h *act.Hands, s Stage, start time.Time) {
	t := time.NewTicker(statusPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p, err := h.B.Ping()
		if err != nil {
			continue
		}
		f, err := h.B.Force()
		if err != nil {
			continue
		}
		game := time.Duration(p.Tick) * time.Second / 60
		h.Say("%s", statusText(game, s, time.Since(start), time.Duration(estimate.Load()), &f))
	}
}

// statusText — текст сводки: game — время карты, took — сколько идёт стадия, est — её оценка из
// проверки перед стройкой (0 — нет).
func statusText(game time.Duration, s Stage, took, est time.Duration, f *bridge.Force) string {
	var b strings.Builder
	fmt.Fprintf(&b, "сводка: игра идёт %s; стадия %d «%s» — %s", hm(game), s.N, s.Name, hm(took))
	if est > 0 {
		fmt.Fprintf(&b, " (оценка %s)", hm(est))
	}
	if f.Current != "" {
		fmt.Fprintf(&b, "; исследую %s — %d%%", f.Current, int(f.Progress*100))
	}
	var sci []string
	for _, p := range packNames {
		if n := f.Produced[p.item]; n > 0 {
			sci = append(sci, fmt.Sprintf("%s %d", p.name, int(n)))
		}
	}
	if len(sci) > 0 {
		fmt.Fprintf(&b, "; пакетов науки: %s", strings.Join(sci, ", "))
	}
	if f.RocketsLaunched > 0 {
		fmt.Fprintf(&b, "; ракет запущено: %d", f.RocketsLaunched)
	}
	return b.String()
}

// hm — длительность «5 ч 07 мин» или «34 мин».
func hm(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	return fmt.Sprintf("%d ч %02d мин", m/60, m%60)
}
