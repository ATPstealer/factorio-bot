//go:build e2e

// Прохождение игры: шаги по порядку на одной карте, одно состояние бота на всё
// (run/state-game.json, живёт до новой карты). Номер теста = пункт плана (TestGame7_6 —
// стадия 7, пункт 6). scripts/test.sh (SET=game) гоняет их по одному и после каждого
// сохраняет снимок game-7.6 — продолжать с любого шага: GAME_FROM=7.6.
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

// 7.0. Старт (новая карта): 50 дерева, 200 камня с камней, добывающие точки по кругу — уголь
// парами, железо и медь бур → печь, камень 4 бура → сундук; руду руками — только на
// первые буры.
func TestGame7_0_Start(t *testing.T) {
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

// 7.2. Электричество: паровая станция у воды (помпа, бойлер с сундуком-кормушкой, двигатель),
// лаборатории, столбы — всё в одной сети, бойлер с углём.
func TestGame7_2_Power(t *testing.T) {
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

// 7.3. Первое исследование: automation — наука руками в лаборатории, запуск через окно.
func TestGame7_3_Research(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 40*time.Minute, exec.GoalSpec{Kind: "research", Item: "automation"})
	if f, _ := h.B.Force(); !slices.Contains(f.Researched, "automation") {
		t.Error("automation не изучена")
	}
}

// 7.4. Рецепт сборщика: сборщик с рецептом шестерён — поставить и задать рецепт через окно.
func TestGame7_4_Recipe(t *testing.T) {
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

// 7.5. Электробуры: electric-mining-drill — наукой руками (фабрике нужны электробуры).
func TestGame7_5_ElectricMining(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 40*time.Minute, exec.GoalSpec{Kind: "research", Item: "electric-mining-drill"})
	if f, _ := h.B.Force(); !slices.Contains(f.Researched, "electric-mining-drill") {
		t.Error("electric-mining-drill не изучена")
	}
}

// 7.6. Фабрика красной науки: ряды плавки на электробурах, сборщики с рецептами, лаборатории,
// ленты, столбы к сети станции.
func TestGame7_6_Factory(t *testing.T) {
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

// 7.7. Ожидание против реальности: у станции «пропадает» топливо (бот забирает уголь из
// бойлеров и кормушек) — бот замечает «нет питания», находит причину, первым делом
// чинит (заправляет, при нехватке мощности достраивает станцию), лаборатории снова в сети.
func TestGame7_7_PowerOutage(t *testing.T) {
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

// 7.8. Всё по красной, что нужно для ракеты (критерий стадии 7; оружие и прочее — нет):
// наукой со сборщиков, руками — только топливо.
func TestGame7_8_RedScience(t *testing.T) {
	h := handsT(t)
	f0, _ := h.B.Force()
	before := f0.Researched
	gameRun(t, h, 120*time.Minute, exec.GoalSpec{Kind: "redscience"})
	f, _ := h.B.Force()
	for _, name := range []string{"automation", "logistics", "logistic-science-pack", "steel-processing", "radar"} {
		if !slices.Contains(f.Researched, name) {
			t.Errorf("%s не изучена", name)
		}
	}
	for _, name := range []string{"military", "gun-turret", "stone-wall", "heavy-armor"} {
		if slices.Contains(f.Researched, name) && !slices.Contains(before, name) {
			t.Errorf("%s изучена — для ракеты не нужна", name)
		}
	}
	var made int
	lua(t, sciMade, &made)
	t.Logf("сборщики сделали науки: %d", made)
	if made < 100 {
		t.Errorf("сборщики сделали %d науки — основная часть руками?", made)
	}
}

// 8.1. Зелёная наука: блок прямой подачи (кабель → схемы → инсертеры ← шестерни → ленты →
// наука) с рядами плавки построен, зелёная наука доходит до лабораторий.
func TestGame8_1_GreenScience(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 60*time.Minute, exec.GoalSpec{Kind: "site", Item: "green"})
	const greenInLabs = `(function() local n=0 for _,e in pairs(s.find_entities_filtered{type="lab", force=p.force}) do n=n+e.get_inventory(defines.inventory.lab_input).get_item_count("logistic-science-pack") end return n end)()`
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		var n int
		lua(t, greenInLabs, &n)
		if n > 0 {
			t.Logf("зелёная в лабораториях: %d", n)
			return
		}
		time.Sleep(15 * time.Second)
	}
	var made int
	lua(t, `(function() local n=0 for _,e in pairs(s.find_entities_filtered{name="assembling-machine-1", force=p.force}) do local r=e.get_recipe() if r and r.name=="logistic-science-pack" then n=n+e.products_finished end end return n end)()`, &made)
	t.Errorf("зелёная наука не дошла до лабораторий (сборщики сделали %d)", made)
}

// 8.2. Исследования на красной+зелёной — путь к нефти (oil-gathering: fluid-handling,
// engine, automation-2): наукой со сборщиков.
func TestGame8_2_GreenResearch(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 90*time.Minute, exec.GoalSpec{Kind: "greenscience", Item: "oil-gathering"})
	f, _ := h.B.Force()
	for _, name := range []string{"fluid-handling", "oil-gathering"} {
		if !slices.Contains(f.Researched, name) {
			t.Errorf("%s не изучена", name)
		}
	}
}

// 8.3. Сталь: столбики «электробур → печь → печь (сталь) → сундук», сталь копится.
func TestGame8_3_Steel(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 40*time.Minute, exec.GoalSpec{Kind: "site", Item: "steel"})
	const steel = `(function() local n=0 for _,e in pairs(s.find_entities_filtered{name="wooden-chest", force=p.force}) do n=n+e.get_item_count("steel-plate") end return n end)()`
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		var n int
		lua(t, steel, &n)
		if n >= 3 {
			t.Logf("стали в сундуках: %d", n)
			return
		}
		time.Sleep(15 * time.Second)
	}
	t.Error("сталь не копится")
}

// 8.5. Нефть: насосы → нефтезавод → химзаводы; пластик и сера копятся в сундуках.
// oil-processing — trigger (добыть нефть), plastics и sulfur-processing — наукой, по ходу.
func TestGame8_5_Oil(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 120*time.Minute, exec.GoalSpec{Kind: "site", Item: "oil"})
	const count = `(function() local r={} for _,e in pairs(s.find_entities_filtered{name="wooden-chest", force=p.force}) do for _,n in pairs{"plastic-bar","sulfur"} do r[n]=(r[n] or 0)+e.get_item_count(n) end end return r end)()`
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		var n map[string]int
		lua(t, count, &n)
		if n["plastic-bar"] >= 5 && n["sulfur"] >= 5 {
			t.Logf("в сундуках: %v", n)
			return
		}
		time.Sleep(15 * time.Second)
	}
	t.Error("пластик и сера не копятся")
}

// 8.6. Уголь большими партиями: шахта на электробурах с сундуками, уголь копится.
func TestGame8_6_CoalMine(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 40*time.Minute, exec.GoalSpec{Kind: "site", Item: "coal-mine"})
	const coal = `(function() local n=0 for _,d in pairs(s.find_entities_filtered{name="electric-mining-drill", force=p.force}) do local t=d.drop_target if t and t.name=="wooden-chest" then n=n+t.get_item_count("coal") end end return n end)()`
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		var n int
		lua(t, coal, &n)
		if n >= 30 {
			t.Logf("угля в сундуках шахты: %d", n)
			return
		}
		time.Sleep(15 * time.Second)
	}
	t.Error("уголь в шахте не копится")
}

// 8.7. Пластик (и сера, если её ленту подвести) на базу: лента от химзаводов у нефти до
// буфера у фабрики.
func TestGame8_7_OilBelt(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 90*time.Minute, exec.GoalSpec{Kind: "site", Item: "oil-belt"})
	// в сундуках дальше 100 тайлов от насоса — то, что доехало по ленте
	const count = `(function() local j=s.find_entities_filtered{name="pumpjack", force=p.force}[1] local r={} for _,e in pairs(s.find_entities_filtered{name="wooden-chest", force=p.force}) do local dx,dy=e.position.x-j.position.x,e.position.y-j.position.y if dx*dx+dy*dy>100*100 then for _,n in pairs{"plastic-bar","sulfur"} do r[n]=(r[n] or 0)+e.get_item_count(n) end end end return r end)()`
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var n map[string]int
		lua(t, count, &n)
		if n["plastic-bar"] >= 5 {
			t.Logf("доехало до базы: %v", n)
			return
		}
		time.Sleep(15 * time.Second)
	}
	t.Error("пластик не доезжает до базы")
}

// 8.8. Синяя наука: блок (красные схемы, двигатели, синяя), пластик с ленты 8.7, сера и
// сталь — бот; синяя доходит до лабораторий.
func TestGame8_8_BlueScience(t *testing.T) {
	h := handsT(t)
	gameRun(t, h, 180*time.Minute, exec.GoalSpec{Kind: "site", Item: "blue"})
	const blue = `(function() local n=0 for _,e in pairs(s.find_entities_filtered{name="lab", force=p.force}) do n=n+e.get_item_count("chemical-science-pack") end return n end)()`
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		var n int
		lua(t, blue, &n)
		if n > 0 {
			t.Logf("синей в лабораториях: %d", n)
			return
		}
		time.Sleep(15 * time.Second)
	}
	t.Error("синяя наука не доходит до лабораторий")
}

// Стадия 10 — прохождение кластерами по этапам game-stages.md; лимит шага — 2× лимита этапа.

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
