package stage

import (
	"context"
	"os"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Стадии 51–61 (stages.md): стройка жёлтой науки по одной части — той же таблицей, что синяя и
// фиолетовая (blueStage): исследования (поставлены на стадии 49, здесь — ждать), постройки плана
// этой стадии, стройка, проверка по факту.

// lastYellowStage — последняя стадия стройки жёлтой: весь план — в раскладках стадий.
const lastYellowStage = 61

// yellowStages — стадии 51–61 по номеру: их раскладка — план стадии для проверки перед стройкой
// и «нужного» (prepare.go planOf).
var yellowStages = map[int]blueStage{}

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
		{n: 51, name: "НПЗ продвинутой переработки", file: "state-refinery-adv.json",
			wait: []string{"advanced-oil-processing"},
			fact: allOf(working("oil-refinery"), holds("heavy-oil", "pipe", "pipe-to-ground"), holds("light-oil", "pipe", "pipe-to-ground"))},
		{n: 52, name: "крекинг тяжёлого и лёгкого масла", file: "state-cracking.json",
			wait: []string{"advanced-oil-processing"}, fact: chem},
		{n: 53, name: "смазка", file: "state-lubricant.json",
			wait: []string{"lubricant"}, fact: allOf(chem, holds("lubricant", "pipe", "pipe-to-ground"))},
		{n: 54, name: "серная кислота", file: "state-acid.json",
			fact: allOf(chem, holds("sulfuric-acid", "pipe", "pipe-to-ground"))},
		{n: 55, name: "батареи", file: "state-battery.json",
			wait: []string{"battery"}, fact: inBuffer("battery")},
		{n: 56, name: "электродвигатели", file: "state-electric-engines.json",
			wait: []string{"electric-engine"}, fact: inBuffer("electric-engine-unit")},
		{n: 57, name: "рамы роботов", file: "state-frames.json",
			wait: []string{"robotics"}, fact: inBuffer("flying-robot-frame")},
		{n: 58, name: "ЛПС", file: "state-lds.json",
			wait: []string{"low-density-structure"}, fact: inBuffer("low-density-structure")},
		{n: 59, name: "процессоры", file: "state-processing-units.json",
			wait: []string{"processing-unit"}, fact: inBuffer("processing-unit")},
		{n: 60, name: "жёлтая наука", file: "state-yellow-science.json",
			wait: []string{yellowPack}, fact: working("assembling-machine-1", "assembling-machine-2")},
		// часть блока лабораторий под жёлтую (cluster.md §2а «Лаборатории»); исследования — и на
		// жёлтой, первыми (как стадии 41 и 48)
		{n: 61, name: "жёлтая в лаборатории", file: "state-labs-yellow.json", fact: labsOn(yellowPack),
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
