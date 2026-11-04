package stage

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 42 (stages.md): обслуживание после синей науки — как стадия 28: в самом начале —
// исследования фиолетовой в очередь по порядку; чертёж (постройки плана стадий до 42) — обновить
// текущим кодом, сверка, лишнее — разобрать, буферы и ленты — очистить, инвентарь — в порядок.

func init() {
	register(Stage{N: 42, Name: "обслуживание: привести к чертежу синей", Run: runBlueprintBlue, Check: checkBlueprintBlue})
}

// firstTechs42 — в самом начале стадии 42 — в очередь по порядку (stages.md): стальная печь
// (второй уровень на этой стадии), потом электропечь, модули, рельсы, фиолетовый пакет.
var firstTechs42 = []string{"advanced-material-processing",
	"advanced-material-processing-2", "modules", "productivity-module", "railway", purplePack}

func runBlueprintBlue(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, firstTechs42...); err != nil {
		return err
	}
	if err := refreshLayouts(h, 41); err != nil {
		return err
	}
	// печи и сборщики — второго уровня, ряды шины, которым жёлтой мало, — быстрыми (stages.md,
	// стадия 42): до сверки — первые на их местах не лишнее
	if err := tierStage(ctx, h, cluster.Tier2From, "advanced-material-processing", "automation-2"); err != nil {
		return err
	}
	return maintain(ctx, h, func() ([]layout.Placement, error) { return blueprint(h, planBefore(42)) }, true)
}

// checkBlueprintBlue — построенное по чертежу синей, лишнего нет (checkDrawing); исследования
// проекта — изучены или в очереди (очередь игры — 7: полная — доставятся по ходу).
func checkBlueprintBlue(h *act.Hands) error {
	// и жёлтых лент в рядах шины, которым жёлтой мало (cluster.md §5)
	if err := noBaseTier(h, cluster.Tier2From, "stone-furnace", "assembling-machine-1",
		"transport-belt", "underground-belt", "splitter"); err != nil {
		return err
	}
	if err := checkDrawing(h, planBefore(42)); err != nil {
		return err
	}
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	var miss []string
	for _, t := range firstTechs42 {
		if !slices.Contains(f.Researched, t) && f.Current != t && !slices.Contains(f.Queue, t) && techReady(h, f, t) {
			miss = append(miss, t)
		}
	}
	if len(miss) > 0 && len(f.Queue) < 7 {
		return fmt.Errorf("исследования проекта не в очереди: %s", strings.Join(miss, ", "))
	}
	return nil
}
