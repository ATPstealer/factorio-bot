package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// oilFile — раскладка стадии нефти (качалки и трасса).
const oilFile = "state-oil.json"

// oilReady — перед НПЗ: трасса нефти — как в плане. План, построенный до насоса на входе склада
// (cluster.md §2а «Нефть»), — конец трасс у склада заново через насос (cluster.InletPumps), своё
// убранное — снять; потом трасса целиком — достроить: недостроенное и стоящее не так (стадию
// нефти прервали посреди перестановки — подземная труба осталась поперёк, нефть не шла: стадия 36
// на 427965634). План и раскладка трассы — в файлы после стройки: сбой посреди — следующий заход
// посчитает то же заново (снятого нет, поставленное — построено).
func oilReady(ctx context.Context, h *act.Hands) error {
	data, err := os.ReadFile(runPath(planFile))
	if err != nil {
		return err
	}
	var plan ClusterPlan // как сохранён — без фильтров загрузки
	if err := json.Unmarshal(data, &plan); err != nil {
		return err
	}
	var tank *cluster.Cluster
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Tank && c.Product == "crude-oil" {
			tank = c
		}
	}
	if tank == nil {
		return nil // нефти в плане нет
	}
	var oil []layout.Placement
	if data, err := os.ReadFile(runPath(oilFile)); err == nil {
		if err := json.Unmarshal(data, &oil); err != nil {
			return err
		}
	}
	l := layout.New(h.Protos, h.World)
	was := map[layout.Placement]bool{}
	for _, pl := range plan.Layout {
		l.Add(pl)
		was[pl] = true
	}
	// построенное по плану — не препятствие (в модели мира — свои постройки: начало перекладки
	// «закрыто» своей же подземной трубой, стадия 36 на 427965634)
	objs := make([]world.Object, 0, len(plan.Layout))
	for _, pl := range plan.Layout {
		objs = append(objs, world.Object{Name: pl.Name, Pos: pl.Pos})
	}
	restore := h.World.Hide(objs)
	removed, err := cluster.InletPumps(l, plan.Clusters, tank)
	restore()
	if err != nil {
		return err
	}
	var added []layout.Placement
	for _, pl := range l.List {
		if !was[pl] {
			added = append(added, pl)
		}
	}
	if len(removed)+len(added) > 0 {
		h.Say("вход склада нефти — через насос: убрать %d, поставить %d", len(removed), len(added))
		var es []bridge.Entity
		for _, pl := range removed {
			if e, err := h.B.EntityAt(pl.Name, pl.Pos); err != nil {
				return err
			} else if e != nil {
				es = append(es, *e)
			}
		}
		if err := h.PickupAll(ctx, es); err != nil {
			return fmt.Errorf("снять конец трассы у склада: %w", err)
		}
		oil = append(slices.DeleteFunc(oil, func(pl layout.Placement) bool { return slices.Contains(removed, pl) }), added...)
	}
	if err := buildSorted(ctx, h, oil, blueOrder); err != nil {
		return fmt.Errorf("трасса нефти: %w", err)
	}
	if len(removed)+len(added) == 0 {
		return nil
	}
	plan.Layout, plan.Placed = l.List, len(l.List)
	if err := writeState(planFile, plan); err != nil {
		return err
	}
	return writeState(oilFile, oil)
}
