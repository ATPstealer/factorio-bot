//go:build e2e

// Прохождение игры: шаги по порядку на одной карте, одно состояние бота на всё
// (run/state-game.json, живёт до новой карты). Номер теста = пункт плана (TestGame7_6 —
// стадия 7, пункт 6). scripts/test.sh (SET=game) гоняет их по одному и после каждого
// сохраняет снимок game-7.6 — продолжать с любого шага: GAME_FROM=7.6.
package e2e

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/exec"
)

// gameRun — шаг прохождения: цели — поддерживать старт (точки, топливо) и goals.
func gameRun(t *testing.T, h *act.Hands, limit time.Duration, goals ...exec.GoalSpec) *exec.Runner {
	t.Helper()
	os.Remove(path("run", "bot-test.log"))
	r := newRunner(t, h, path("run", "state-game.json"), append([]exec.GoalSpec{{Kind: "start"}}, goals...))
	t.Cleanup(func() { r.Close() })
	start := time.Now()
	if err := r.Run(ctxT(t, limit)); err != nil {
		t.Fatal(err)
	}
	t.Logf("шаг за %.0f мин", time.Since(start).Minutes())
	return r
}

// Стадия 10 — прохождение кластерами по этапам; лимит шага — 2× лимита этапа.

// 10.0. Этап 0 (новая карта, 30 мин): старт руками (дерево, камни, точки на угольных бурах),
// станция и лаборатории, изучены электробуры — всё для электрических кластеров.
func TestGame10_0_Stage0(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 60*time.Minute, exec.GoalSpec{Kind: "research", Item: "electric-mining-drill"})
	if f, _ := h.B.Force(); !slices.Contains(f.Researched, "electric-mining-drill") {
		t.Error("electric-mining-drill не изучена")
	}
}

// 10.1. Этап 1 (30 мин): добыча угля, железа, меди на электробурах → плавка (уголь — отводом
// на вторую полосу) → пластины лентой; станция power-1 на угле с ленты. Монитор пишет
// run/clusters.txt.
func TestGame10_1_Stage1(t *testing.T) {
	h := handsT(t)
	r := gameRun(t, h, 60*time.Minute, exec.GoalSpec{Kind: "site", Item: "stage-1"})
	const onBelts = `(function() local r={} for _,b in pairs(s.find_entities_filtered{type="transport-belt", force=p.force}) do for i=1,2 do for n,c in pairs(b.get_transport_line(i).get_contents()) do local name=c.name or n local cnt=c.count or c r[name]=(r[name] or 0)+cnt end end end return r end)()`
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var n map[string]int
		lua(t, onBelts, &n)
		s, err := r.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		st := r.Monitor(s)
		boiler := false
		for _, c := range st {
			if strings.HasPrefix(c.Name, "power-") && c.Status["working"] > 0 {
				boiler = true
			}
		}
		if n["iron-plate"] >= 20 && n["copper-plate"] >= 10 && boiler {
			t.Logf("на лентах: %v; станция работает", n)
			return
		}
		time.Sleep(20 * time.Second)
	}
	out, _ := os.ReadFile(path("run", "clusters.txt"))
	t.Errorf("пластины не идут лентой / станция стоит:\n%s", out)
}

// 10.2. Этап 2 (60 мин): шестерни и красная наука — свои кластеры (пластины — с этапа 1),
// лаборатории; изучено всё на красной, что нужно для ракеты.
func TestGame10_2_Stage2(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 120*time.Minute, exec.GoalSpec{Kind: "stage", N: 2})
	f, _ := h.B.Force()
	for _, tech := range []string{"automation", "logistics", "logistic-science-pack", "steel-processing"} {
		if !slices.Contains(f.Researched, tech) {
			t.Errorf("%s не изучена", tech)
		}
	}
}

// 10.3. Этап 3 (45 мин): зелёная наука кластерами — промежуточное (шестерни, провод) парами
// прямой подачи внутри потребителей, лаборатории под красную и зелёную; изучено нужное до
// нефти и стали.
func TestGame10_3_Stage3(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 120*time.Minute, exec.GoalSpec{Kind: "stage", N: 3})
	f, _ := h.B.Force()
	for _, tech := range []string{"oil-gathering", "automation-2"} {
		if !slices.Contains(f.Researched, tech) {
			t.Errorf("%s не изучена", tech)
		}
	}
}
