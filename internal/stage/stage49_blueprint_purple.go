package stage

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадия 49 (stages.md): обслуживание после фиолетовой науки — как стадии 28 и 42: в самом
// начале — исследования проекта жёлтой в очередь по порядку; чертёж (план с фиолетовой и
// раскладки стадий 10–48) — обновить текущим кодом, сверка, недостающее — достроить, лишнее —
// разобрать, буферы и ленты — очистить, инвентарь — в порядок. Чужая жидкость в сетях — общее
// для всех стадий (stage.Do).

func init() {
	register(Stage{N: 49, Name: "обслуживание: привести к чертежу фиолетовой", Run: runBlueprintPurple, Check: checkBlueprintPurple})
}

// firstTechs49 — в самом начале стадии 49 — в очередь по порядку (stages.md): продвинутая
// переработка, смазка, батареи, электродвигатели, рамы роботов, ЛПС, процессоры, жёлтый пакет.
var firstTechs49 = []string{"advanced-oil-processing", "lubricant", "battery", "electric-engine", "robotics",
	"low-density-structure", "processing-unit", yellowPack}

func runBlueprintPurple(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, firstTechs49...); err != nil {
		return err
	}
	if err := refreshLayouts(h, lastPurpleStage); err != nil {
		return err
	}
	return maintain(ctx, h, func() ([]layout.Placement, error) { return blueprint(h, planPurpleAll) }, true)
}

// checkBlueprintPurple — построенное по чертежу фиолетовой, лишнего нет (checkDrawing);
// исследования проекта жёлтой — изучены или в очереди (очередь игры — 7: полная — доставятся по
// ходу).
func checkBlueprintPurple(h *act.Hands) error {
	if err := checkDrawing(h, planPurpleAll); err != nil {
		return err
	}
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	var miss []string
	for _, t := range firstTechs49 {
		if !slices.Contains(f.Researched, t) && f.Current != t && !slices.Contains(f.Queue, t) && techReady(h, f, t) {
			miss = append(miss, t)
		}
	}
	if len(miss) > 0 && len(f.Queue) < 7 {
		return fmt.Errorf("исследования проекта не в очереди: %s", strings.Join(miss, ", "))
	}
	return nil
}

// planPurpleAll — весь план стройки до жёлтой: чертёж синей (planBlueAll) и дописанное проектом
// фиолетовой (43–48); постройка под делителем или подземкой проекта — не чертёж (overlay).
func planPurpleAll(h *act.Hands) ([]layout.Placement, error) {
	base, err := planBlueAll(h)
	if err != nil {
		return nil, err
	}
	added, _, err := purpleAdded()
	if err != nil {
		return nil, err
	}
	return overlay(h, base, added), nil
}
