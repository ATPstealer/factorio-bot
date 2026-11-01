package stage

import (
	"context"
	"os"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадии 50–61 (stages.md): стройка проекта жёлтой науки (стадия 7) по одной части — той же
// таблицей, что синяя и фиолетовая (blueStage): исследования (поставлены на
// стадии 49, здесь — ждать), постройки части из дописанного проектом (yellowAdded), стройка,
// проверка по факту. Все новые постройки — второго уровня (в плане — UpgradeNew, своё стадий —
// tier).

// lastYellowStage — последняя стадия стройки жёлтой: весь план — в раскладках стадий.
const lastYellowStage = 61

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

func init() {
	chem := working("chemical-plant")
	for _, s := range []blueStage{
		{n: 51, name: "НПЗ продвинутой переработки и склады масел", file: "state-refinery-adv.json",
			wait: []string{"advanced-oil-processing"}, pick: onStage(51),
			fact: allOf(working("oil-refinery"), holds("heavy-oil", "storage-tank"), holds("light-oil", "storage-tank"))},
		{n: 52, name: "крекинг тяжёлого масла", file: "state-cracking.json",
			wait: []string{"advanced-oil-processing"}, pick: onStage(52), fact: chem},
		{n: 53, name: "смазка", file: "state-lubricant.json",
			wait: []string{"lubricant"}, pick: onStage(53),
			fact: allOf(chem, holds("lubricant", "pipe", "pipe-to-ground"))},
		{n: 54, name: "серная кислота", file: "state-acid.json",
			pick: onStage(54), fact: allOf(chem, holds("sulfuric-acid", "storage-tank"))},
		{n: 55, name: "батареи", file: "state-battery.json",
			wait: []string{"battery"}, pick: onStage(55), fact: inBuffer("battery")},
		{n: 56, name: "электродвигатели", file: "state-electric-engines.json",
			wait: []string{"electric-engine"}, pick: onStage(56), fact: inBuffer("electric-engine-unit")},
		{n: 57, name: "рамы роботов", file: "state-frames.json",
			wait: []string{"robotics"}, pick: onStage(57), fact: inBuffer("flying-robot-frame")},
		{n: 58, name: "ЛПС", file: "state-lds.json",
			wait: []string{"low-density-structure"}, pick: onStage(58), fact: inBuffer("low-density-structure")},
		// сначала рост красных схем (процессорам), потом кластер процессоров (как стадия 44)
		{n: 59, name: "процессоры", file: "state-processing-units.json",
			wait: []string{"processing-unit"}, pick: onStage(59), fact: inBuffer("processing-unit")},
		{n: 60, name: "жёлтая наука", file: "state-yellow-science.json",
			wait: []string{yellowPack}, pick: onStage(60), fact: working("assembling-machine-2")},
		// жёлтая — на вторую полосу ленты C блока лабораторий (cluster.md §2а «Лаборатории»);
		// исследования — и на жёлтой, первыми (как стадии 41 и 48)
		{n: 61, name: "жёлтая в лаборатории", file: "state-labs-yellow.json",
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
