package stage

import (
	"context"
	"fmt"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 31 (stages.md): сталь — кластер стали из плана стадии 5 (пары печей «пластины → сталь»,
// уголь второй лентой), отводы руды и угля с bus-1, лента стали bus-2 с буфером выхода, столбы.
// Исследование стали — поставлено на стадии 30; здесь — ждать, пока изучено.

const (
	steelFile = "state-steel.json" // раскладка стадии: один раз на карту
	steelTech = "steel-processing"
)

func init() {
	register(Stage{N: 31, Name: "сталь", Run: runSteel, Check: checkSteel})
}

func steelLayout(h *act.Hands) ([]layout.Placement, error) {
	pick := func(c *cluster.Cluster) bool { return c.IsSteel() }
	return blueAddedFor(h, steelFile, pick, false, endBuffers(h, pick)) // буфер стали для бота (cluster.md §6а)
}

func runSteel(ctx context.Context, h *act.Hands) error {
	if err := queueTechs(ctx, h, "fast-inserter"); err != nil { // на стадию 32
		return err
	}
	if err := waitResearch(ctx, h, steelTech); err != nil {
		return err
	}
	list, err := steelLayout(h)
	if err != nil {
		return err
	}
	h.Say("сталь: %d построек по плану стадии 5", len(list))
	order := []string{"stone-furnace", chest, "inserter", "long-handed-inserter", "transport-belt",
		"underground-belt", "splitter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	// по факту — подождать: проверка сразу после последней постройки шла раньше, чем железо
	// дошло до печей (стадия 31 на 260647970)
	return waitFact(ctx, h, list, steelFact)
}

// checkSteel — всё из раскладки стоит, сталь идёт (steelFact).
func checkSteel(h *act.Hands) error {
	list, err := steelLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	return steelFact(h, list)
}

// steelFact — в печах стали — сталь или пластины (идёт работа), в буфере в конце ленты стали —
// сталь (лента без разрывов: под делителем будущего отвода рвалась — стадия 33 на 859088572).
func steelFact(h *act.Hands, list []layout.Placement) error {
	got := false
	for _, pl := range list {
		if pl.Name != "stone-furnace" || pl.Recipe != "steel-plate" || got {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		for _, inv := range st.Inventories {
			if inv["steel-plate"] > 0 || inv["iron-plate"] > 0 {
				got = true
			}
		}
	}
	if !got {
		return fmt.Errorf("печи стали ещё ничего не получили")
	}
	for _, pl := range list {
		if !strings.HasSuffix(pl.Name, "chest") || !strings.HasPrefix(pl.Tag, "outbuf/") {
			continue
		}
		if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && st.Inventories["chest"]["steel-plate"] > 0 {
			return nil
		}
	}
	return fmt.Errorf("в буфере в конце ленты стали стали нет")
}
