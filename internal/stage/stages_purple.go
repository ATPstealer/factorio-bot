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

// purpleAdded — дописанное стадией 6 (весь план минус план до неё) и весь план.
func purpleAdded() ([]layout.Placement, *ClusterPlan, error) {
	plan, err := LoadPurplePlan()
	if err != nil {
		return nil, nil, err
	}
	base, err := loadPlanFile(runPath(purpleBaseFile))
	if err != nil {
		return nil, nil, fmt.Errorf("нет плана до стадии 6: %w", err)
	}
	was := map[layout.Placement]bool{}
	for _, pl := range base.Layout {
		was[pl] = true
	}
	var res []layout.Placement
	for _, pl := range plan.Layout {
		if !was[pl] {
			res = append(res, pl)
		}
	}
	return res, plan, nil
}

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

// fresh — кластер дописан проектом фиолетовой (его нет в плане до стадии 6); имена старых —
// из файла один раз (buffers зовётся на каждый кластер).
var fresh = func() func(*cluster.Cluster) bool {
	var old map[string]bool
	return func(c *cluster.Cluster) bool {
		if old == nil {
			old = map[string]bool{}
			if base, err := loadPlanFile(runPath(purpleBaseFile)); err == nil {
				for _, o := range base.Clusters {
					old[o.Name] = true
				}
			}
		}
		return len(old) > 0 && !old[c.Name]
	}
}()

// purpleLanes — ленты шин, дописанные проектом фиолетовой (новые ленты кирпича, электропечей,
// модулей, рельсов, фиолетовой, продления, их подземки и делители) — все сразу на стадии 43,
// как стадия 30 для синей: подземки лент шин в раскладку стадии иначе не попадают (в Recipe у
// них «вход/выход», не предмет), пустые ленты подождут своих кластеров.
func purpleLanes(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error) {
	return func(plan *ClusterPlan, _ *layout.Layout) ([]layout.Placement, error) {
		added, _, err := purpleAdded()
		if err != nil {
			return nil, err
		}
		buses := map[string]bool{}
		for _, b := range busNames(plan) {
			buses[cluster.Tag(b)] = true
		}
		var res []layout.Placement
		for _, pl := range added {
			if buses[pl.Tag] {
				res = append(res, pl)
			}
		}
		return res, nil
	}
}

func init() {
	for _, s := range []blueStage{
		// рост под фиолетовую (станция первой — cluster.md §7; плавка, добыча, сборка, кроме
		// красных схем — они до электропечей, стадия 44), ленты шин проекта с предметами этих
		// кластеров (кирпич — в новую ленту bus-2, сквозная камня bus-1 → bus-2), буфер кирпича —
		// в конце ленты bus-2 (старый, на выходной ленте кластера, снимается — staleBuffers)
		{n: 43, name: "кирпич на шину и рост под фиолетовую", file: "state-brick.json", src: purpleSource,
			pick: func(c *cluster.Cluster) bool {
				switch c.Kind {
				case cluster.Power, cluster.Smelt, cluster.Mine:
					return true
				case cluster.Make:
					_, p := purpleProducts[c.Product]
					return !p && c.Product != "advanced-circuit"
				}
				return false
			},
			// буферы — у кирпича (переезжает на bus-2) и новых кластеров роста; у старых плавка
			// держит буферы на выходной ленте (стадия 17), сборка — в конце своих лент (как есть)
			buffers: func(c *cluster.Cluster) bool {
				return c.Kind == cluster.Smelt && c.Product == "stone-brick" || fresh(c)
			},
			more:  purpleLanes,
			built: fuelBoilers, // новые бойлеры при запуске — углём руками (как на стадии 29)
			fact:  inBuffer("stone-brick")},
		// сначала рост красных схем (под синюю и фиолетовую вместе), потом кластер электропечей
		{n: 44, name: "электропечи", file: "state-electric-furnaces.json", src: purpleSource,
			wait: []string{"advanced-material-processing-2"},
			pick: func(c *cluster.Cluster) bool { return product("advanced-circuit")(c) || product("electric-furnace")(c) },
			fact: inBuffer("electric-furnace")},
		{n: 45, name: "модули продуктивности", file: "state-modules.json", src: purpleSource,
			wait: []string{"modules", "productivity-module"}, pick: product("productivity-module"),
			fact: inBuffer("productivity-module")},
		// пары «стержни → рельсы»; камень — сквозной лентой bus-1 → bus-2 (вход стадии — throughLanes)
		{n: 46, name: "рельсы", file: "state-rails.json", src: purpleSource, wait: []string{"railway"},
			pick: product("rail"), fact: inBuffer("rail")},
		{n: 47, name: "фиолетовая наука", file: "state-purple-science.json", src: purpleSource,
			wait: []string{purplePack}, pick: product(purplePack), fact: working("assembling-machine-2")},
		// лента C блока лабораторий, её инсертеры, отвод фиолетовой с bus-4 (cluster.md §2а
		// «Лаборатории»); исследования — и на фиолетовой, первыми (как стадия 41 — на синей)
		{n: 48, name: "фиолетовая в лаборатории", file: "state-labs-purple.json", src: purpleSource,
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
