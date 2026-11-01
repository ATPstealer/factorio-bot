package stage

import (
	"context"
	"fmt"
	"os"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадии 43–48 (stages.md): стройка проекта фиолетовой науки (стадия 6) по одной части — как
// синяя (stages_blue.go, blueStage): исследования (поставлены на стадии 42, здесь — ждать),
// постройки части из дописанного проектом (purpleAdded), стройка, проверка по факту. Все новые
// постройки — второго уровня: в плане — cluster.UpgradeNew, своё стадий (буферы, столбы) — tier.

// lastPurpleStage — последняя стадия стройки фиолетовой: весь план — в раскладках стадий.
const lastPurpleStage = 48

// purpleStages — стадии 43–48 по номеру: их раскладка — план стадии для проверки перед стройкой
// и «нужного» (prepare.go planOf).
var purpleStages = map[int]blueStage{}

// purpleProducts — продукты новых кластеров проекта и стадии их стройки.
var purpleProducts = map[string]int{"electric-furnace": 44, "productivity-module": 45, "rail": 46, purplePack: 47}

// inBuffer — item дошёл до буфера выхода (cluster.md §6а): есть хоть в одном сундуке буферов
// раскладки стадии (лента шины — до конца, за всеми потребителями).
func inBuffer(item string) func(h *act.Hands, list []layout.Placement) error {
	return func(h *act.Hands, list []layout.Placement) error {
		n := 0
		for _, pl := range list {
			if !strings.HasPrefix(pl.Tag, "outbuf/") || !strings.HasSuffix(pl.Name, "chest") {
				continue
			}
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
		// сначала рост красных схем (под синюю и фиолетовую вместе), потом кластер электропечей
		{n: 44, name: "электропечи", file: "state-electric-furnaces.json",
			wait: []string{"advanced-material-processing-2"},
			pick: func(c *cluster.Cluster) bool { return product("advanced-circuit")(c) || product("electric-furnace")(c) },
			fact: inBuffer("electric-furnace")},
		{n: 45, name: "модули продуктивности", file: "state-modules.json",
			wait: []string{"modules", "productivity-module"}, pick: product("productivity-module"),
			fact: inBuffer("productivity-module")},
		// пары «стержни → рельсы»; камень — сквозной лентой bus-1 → bus-2 (вход стадии — throughLanes)
		{n: 46, name: "рельсы", file: "state-rails.json", wait: []string{"railway"},
			pick: product("rail"), fact: inBuffer("rail")},
		{n: 47, name: "фиолетовая наука", file: "state-purple-science.json",
			wait: []string{purplePack}, pick: product(purplePack), fact: working("assembling-machine-2")},
		// лента C блока лабораторий, её инсертеры, отвод фиолетовой с bus-4 (cluster.md §2а
		// «Лаборатории»); исследования — и на фиолетовой, первыми (как стадия 41 — на синей)
		{n: 48, name: "фиолетовая в лаборатории", file: "state-labs-purple.json",
			pick: func(c *cluster.Cluster) bool { return c.Kind == cluster.Lab }, fact: labsOn(purplePack),
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
