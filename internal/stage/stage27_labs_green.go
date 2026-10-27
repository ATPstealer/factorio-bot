package stage

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 27 (stages.md): второй вход кластера лабораторий — отвод зелёной науки с шины конечных
// (по плану стадии 4: подача снизу ленты A, свой буфер), фильтры буфера зелёного и отложенные
// фильтры стадии 21; в очередь — все нужные для ракеты на красной и зелёной. Критерий —
// исследование на красной и зелёной науке идёт само.

const labsGreenFile = "state-labs-green.json"

func init() {
	register(Stage{N: 27, Name: "зелёная наука в лаборатории", Run: runLabsGreen, Check: checkLabsGreen})
}

// labsGreenLayout — дописанное стадией 6 в кластер лабораторий и его отводы, лента зелёной науки
// шины конечных с подводом, делители, столбы; один раз на карту (файл).
func labsGreenLayout(h *act.Hands) ([]layout.Placement, error) {
	return onceLayout(labsGreenFile, func() ([]layout.Placement, error) {
		added, plan, err := greenAdded()
		if err != nil {
			return nil, err
		}
		lab, green := clusterOf(plan, cluster.Lab, ""), clusterOf(plan, cluster.Make, greenPack)
		if lab == nil || green == nil {
			return nil, fmt.Errorf("в плане нет лабораторий или зелёной науки")
		}
		var res []layout.Placement
		for _, pl := range added {
			if pl.Tag == cluster.Tag(lab.Name) || pl.Tag == cluster.LogisticsTag+"/"+lab.Name {
				res = append(res, pl)
			}
		}
		res = withSplitters(h, plan, append(res, busParts(plan, []string{greenPack}, []string{green.Name})...))
		poles, err := connectPower(h, plan, res, []string{lab.Name})
		return append(res, poles...), err
	})
}

// greenTechs — доступные (предпосылки изучены), не изученные исследования на красной и зелёной
// науке; сначала нужные для ракеты, потом самые дорогие.
func greenTechs(h *act.Hands) ([]string, error) {
	f, err := h.B.Force()
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, t := range f.Researched {
		done[t] = true
	}
	path, err := h.Protos.TechPath([]string{"rocket-silo"}, done)
	if err != nil {
		return nil, err
	}
	var cands []string
	for name, t := range h.Protos.Technologies {
		if t.Hidden || !t.Enabled || t.Trigger != nil || done[name] || len(t.UnitIngredients) != 2 {
			continue
		}
		ok := true
		for _, in := range t.UnitIngredients {
			ok = ok && (in.Name == redPack || in.Name == greenPack)
		}
		for _, p := range t.Prerequisites {
			ok = ok && done[p]
		}
		if ok {
			cands = append(cands, name)
		}
	}
	cost := func(n string) float64 { t := h.Protos.Technologies[n]; return t.UnitCount * t.UnitTime }
	sort.Slice(cands, func(i, j int) bool {
		ri, rj := slices.Contains(path, cands[i]), slices.Contains(path, cands[j])
		if ri != rj {
			return ri
		}
		if cost(cands[i]) != cost(cands[j]) {
			return cost(cands[i]) > cost(cands[j])
		}
		return cands[i] < cands[j]
	})
	return cands, nil
}

// usesGreen — исследование t требует зелёную науку.
func usesGreen(h *act.Hands, t string) bool {
	for _, in := range h.Protos.Technologies[t].UnitIngredients {
		if in.Name == greenPack {
			return true
		}
	}
	return false
}

func runLabsGreen(ctx context.Context, h *act.Hands) error {
	list, err := labsGreenLayout(h)
	if err != nil {
		return err
	}
	order := []string{chest, "inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	// фильтры буфера зелёного и отложенные фильтры лабораторий стадии 21 (зелёные пакеты до
	// исследования в окне фильтра не выбрать — 2.1)
	labs, err := labsLayout(h)
	if err != nil {
		return err
	}
	if err := configure(ctx, h, append(slices.Clone(list), labs...)); err != nil {
		return err
	}
	// зелёные пошли — в очередь сразу все нужные для ракеты на красной и зелёной (stages.md);
	// дальше это делает и обслуживание (greenOnFile)
	if err := os.WriteFile(runPath(greenOnFile), []byte("{}\n"), 0o644); err != nil {
		return err
	}
	start := func() error {
		if err := queueResearch(ctx, h); err != nil {
			return err
		}
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		for _, t := range append([]string{f.Current}, f.Queue...) {
			if t != "" && usesGreen(h, t) {
				return nil
			}
		}
		// нужных для ракеты на зелёной сейчас нет в очереди — первое доступное
		techs, err := greenTechs(h)
		if err != nil {
			return err
		}
		if len(techs) == 0 {
			return fmt.Errorf("нет доступных исследований на красной и зелёной науке")
		}
		return h.StartResearch(ctx, techs[0])
	}
	if err := start(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Minute)
	for checkLabsGreen(h) != nil && time.Now().Before(deadline) {
		if err := start(); err != nil { // изучено — доставить следующие
			return err
		}
		if err := sleepCtx(ctx, 20*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// checkLabsGreen — всё стоит; в лабораториях зелёная наука (пришла лентой); идёт исследование
// на зелёной и за 30 с продвигается.
func checkLabsGreen(h *act.Hands) error {
	list, err := labsGreenLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	for _, pl := range list {
		if pl.Name != "inserter" || pl.Recipe == "" {
			continue
		}
		if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil && !slices.Contains(st.Filters, pl.Recipe) {
			return fmt.Errorf("инсертер в (%.1f, %.1f) без фильтра %s", pl.Pos.X, pl.Pos.Y, pl.Recipe)
		}
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	// зелёные дошли до лабораторий: в них, на лентах подачи или в буфере. В саму лабораторию
	// инсертер кладёт только пакеты текущего исследования (очередь по пути: впереди бывают на
	// одной красной — 851731946), а зелёных мало (0.1/с на 30 лабораторий) — искать не одним
	// снимком, а в течение минуты
	lab := clusterOf(plan, cluster.Lab, "")
	where := append(tagged(plan, cluster.Tag(lab.Name)), list...)
	fed := false
	for deadline := time.Now().Add(time.Minute); !fed && time.Now().Before(deadline); {
		for _, pl := range where {
			if pl.Name != "lab" && pl.Name != "transport-belt" && !strings.HasSuffix(pl.Name, "chest") {
				continue
			}
			st, err := h.B.GetEntity(pl.Name, pl.Pos)
			if err == nil && st != nil && (st.Count(greenPack) > 0 || st.Belt[greenPack] > 0) {
				fed = true
				break
			}
		}
		if !fed {
			time.Sleep(5 * time.Second)
		}
	}
	if !fed {
		return fmt.Errorf("до лабораторий за минуту не дошло %s", greenPack)
	}
	// исследование идёт (очередь — по порядку пути: впереди бывают на одной красной), в
	// текущем или очереди — на зелёной
	f0, err := h.B.Force()
	if err != nil {
		return err
	}
	green := false
	for _, t := range append([]string{f0.Current}, f0.Queue...) {
		green = green || t != "" && usesGreen(h, t)
	}
	if f0.Current == "" || !green {
		return fmt.Errorf("в очереди исследований нет на зелёной науке (идёт %q)", f0.Current)
	}
	time.Sleep(30 * time.Second)
	f1, err := h.B.Force()
	if err != nil {
		return err
	}
	if f1.Current == f0.Current && f1.Progress <= f0.Progress {
		return fmt.Errorf("исследование %s не продвигается (%.0f%%)", f0.Current, f0.Progress*100)
	}
	return nil
}
