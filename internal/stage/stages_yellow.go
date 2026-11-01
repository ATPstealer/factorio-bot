package stage

import (
	"context"
	"os"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадии 50–61 (stages.md): стройка проекта жёлтой науки (стадия 7) по одной части — той же
// таблицей, что синяя и фиолетовая (blueStage, src: yellowSource): исследования (поставлены на
// стадии 49, здесь — ждать), постройки части из дописанного проектом (yellowAdded), стройка,
// проверка по факту. Все новые постройки — второго уровня (в плане — UpgradeNew, своё стадий —
// tier).

// lastYellowStage — последняя стадия стройки жёлтой: весь план — в раскладках стадий.
const lastYellowStage = 61

// yellowSource — проект жёлтой науки: дописанное стадией 7 и весь план.
var yellowSource = planSource{7, yellowAdded, LoadWholePlan}

// yellowStages — стадии 50–61 по номеру: их раскладка — план стадии для проверки перед стройкой
// и «нужного» (prepare.go planOf).
var yellowStages = map[int]blueStage{}

// yellowProducts — продукты новых кластеров проекта и стадии их стройки (крекинг тяжёлого даёт
// лёгкое, НПЗ‑2 — тяжёлое первым продуктом).
var yellowProducts = map[string]int{"heavy-oil": 51, "light-oil": 52, "lubricant": 53, "sulfuric-acid": 54,
	"battery": 55, "electric-engine-unit": 56, "flying-robot-frame": 57, "low-density-structure": 58,
	"processing-unit": 59, yellowPack: 60}

// yellowStageOf — стадия, которая строит кластер c проекта жёлтой (0 — рост, стадия 50):
// склады масел — с НПЗ‑2 (51), склад кислоты — с кислотой (54), рост красных схем — с
// процессорами (59: им красные схемы, как электропечам на стадии 44).
func yellowStageOf(c *cluster.Cluster) int {
	switch c.Kind {
	case cluster.Tank:
		switch c.Product {
		case "heavy-oil", "light-oil":
			return 51
		case "sulfuric-acid":
			return 54
		}
	case cluster.Make:
		if n, ok := yellowProducts[c.Product]; ok {
			return n
		}
		if c.Product == "advanced-circuit" {
			return 59
		}
	}
	return 0
}

// onStage — кластеры, которые строит стадия n.
func onStage(n int) func(*cluster.Cluster) bool {
	return func(c *cluster.Cluster) bool { return yellowStageOf(c) == n }
}

// yellowAdded — постройки плана стадий жёлтой науки (50–61) и весь план.
func yellowAdded() ([]layout.Placement, *ClusterPlan, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	return stageAdded(plan, 50, 61), plan, nil
}

// yellowFresh — кластер стадий жёлтой науки (50–61).
func yellowFresh(c *cluster.Cluster) bool { return c.Stage >= 50 && c.Stage <= 61 }

// yellowLanes — ленты и трубы шин, дописанные проектом жёлтой (новые ленты, трубы масел, смазки,
// кислоты, их отводы и насосы, продления старых, подземки и делители) — все сразу на стадии 50,
// как стадии 30 и 43: пустые подождут своих кластеров.
func yellowLanes(h *act.Hands) func(*ClusterPlan, *layout.Layout) ([]layout.Placement, error) {
	return func(plan *ClusterPlan, _ *layout.Layout) ([]layout.Placement, error) {
		added, _, err := yellowAdded()
		if err != nil {
			return nil, err
		}
		buses := busNames(plan)
		var res []layout.Placement
		for _, pl := range added {
			for _, b := range buses {
				if t := cluster.Tag(b); pl.Tag == t || strings.HasPrefix(pl.Tag, t+"/") {
					res = append(res, pl)
					break
				}
			}
		}
		// делители плана в разрывах этих лент — с лентами (как стадия 43)
		return withSplitters(h, plan, res), nil
	}
}

// allOf — все проверки по факту подтвердились (первая неудачная — причина).
func allOf(facts ...func(*act.Hands, []layout.Placement) error) func(*act.Hands, []layout.Placement) error {
	return func(h *act.Hands, list []layout.Placement) error {
		for _, f := range facts {
			if err := f(h, list); err != nil {
				return err
			}
		}
		return nil
	}
}

// newOil — новые качалки стадии (третье месторождение) дают нефть; новых нет — подтверждено.
func newOil(h *act.Hands, list []layout.Placement) error {
	for _, pl := range list {
		if pl.Name == "pumpjack" {
			return holds("crude-oil", "pumpjack")(h, list)
		}
	}
	return nil
}

func init() {
	chem := working("chemical-plant")
	for _, s := range []blueStage{
		// рост под жёлтую (станция первой — cluster.md §7; плавка, добыча, нефть, сборка), все
		// ленты и трубы шин проекта; кластеры проекта и рост красных схем — их стадии (51–60)
		{n: 50, name: "рост под жёлтую", file: "state-grow-yellow.json", src: yellowSource,
			pick: func(c *cluster.Cluster) bool {
				switch c.Kind {
				case cluster.Power, cluster.Smelt, cluster.Mine:
					return true
				case cluster.Make:
					return yellowStageOf(c) == 0
				}
				return false
			},
			buffers: func(c *cluster.Cluster) bool { return yellowFresh(c) && yellowStageOf(c) == 0 },
			more:    yellowLanes,
			built:   fuelBoilers, // новые бойлеры при запуске — углём руками (как на стадиях 29 и 43)
			fact:    newOil},
		{n: 51, name: "НПЗ продвинутой переработки и склады масел", file: "state-refinery-adv.json", src: yellowSource,
			wait: []string{"advanced-oil-processing"}, pick: onStage(51),
			fact: allOf(working("oil-refinery"), holds("heavy-oil", "storage-tank"), holds("light-oil", "storage-tank"))},
		{n: 52, name: "крекинг тяжёлого масла", file: "state-cracking.json", src: yellowSource,
			wait: []string{"advanced-oil-processing"}, pick: onStage(52), fact: chem},
		{n: 53, name: "смазка", file: "state-lubricant.json", src: yellowSource,
			wait: []string{"lubricant"}, pick: onStage(53),
			fact: allOf(chem, holds("lubricant", "pipe", "pipe-to-ground"))},
		{n: 54, name: "серная кислота", file: "state-acid.json", src: yellowSource,
			pick: onStage(54), fact: allOf(chem, holds("sulfuric-acid", "storage-tank"))},
		{n: 55, name: "батареи", file: "state-battery.json", src: yellowSource,
			wait: []string{"battery"}, pick: onStage(55), fact: inBuffer("battery")},
		{n: 56, name: "электродвигатели", file: "state-electric-engines.json", src: yellowSource,
			wait: []string{"electric-engine"}, pick: onStage(56), fact: inBuffer("electric-engine-unit")},
		{n: 57, name: "рамы роботов", file: "state-frames.json", src: yellowSource,
			wait: []string{"robotics"}, pick: onStage(57), fact: inBuffer("flying-robot-frame")},
		{n: 58, name: "ЛПС", file: "state-lds.json", src: yellowSource,
			wait: []string{"low-density-structure"}, pick: onStage(58), fact: inBuffer("low-density-structure")},
		// сначала рост красных схем (процессорам), потом кластер процессоров (как стадия 44)
		{n: 59, name: "процессоры", file: "state-processing-units.json", src: yellowSource,
			wait: []string{"processing-unit"}, pick: onStage(59), fact: inBuffer("processing-unit")},
		{n: 60, name: "жёлтая наука", file: "state-yellow-science.json", src: yellowSource,
			wait: []string{yellowPack}, pick: onStage(60), fact: working("assembling-machine-2")},
		// жёлтая — на вторую полосу ленты C блока лабораторий (cluster.md §2а «Лаборатории»);
		// исследования — и на жёлтой, первыми (как стадии 41 и 48)
		{n: 61, name: "жёлтая в лаборатории", file: "state-labs-yellow.json", src: yellowSource,
			pick: func(c *cluster.Cluster) bool { return c.Kind == cluster.Lab }, fact: labsOn(yellowPack),
			before: func(ctx context.Context, h *act.Hands) error {
				if err := os.WriteFile(runPath(yellowOnFile), []byte("{}\n"), 0o644); err != nil {
					return err
				}
				return queueResearch(ctx, h, yellowPack)
			},
			after: func(ctx context.Context, h *act.Hands) error { return queueResearch(ctx, h) }},
	} {
		register(Stage{N: s.n, Name: s.name, Run: s.run, Check: s.check})
		yellowStages[s.n] = s
	}
}
