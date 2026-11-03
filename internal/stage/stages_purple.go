package stage

import (
	"context"
	"fmt"
	"os"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадии 44–48 (stages.md): стройка фиолетовой науки по одной части — как синяя
// (stages_blue.go, blueStage): исследования (поставлены на стадии 42, здесь — ждать), постройки
// плана этой стадии, стройка, проверка по факту.

// lastPurpleStage — последняя стадия стройки фиолетовой.
const lastPurpleStage = 48

// purpleStages — стадии 44–48 по номеру: их раскладка — план стадии для проверки перед стройкой
// и «нужного» (prepare.go planOf).
var purpleStages = map[int]blueStage{}

// inBuffer — item дошёл до буфера выхода (cluster.md §6а): есть хоть в одном сундуке буферов
// раскладки стадии (лента шины — до конца, за всеми потребителями).
func inBuffer(item string) func(h *act.Hands, list []layout.Placement) error {
	return func(h *act.Hands, list []layout.Placement) error {
		n := 0
		for _, b := range outBufs(h, list) {
			pl := b.chest
			n++
			if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && st.Inventories["chest"][item] > 0 {
				return nil
			}
		}
		return fmt.Errorf("%s нет ни в одном из %d буферов выхода", item, n)
	}
}

func init() {
	for _, s := range []blueStage{
		{n: 44, name: "электропечи", file: "state-electric-furnaces.json",
			wait: []string{"advanced-material-processing-2"}, fact: inBuffer("electric-furnace")},
		{n: 45, name: "модули продуктивности", file: "state-modules.json",
			wait: []string{"modules", "productivity-module"}, fact: inBuffer("productivity-module")},
		// пары «стержни → рельсы»; камень — сквозной лентой bus-1 → bus-2
		{n: 46, name: "рельсы", file: "state-rails.json", wait: []string{"railway"}, fact: inBuffer("rail")},
		{n: 47, name: "фиолетовая наука", file: "state-purple-science.json",
			wait: []string{purplePack}, fact: working("assembling-machine-1", "assembling-machine-2")}, // до стадии 50 — первый уровень
		// часть блока лабораторий под фиолетовую, отвод фиолетовой (cluster.md §2а «Лаборатории»);
		// исследования — и на фиолетовой, первыми (как стадия 41 — на синей)
		{n: 48, name: "фиолетовая в лаборатории", file: "state-labs-purple.json", fact: labsOn(purplePack),
			before: func(ctx context.Context, h *act.Hands) error {
				if err := os.WriteFile(runPath(purpleOnFile), []byte("{}\n"), 0o644); err != nil {
					return err
				}
				return queueResearch(ctx, h, purplePack)
			},
			after: func(ctx context.Context, h *act.Hands) error { return queueResearch(ctx, h) }},
	} {
		register(Stage{N: s.n, Name: s.name, Run: s.run, Check: s.check})
		purpleStages[s.n] = s
	}
}
