//go:build e2e

// Стадия 5 — планировщик: цели → HTN → шаги → руки; перепланирование, восстановление,
// продолжение после рестарта.
package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/exec"
	"factorio-bot/internal/plan"
	"factorio-bot/internal/world"
)

// stash — «пустой инвентарь» честно: всё переложить в сундуки рядом.
func stash(t *testing.T, h *act.Hands) {
	t.Helper()
	ctx := ctxT(t, 5*time.Minute)
	have(t, h, "wooden-chest", 3)
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	noOre := func(tl world.Tile) bool { _, ok := h.World.ResourceAt(tl); return !ok }
	c, ok := h.World.FreeSpotWhere(p.Position, 5, 1, 20, &p.Position, noOre)
	if !ok {
		t.Skip("нет места для сундуков")
	}
	var spots []bridge.Pos
	for i := 0; i < 3; i++ {
		spots = append(spots, bridge.Pos{X: c.X - 2 + float64(i)*2, Y: c.Y})
	}
	chests, err := h.PlaceAll(ctx, "wooden-chest", spots, act.North)
	if err != nil {
		t.Fatal(err)
	}
	for {
		p, _ := h.B.Player()
		if len(p.Inventory) == 0 {
			break
		}
		moved := false
		for it := range p.Inventory {
			for _, ch := range chests {
				n, err := h.InsertAll(ctx, ch, it)
				if err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					moved = true
				}
				if q, _ := h.B.Player(); q.Inventory[it] == 0 {
					break
				}
			}
		}
		if !moved {
			t.Fatalf("сундуки полны, в инвентаре осталось %v", p.Inventory)
		}
	}
}

func newRunner(t *testing.T, h *act.Hands, state string, goals []exec.GoalSpec) *exec.Runner {
	t.Helper()
	saved, err := exec.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	if goals != nil {
		saved.Goals = goals
	}
	return &exec.Runner{H: h, B: h.B, Saved: saved, StatePath: state, LogPath: path("run", "bot-test.log")}
}

// have — довести число предмета в инвентаре до n планировщиком (добыть, разведать,
// скрафтить), как это делает бот.
func have(t *testing.T, h *act.Hands, item string, n int) {
	t.Helper()
	if p, _ := h.B.Player(); p.Inventory[item] >= n {
		return
	}
	state := path("run", "state-have.json")
	os.Remove(state)
	defer os.Remove(state)
	r := newRunner(t, h, state, []exec.GoalSpec{{Kind: "have", Item: item, N: n}})
	defer r.Close()
	// много на свежей карте (130 камня под «улитку») — площадка буров и ожидание
	if err := r.Run(ctxT(t, 15*time.Minute)); err != nil {
		t.Fatalf("набрать %s ×%d: %v", item, n, err)
	}
}

func lineOK(t *testing.T, h *act.Hands, r *exec.Runner, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		role := plan.LineRole("iron-ore", i)
		var o *plan.Owned
		for j := range r.Saved.Owned {
			if r.Saved.Owned[j].Role == role {
				o = &r.Saved.Owned[j]
			}
		}
		if o == nil {
			t.Errorf("нет печи %s", role)
			continue
		}
		st, err := h.B.GetEntity(o.Name, o.Pos)
		if err != nil || st == nil {
			t.Errorf("%s в игре нет (%v)", role, err)
			continue
		}
		if st.Inventories["fuel"]["coal"] < 1 && st.Burning == "" {
			t.Errorf("%s без угля: %+v", role, st.Inventories)
		}
		if st.Status != "working" && len(st.Inventories["crafter_output"]) == 0 {
			t.Errorf("%s не плавит: статус %s, %+v", role, st.Status, st.Inventories)
		}
	}
}

// Критерий стадии: с пустого инвентаря — 5 каменных печей на железе с углём; убрали печь —
// бот (новый процесс, из сохранённого состояния) замечает и восстанавливает.
func TestExtra_SmeltingLine(t *testing.T) {
	h := handsT(t)
	state := path("run", "state-test.json")
	os.Remove(state)
	defer os.Remove(state)
	os.Remove(path("run", "bot-test.log"))

	stash(t, h)
	if p, _ := h.B.Player(); len(p.Inventory) != 0 {
		t.Fatalf("инвентарь не пуст: %v", p.Inventory)
	}

	goals := []exec.GoalSpec{{Kind: "smelt", Item: "iron-ore", N: 5}}
	r := newRunner(t, h, state, goals)
	defer r.Close()
	start := time.Now()
	if err := r.Run(ctxT(t, 20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Logf("линия с нуля за %.0f с", time.Since(start).Seconds())
	time.Sleep(2 * time.Second) // печи разгорелись
	lineOK(t, h, r, 5)

	// много угля и руды — бурами: на угле бур с сундуком, уголь из сундука идёт и в печи,
	// и обратно в бур; руками — только мелочь
	coalDrill, coalChest := false, false
	for _, o := range r.Saved.Owned {
		coalDrill = coalDrill || o.Role == plan.SiteRole("coal", 1)
		coalChest = coalChest || o.Role == plan.OutRole("coal", 1)
	}
	if !coalDrill || !coalChest {
		t.Errorf("нет бура с сундуком на угле: %+v", r.Saved.Owned)
	}
	journal, _ := os.ReadFile(path("run", "bot-test.log"))
	for _, want := range []string{"шаг: забрать из " + plan.OutRole("coal", 1), "шаг: положить coal ×5 в " + plan.SiteRole("coal", 1)} {
		if !strings.Contains(string(journal), want) {
			t.Errorf("в журнале нет «%s»", want)
		}
	}
	for _, line := range strings.Split(string(journal), "\n") {
		var item string
		var n int
		if i := strings.Index(line, "шаг: добыть "); i >= 0 {
			if _, err := fmt.Sscanf(line[i+len("шаг: добыть "):], "%s ×%d", &item, &n); err == nil && n >= plan.DrillThreshold {
				t.Errorf("руками много: %s", line[i:])
			}
		}
	}

	// «игрок» убирает печь №3; бот перезапускается и продолжает по сохранённому состоянию
	var third plan.Owned
	for _, o := range r.Saved.Owned {
		if o.Role == plan.LineRole("iron-ore", 3) {
			third = o
		}
	}
	st, _ := h.B.GetEntity(third.Name, third.Pos)
	if st == nil {
		t.Fatal("печи №3 нет")
	}
	if err := h.Pickup(ctxT(t, time.Minute), st.Entity); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r2 := newRunner(t, h, state, nil) // цели — из файла
	defer r2.Close()
	if ok, err := r2.Satisfied(); err != nil || ok {
		t.Fatalf("без печи №3 цель выполнена? %v %v", ok, err)
	}
	start = time.Now()
	if err := r2.Run(ctxT(t, 10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Logf("восстановление за %.0f с", time.Since(start).Seconds())
	lineOK(t, h, r2, 5)

	log, _ := os.ReadFile(path("run", "bot-test.log"))
	os.Remove(path("run", "bot-test.log"))
	for _, want := range []string{"почему:", "исчезла", "линия плавки"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("в журнале нет %q", want)
		}
	}
	clean(t, h)
}
