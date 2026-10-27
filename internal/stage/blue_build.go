package stage

import (
	"context"
	"fmt"
	"slices"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Общее для стройки проекта синей науки (стадии 29–41): постройки, которые стадия 5 дописала в
// план, — по кластерам стадии; исследования — в очередь заранее и ждать.

// blueAddedFor — дописанное стадией 7 для кластеров, которые выбирает pick: их постройки и
// логистика (ленты, трубы, отводы), ленты шин их продуктов (и делители на шинах, если splitters),
// столбы к сети базы; один раз на карту (file).
func blueAddedFor(h *act.Hands, file string, pick func(*cluster.Cluster) bool, splitters bool,
	more ...func(plan *ClusterPlan, all *layout.Layout) ([]layout.Placement, error)) ([]layout.Placement, error) {
	return onceLayout(file, func() ([]layout.Placement, error) {
		added, plan, err := blueAdded()
		if err != nil {
			return nil, err
		}
		tags := map[string]bool{}
		items := map[string]bool{}
		var allow []string
		buses := busNames(plan)
		for _, c := range plan.Clusters {
			if c.Kind == cluster.Bus || !pick(c) {
				continue
			}
			tags[cluster.Tag(c.Name)], tags[cluster.LogisticsTag+"/"+c.Name] = true, true
			for _, bus := range buses {
				tags[cluster.LogisticsTag+"/"+bus+"/"+c.Name] = true
			}
			items[c.Product] = true
			for _, o := range c.Out {
				items[o.Item] = true
			}
			allow = append(allow, c.Name)
		}
		var res []layout.Placement
		for _, pl := range added {
			onBus := slices.ContainsFunc(buses, func(b string) bool { return pl.Tag == cluster.Tag(b) }) &&
				(items[pl.Recipe] || splitters && pl.Name == "splitter")
			if tags[pl.Tag] || onBus {
				res = append(res, pl)
			}
		}
		if len(more) > 0 { // своё стадии (будущая добыча, буферы выхода…) — по всему плану
			all := layout.New(h.Protos, h.World)
			for _, pl := range plan.Layout {
				all.Add(pl)
			}
			for _, m := range more {
				add, err := m(plan, all)
				if err != nil {
					return nil, err
				}
				res = append(res, add...)
			}
			for _, c := range plan.Clusters {
				if c.Later {
					allow = append(allow, c.Name)
				}
			}
		}
		if len(res) == 0 {
			return nil, fmt.Errorf("в плане нет этих построек (%v)", allow)
		}
		poles, err := connectPower(h, plan, res, allow)
		return append(res, poles...), err
	})
}

// queueTechs — исследования techs — в очередь, если ещё не изучены и не стоят.
func queueTechs(ctx context.Context, h *act.Hands, techs ...string) error {
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	for _, t := range techs {
		if slices.Contains(f.Researched, t) || f.Current == t || slices.Contains(f.Queue, t) {
			continue
		}
		if err := h.StartResearch(ctx, t); err != nil {
			h.Say("исследование %s в очередь не встало: %v", t, err)
		}
	}
	return nil
}

// waitResearch — ждать, пока techs изучены (не стоят в очереди — поставить).
func waitResearch(ctx context.Context, h *act.Hands, techs ...string) error {
	if err := queueTechs(ctx, h, techs...); err != nil {
		return err
	}
	said := false
	for {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		var miss, out []string // не изучены; из них не в очереди
		for _, t := range techs {
			if !slices.Contains(f.Researched, t) {
				miss = append(miss, t)
				if f.Current != t && !slices.Contains(f.Queue, t) {
					out = append(out, t)
				}
			}
		}
		if len(miss) == 0 {
			return nil
		}
		// не встало в очередь (была полна — stages.md, общее: исследования в начале каждой
		// стадии) — доставить, как только там место
		busy := len(f.Queue)
		if f.Current != "" && !slices.Contains(f.Queue, f.Current) {
			busy++
		}
		if len(out) > 0 && busy < 7 {
			if err := queueTechs(ctx, h, out...); err != nil {
				return err
			}
		}
		if !said {
			h.Say("жду исследований: %v", miss)
			said = true
		}
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
}
