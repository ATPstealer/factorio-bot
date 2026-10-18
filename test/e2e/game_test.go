//go:build e2e

// Прохождение игры: шаги по порядку на одной карте, одно состояние бота на всё
// (run/state-game.json, живёт до новой карты). scripts/test.sh (SET=game) гоняет их по
// одному и после каждого сохраняет снимок game-0N — продолжать с любого шага: GAME_FROM=N.
package e2e

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/exec"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/plan"
	"factorio-bot/internal/world"
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

// handMined — сколько руды/угля добыто руками за шаг (журнал; дерево и камни не в счёт).
func handMined(t *testing.T) int {
	journal, _ := os.ReadFile(path("run", "bot-test.log"))
	hand := 0
	for _, line := range strings.Split(string(journal), "\n") {
		var item string
		var n int
		if i := strings.Index(line, "шаг: добыть "); i >= 0 && !strings.Contains(line, "(tree)") && !strings.Contains(line, "(rock)") {
			if _, err := fmt.Sscanf(line[i+len("шаг: добыть "):], "%s ×%d", &item, &n); err == nil {
				hand += n
			}
		}
	}
	return hand
}

// 1. Старт (новая карта): 50 дерева, 200 камня с камней, добывающие точки по кругу — уголь
// парами, железо и медь бур → печь, камень 4 бура → сундук; руду руками — только на
// первые буры.
func TestGame01_Start(t *testing.T) {
	h := handsT(t)
	r := gameRun(t, h, 90*time.Minute)
	built := map[string]int{}
	for _, o := range r.Saved.Owned {
		if tag, _, ok := plan.ModuleOf(o.Role); ok {
			built[tag]++
		}
	}
	for _, tag := range plan.StartModules {
		if built[tag] == 0 {
			t.Errorf("точка %s не построена", tag)
		}
	}
	hand := handMined(t)
	t.Logf("руками добыто %d", hand)
	if hand > 80 {
		t.Errorf("руками добыто %d — точки не используются", hand)
	}
}

// 2. Электричество: паровая станция у воды (помпа, бойлер с сундуком-кормушкой, двигатель),
// лаборатории, столбы — всё в одной сети, бойлер с углём.
func TestGame02_Power(t *testing.T) {
	h := handsT(t)
	r := gameRun(t, h, 40*time.Minute, exec.GoalSpec{Kind: "site", Item: "research"})
	nets := map[int]bool{}
	for _, pl := range r.Saved.Designs["research"] {
		st, _ := h.B.GetEntity(pl.Name, pl.Pos)
		if st == nil {
			t.Errorf("нет %s в (%.1f, %.1f)", pl.Name, pl.Pos.X, pl.Pos.Y)
			continue
		}
		switch pl.Name {
		case "steam-engine", "lab":
			if st.Network == 0 || st.Status == "no_power" {
				t.Errorf("%s без питания (сеть %d, %s)", pl.Name, st.Network, st.Status)
			}
			nets[st.Network] = true
		case "boiler":
			if st.Count("coal") == 0 && st.Burning == "" {
				t.Errorf("бойлер без угля")
			}
		}
	}
	if len(nets) != 1 {
		t.Errorf("сетей %d: двигатель и лаборатории в разных", len(nets))
	}
}

// 3. Первое исследование: automation — наука руками в лаборатории, запуск через окно.
func TestGame03_Research(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 40*time.Minute, exec.GoalSpec{Kind: "research", Item: "automation"})
	if f, _ := h.B.Force(); !slices.Contains(f.Researched, "automation") {
		t.Error("automation не изучена")
	}
}

// 4. Рецепт сборщика: сборщик с рецептом шестерён — поставить и задать рецепт через окно.
func TestGame04_Recipe(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	lt, ok := l.FindArea(world.TileOf(p.Position), 3, 3, 1, 30, layout.OffOre)
	if !ok {
		t.Skip("нет места 3×3")
	}
	asm := layout.Placement{Name: "assembling-machine-1", Pos: layout.CenterAt(h.Protos, "assembling-machine-1", layout.N, lt), Recipe: "iron-gear-wheel", Tag: "asm"}
	l.Add(asm)
	l.Poles("small-electric-pole", "asm")
	buildT(t, h, l, "asm", 10*time.Minute)
	if st, _ := h.B.GetEntity(asm.Name, asm.Pos); st == nil || st.Recipe != asm.Recipe {
		t.Errorf("рецепт: %+v", st)
	}
}

// 5. Электробуры: electric-mining-drill — наукой руками (фабрике нужны электробуры).
func TestGame05_ElectricMining(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 40*time.Minute, exec.GoalSpec{Kind: "research", Item: "electric-mining-drill"})
	if f, _ := h.B.Force(); !slices.Contains(f.Researched, "electric-mining-drill") {
		t.Error("electric-mining-drill не изучена")
	}
}

// 6. Фабрика красной науки: ряды плавки на электробурах, сборщики с рецептами, лаборатории,
// ленты, столбы к сети станции.
func TestGame06_Factory(t *testing.T) {
	h := handsT(t)
	r := gameRun(t, h, 60*time.Minute, exec.GoalSpec{Kind: "site", Item: "science"})
	recipes := map[string]int{}
	for _, pl := range r.Saved.Designs["science"] {
		st, _ := h.B.GetEntity(pl.Name, pl.Pos)
		if st == nil {
			t.Errorf("нет %s в (%.1f, %.1f)", pl.Name, pl.Pos.X, pl.Pos.Y)
			continue
		}
		if pl.Recipe != "" {
			recipes[st.Recipe]++
		}
	}
	if recipes["iron-gear-wheel"] == 0 || recipes["automation-science-pack"] == 0 {
		t.Errorf("рецепты сборщиков: %v", recipes)
	}
}

// sciMade — сколько красной науки сделали сборщики (Lua-выражение для lua()).
const sciMade = `(function() local n=0 for _,e in pairs(s.find_entities_filtered{name="assembling-machine-1", force=p.force}) do local r=e.get_recipe() if r and r.name=="automation-science-pack" then n=n+e.products_finished end end return n end)()`

// 7. Ожидание против реальности: у станции «пропадает» топливо (бот забирает уголь из
// бойлеров и кормушек) — бот замечает «нет питания», находит причину, первым делом
// чинит (заправляет, при нехватке мощности достраивает станцию), лаборатории снова в сети.
func TestGame07_PowerOutage(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 5*time.Minute)
	for _, typ := range []string{"boiler", "container"} {
		es, err := h.B.FindNearest(bridge.FindArgs{Type: typ, Radius: 300, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			st, _ := h.B.GetEntity(e.Name, e.Position)
			if st == nil || !st.Own || st.Count("coal") == 0 || typ == "container" && !strings.Contains(fmt.Sprint(st.Inventories), "coal") {
				continue
			}
			if typ == "container" && len(st.Inventories["chest"]) != 1 { // только кормушки (один уголь)
				continue
			}
			if _, err := h.Take(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
	}
	journal := func() string { b, _ := os.ReadFile(path("run", "bot-test.log")); return string(b) }
	labsPowered := func() bool {
		var n int
		lua(t, `(function() local n=0 for _,e in pairs(s.find_entities_filtered{type="lab", force=p.force}) do if e.status==defines.entity_status.no_power or e.status==defines.entity_status.low_power then n=n+1 end end return n end)()`, &n)
		return n == 0
	}
	os.Remove(path("run", "bot-test.log"))
	r := newRunner(t, h, path("run", "state-game.json"), []exec.GoalSpec{{Kind: "start"}, {Kind: "redscience"}})
	defer r.Close()
	last := time.Now()
	r.Until = func() bool {
		if time.Since(last) < 20*time.Second {
			return false
		}
		last = time.Now()
		j := journal()
		return strings.Contains(j, "исправлено: нет питания") && labsPowered()
	}
	if err := r.Run(ctxT(t, 40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	j := journal()
	for _, want := range []string{"проблема: нет питания", "исправить расхождения", "исправлено: нет питания"} {
		if !strings.Contains(j, want) {
			t.Errorf("в журнале нет «%s»", want)
		}
	}
	if !labsPowered() {
		t.Error("лаборатории без питания")
	}
}

// 8. Всё по красной (критерий стадии 7): наукой со сборщиков, руками — только топливо.
func TestGame08_RedScience(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 120*time.Minute, exec.GoalSpec{Kind: "redscience"})
	f, _ := h.B.Force()
	for name, tt := range h.Protos.Technologies {
		if tt.Trigger == nil && !tt.Hidden && len(tt.UnitIngredients) == 1 && tt.UnitIngredients[0].Name == "automation-science-pack" && !slices.Contains(f.Researched, name) {
			t.Errorf("%s не изучена", name)
		}
	}
	var made int
	lua(t, sciMade, &made)
	t.Logf("сборщики сделали науки: %d", made)
	if made < 400 {
		t.Errorf("сборщики сделали %d науки — основная часть руками?", made)
	}
}
