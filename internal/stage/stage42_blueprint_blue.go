package stage

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадия 42 (stages.md): обслуживание после синей науки — как стадия 28: в самом начале —
// исследования проекта фиолетовой в очередь по порядку; чертёж (план с синей и раскладки стадий
// 10–41) — обновить текущим кодом, сверка, лишнее — разобрать, буферы и ленты — очистить,
// инвентарь — в порядок. С 43-й все новые постройки — второго уровня (tier2).

func init() {
	register(Stage{N: 42, Name: "обслуживание: привести к чертежу синей", Run: runBlueprintBlue, Check: checkBlueprintBlue})
}

// firstTechs42 — в самом начале стадии 42 — в очередь по порядку (stages.md): постройки второго
// уровня (стальная печь, средний столб), потом электропечь, модули, рельсы, фиолетовый пакет.
var firstTechs42 = []string{"advanced-material-processing", "electric-energy-distribution-1",
	"advanced-material-processing-2", "modules", "productivity-module", "railway", purplePack}

func runBlueprintBlue(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, firstTechs42...); err != nil {
		return err
	}
	if err := refreshLayouts(h, 41); err != nil {
		return err
	}
	return maintain(ctx, h, func() ([]layout.Placement, error) { return blueprint(h, planBlueAll) }, true)
}

// checkBlueprintBlue — построенное по чертежу синей, лишнего нет (checkDrawing); исследования
// проекта — изучены или в очереди (очередь игры — 7: полная — доставятся по ходу).
func checkBlueprintBlue(h *act.Hands) error {
	if err := checkDrawing(h, planBlueAll); err != nil {
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

// planBlueAll — весь план стройки до фиолетовой: план до стадии 5 (как строили стадии 11–28,
// с будущей добычей — стадия 30) и дописанное проектом синей (29–41).
func planBlueAll(h *act.Hands) ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	added, _, err := blueAdded()
	if err != nil {
		return nil, err
	}
	return append(slices.Clone(plan.Layout), added...), nil
}
