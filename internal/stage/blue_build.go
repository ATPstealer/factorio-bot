package stage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Общее для стройки стадий по плану всей базы: постройки плана этой стадии (plan_stages.go:
// кластеры стадии, их логистика и части, ленты и трубы шин, которые стадия начинает);
// исследования — в очередь заранее и ждать.

// addedFor — постройки плана стадии n (stageAdded), делители плана в разрывах лент шин
// (withSplitters), столбы к сети базы; один раз на карту (file).
func addedFor(h *act.Hands, file string, n int) ([]layout.Placement, error) {
	list, err := onceLayout(file, func() ([]layout.Placement, error) {
		plan, err := LoadPlan()
		if err != nil {
			return nil, err
		}
		res := stageAdded(plan, n, n)
		var allow []string // площади кластеров стадии — столбам можно
		for _, c := range plan.Clusters {
			if c.Kind != cluster.Bus && clusterStage(c) == n {
				allow = append(allow, c.Name)
			}
		}
		if len(res) == 0 {
			return nil, fmt.Errorf("в плане нет построек стадии %d", n)
		}
		poles, err := connectPower(h, plan, res, allow)
		return append(res, poles...), err
	})
	if err != nil {
		return nil, err
	}
	// лента шины — без разрывов: делитель будущего отвода на ней — сейчас, иначе предмет дальше
	// него не идёт (стадия 31 на 859088572: лента стали рвалась под отводом двигателей стадии 38)
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	return uniq(withSplitters(h, plan, list)), nil
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
			if errors.Is(err, act.ErrResearchQueueFull) {
				return nil // полна — остальные доставят следующие стадии (решение пользователя)
			}
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
	tried := map[string]bool{} // уже ставили первым: снова — не каждые 10 с
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
		// текущее — не нужное и не его предпосылка: нужное — первым (stages.md, общее:
		// исследования в начале каждой стадии; стадия 51 на 2448886157 — текущее на фиолетовой,
		// а фиолетовой нет); первое в пути — то, чьи предпосылки изучены
		done := map[string]bool{}
		for _, t := range f.Researched {
			done[t] = true
		}
		path, err := h.Protos.TechPath(miss, done)
		if err != nil {
			return err
		}
		if !slices.Contains(path, f.Current) {
			for _, t := range path {
				tech := h.Protos.Technologies[t]
				if tried[t] || tech.Trigger != nil || slices.ContainsFunc(tech.Prerequisites, func(p string) bool { return !done[p] }) {
					continue
				}
				tried[t] = true
				if err := h.ResearchFirst(ctx, t); err != nil {
					if ctx.Err() != nil {
						return err
					}
					h.Log("исследование %s первым не встало: %v", t, err)
				}
				break
			}
		}
		// не встало в очередь (была полна) — доставить, как только там место
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
		if err := waitHooks(ctx, h); err != nil { // в ожидании — наука на ракету, лаборатории (stages.md, общее)
			return err
		}
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
}
