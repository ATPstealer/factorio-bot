//go:build e2e

// Стадия 7 — ранняя игра: раскладки строятся по-настоящему, электричество, исследования,
// рецепты сборщиков, автоматическая красная наука.
package e2e

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/exec"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/plan"
	"factorio-bot/internal/world"
)

// buildT — построить раскладку планировщиком (материалы, расчистка, топливо) и проверить,
// что всё стоит с нужными направлениями.
func buildT(t *testing.T, h *act.Hands, l *layout.Layout, tag string, limit time.Duration) {
	t.Helper()
	if errs := l.Validate(); len(errs) > 0 {
		t.Fatalf("раскладка с ошибками: %v", errs)
	}
	state := path("run", "state-test.json")
	os.Remove(state)
	defer os.Remove(state)
	os.Remove(path("run", "bot-test.log"))
	r := newRunner(t, h, state, []exec.GoalSpec{{Kind: "build", Item: tag, Layout: l.List}})
	defer r.Close()
	start := time.Now()
	if err := r.Run(ctxT(t, limit)); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s построено за %.0f с", tag, time.Since(start).Seconds())
	for _, pl := range l.List {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			t.Errorf("нет %s (%v)", pl, err)
			continue
		}
		if st.Direction != nil && *st.Direction != pl.Dir {
			t.Errorf("%s: направление в игре %d", pl, *st.Direction)
		}
	}
}

// Модуль плавки (2 печи, burner-инсертеры, ленты) у игрока: материалы набирает планировщик,
// место расчищается, всё стоит с нужными направлениями.
func TestStage7_Build(t *testing.T) {
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	lt, ok := l.FindArea(world.TileOf(p.Position), 6, 4, 1, 40, layout.OffOre)
	if !ok {
		t.Skip("нет места 6×4 рядом")
	}
	l.SmeltingColumn(lt, 2, "stone-furnace", "burner-inserter", "smelt")
	t.Logf("колонна в %v: %s", lt, l.Summary())

	buildT(t, h, l, "col", 15*time.Minute)
	clean(t, h)
}

// Паровая станция у воды, лаборатория рядом, столбы (раскладка "research"): всё в одной
// сети, бойлер с углём, у лаборатории есть питание.
func TestStage7_SteamPower(t *testing.T) {
	scenario(t)
	h := handsT(t)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l, err := exec.ResearchSite(h.Protos, h.World, world.TileOf(p.Position), 1)
	if err != nil {
		t.Skip(err)
	}
	t.Logf("%s", l.Summary())
	buildT(t, h, l, "power", 25*time.Minute)

	nets := map[int]bool{}
	for _, pl := range l.List {
		st, _ := h.B.GetEntity(pl.Name, pl.Pos)
		if st == nil {
			continue
		}
		switch pl.Name {
		case "steam-engine", "lab":
			if st.Network == 0 {
				t.Errorf("%s вне сети", pl.Name)
			}
			nets[st.Network] = true
			if st.Status == "no_power" {
				t.Errorf("%s без питания", pl.Name)
			}
		case "boiler":
			if st.Count("coal") == 0 && st.Burning == "" {
				t.Errorf("бойлер без угля: %+v", st)
			}
		}
	}
	if len(nets) != 1 {
		t.Errorf("сетей %d (двигатель и лаборатория — в разных)", len(nets))
	}
	clean(t, h)
}

// Исследование наукой: самая дешёвая доступная технология на красной науке — наука
// руками в лаборатории (рядом в сети — из TestStage7_SteamPower, иначе строится),
// запуск через окно технологий, ожидание.
func TestStage7_Research(t *testing.T) {
	scenario(t)
	h := handsT(t)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{f.Current: true}
	for _, r := range f.Researched {
		done[r] = true
	}
	for _, q := range f.Queue {
		done[q] = true
	}
	tech, cost := "", math.Inf(1)
	for name, tt := range h.Protos.Technologies {
		if done[name] || tt.Trigger != nil || len(tt.UnitIngredients) != 1 || tt.UnitIngredients[0].Name != "automation-science-pack" || tt.UnitCount <= 0 {
			continue
		}
		ok := true
		for _, p := range tt.Prerequisites {
			ok = ok && done[p] && p != f.Current
		}
		if ok && tt.UnitCount*tt.UnitTime < cost {
			tech, cost = name, tt.UnitCount*tt.UnitTime
		}
	}
	if tech == "" {
		t.Skip("нет доступной технологии на красной науке")
	}
	t.Logf("исследую %s (%.0f с·ед.)", tech, cost)
	state := path("run", "state-test.json")
	os.Remove(state)
	defer os.Remove(state)
	os.Remove(path("run", "bot-test.log"))
	r := newRunner(t, h, state, []exec.GoalSpec{{Kind: "research", Item: tech}})
	defer r.Close()
	start := time.Now()
	if err := r.Run(ctxT(t, 25*time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Logf("изучено за %.0f с", time.Since(start).Seconds())
	f, _ = h.B.Force()
	if !slices.Contains(f.Researched, tech) {
		t.Errorf("%s не изучена", tech)
	}
	clean(t, h)
}

// Лента-змейка (6 рядов по 5 тайлов, 10 изгибов): построить, положить пластину в начало —
// она доезжает до конца.
func TestStage7_Belt(t *testing.T) {
	h := handsT(t)
	have(t, h, "transport-belt", 30)
	have(t, h, "iron-plate", 1)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	l := layout.New(h.Protos, h.World)
	const rows, length = 6, 5
	lt, ok := l.FindArea(world.TileOf(p.Position), length, rows, 1, 30, layout.OffOre)
	if !ok {
		t.Skip("нет места под змейку")
	}
	// ряды по очереди на восток и на запад; в конце ряда — вниз (2 изгиба на переход)
	var tiles []world.Tile
	for r := 0; r < rows; r++ {
		for k := 0; k < length; k++ {
			x := lt.X + k
			if r%2 == 1 {
				x = lt.X + length - 1 - k
			}
			tiles = append(tiles, world.Tile{X: x, Y: lt.Y + r})
		}
	}
	bends := 0
	for i, tl := range tiles {
		d := layout.S
		if i+1 < len(tiles) {
			n := tiles[i+1]
			switch {
			case n.X > tl.X:
				d = layout.E
			case n.X < tl.X:
				d = layout.W
			}
		} else {
			d = l.List[len(l.List)-1].Dir
		}
		if i > 0 && d != l.List[len(l.List)-1].Dir {
			bends++
		}
		l.Add(layout.Placement{Name: "transport-belt", Pos: tl.Center(), Dir: d, Tag: "snake"})
	}
	last := l.List[len(l.List)-1]
	l.AllowEnd(layout.Step(world.TileOf(last.Pos), last.Dir))
	if bends < 10 {
		t.Fatalf("изгибов %d < 10", bends)
	}
	buildT(t, h, l, "snake", 10*time.Minute)

	first, err := h.B.EntityAt("transport-belt", l.List[0].Pos)
	if err != nil || first == nil {
		t.Fatalf("нет начала ленты: %v", err)
	}
	if err := h.Insert(ctxT(t, time.Minute), *first, "iron-plate", 1); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for time.Since(start) < 40*time.Second {
		if st, _ := h.B.GetEntity(last.Name, last.Pos); st != nil && st.Belt["iron-plate"] > 0 {
			t.Logf("пластина доехала за %.0f с, изгибов %d", time.Since(start).Seconds(), bends)
			clean(t, h)
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("пластина не доехала до конца ленты за 40 с")
}

// Рецепт сборщика: раскладка из сборщика с рецептом шестерён — поставить и задать рецепт
// через окно выбора (поиск по названию).
func TestStage7_SetRecipe(t *testing.T) {
	h := handsT(t)
	if f, _ := h.B.Force(); !slices.Contains(f.Researched, "automation") {
		t.Skip("automation не изучена")
	}
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
	clean(t, h)
}

// Старт игры (7.0): дерево, камни, добывающие точки по кругу — уголь парами, железо и медь
// бур → печь, камень 4 бура → сундук; руду руками — только на первые буры.
func TestStage7_Start(t *testing.T) {
	scenario(t)
	h := handsT(t)
	// состояние — своё и живёт до новой карты (run.sh стирает): перезапуск теста
	// продолжает начатое, а не строит точки заново рядом
	state := path("run", "state-start.json")
	os.Remove(path("run", "bot-test.log"))
	r := newRunner(t, h, state, []exec.GoalSpec{{Kind: "start"}})
	defer r.Close()
	start := time.Now()
	if err := r.Run(ctxT(t, 90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Logf("старт за %.0f с", time.Since(start).Seconds())
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
	t.Logf("руками добыто %d", hand)
	if hand > 80 {
		t.Errorf("руками добыто %d — точки не используются", hand)
	}
}

// sciMade — сколько красной науки сделали сборщики (Lua-выражение для lua()).
const sciMade = `(function() local n=0 for _,e in pairs(s.find_entities_filtered{name="assembling-machine-1", force=p.force}) do local r=e.get_recipe() if r and r.name=="automation-science-pack" then n=n+e.products_finished end end return n end)()`

// Автоматическая красная наука (7.5, долгий): от точек старта (состояние TestStage7_Start)
// — фабрика (ряды плавки, сборщики, лаборатории, ленты), исследования идут наукой со
// сборщиков: изучено ≥1 технологии после постройки фабрики.
func TestStage7_RedScience(t *testing.T) {
	scenario(t)
	h := handsT(t)
	state := path("run", "state-start.json")
	os.Remove(path("run", "bot-test.log"))
	r := newRunner(t, h, state, []exec.GoalSpec{{Kind: "start"}, {Kind: "redscience"}})
	defer r.Close()
	var before int
	built := false
	last := time.Now()
	r.Until = func() bool {
		if time.Since(last) < 30*time.Second {
			return false
		}
		last = time.Now()
		f, err := h.B.Force()
		if err != nil {
			return false
		}
		var made int
		lua(t, sciMade, &made)
		if !built {
			if made > 0 {
				built, before = true, len(f.Researched)
				t.Logf("наука со сборщиков пошла, изучено %d", before)
			}
			return false
		}
		return len(f.Researched) > before
	}
	if err := r.Run(ctxT(t, 150*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !built {
		t.Fatal("фабрика не дала науки")
	}
	var made int
	lua(t, sciMade, &made)
	if made == 0 {
		t.Error("сборщики не сделали ни одной красной науки")
	}
	journal, _ := os.ReadFile(path("run", "bot-test.log"))
	if strings.Contains(string(journal), "положить automation-science-pack") && built {
		t.Log("были ручные подкладки науки (до automation — допустимо)")
	}
	t.Logf("сборщики сделали науки: %d", made)
}
