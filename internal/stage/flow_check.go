package stage

import (
	"fmt"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// flowCheck — проверка течения по готовому плану без игры (layout.Flow; просьба пользователя
// 27.10: тест после генерации чертежей): у каждой машины плана есть все входы — по лентам,
// инсертерам и трубам от источников (буры, помпы, качалки, печи, сборщики). Будущие блоки
// (Later: добыча и кирпич стадии 30) и то, что они кормят, — не в счёт: их ещё нет. Нашлось —
// всё в журнал, стадия плана не выполнена.
// all — считать и будущие блоки (план стадии 5: строится после стадии 30, когда они есть).
func flowCheck(h *act.Hands, l *layout.Layout, cs []*cluster.Cluster, all bool) error {
	return flowCheckSince(h, l, cs, all, nil)
}

// flowCheckSince — flowCheck без того, что уже было в старом плане old (дописывание поверх
// построенного: смешанные ленты старой базы — не ошибка проекта, 427965634 и 267145981 после
// стадии 48 — сера на ленте кирпича, железо на ленте меди). Прощается: та же ошибка и чужой
// предмет на ленте той же пары «предмет — лента» (смешение старой ленты переходит на её
// продление); nil — всё как в flowCheck.
func flowCheckSince(h *act.Hands, l *layout.Layout, cs []*cluster.Cluster, all bool, old []layout.Placement) error {
	skip := skipLater(cs)
	if all {
		skip = nil
	}
	errs := l.Flow(skip)
	if old != nil {
		lo := layout.New(l.P, l.W)
		lo.DrillOre = l.DrillOre
		for _, pl := range old {
			lo.Add(pl)
		}
		was, pairs := map[string]bool{}, map[string]bool{}
		for _, e := range lo.Flow(skip) {
			was[e] = true
			if i := strings.Index(e, "чужой предмет"); i >= 0 {
				pairs[e[i:]] = true
			}
		}
		n := 0
		for _, e := range errs {
			i := strings.Index(e, "чужой предмет")
			if !was[e] && !(i >= 0 && pairs[e[i:]]) {
				errs[n] = e
				n++
			}
		}
		if len(errs) > n {
			h.Say("течение: %d нарушений — из старого плана (смешанные ленты базы), не в счёт", len(errs)-n)
		}
		errs = errs[:n]
	}
	for _, e := range errs {
		h.Log("течение: %s", e)
	}
	if len(errs) > 0 {
		// план с ошибкой — в файл для разбора (bin/bot flow КАРТА.gob state-flow-fail.json X Y)
		if err := writeState("state-flow-fail.json", ClusterPlan{Clusters: cs, Placed: len(l.List), Layout: l.List}); err != nil {
			h.Log("течение: план не сохранён: %v", err)
		}
		return fmt.Errorf("течение: машин без входа %d, первая — %s", len(errs), errs[0])
	}
	h.Log("течение: у всех машин плана входы есть")
	return nil
}

// SkipLater — чего в плане ещё нет для проверки течения: постройки будущих блоков (Later) и их
// логистики, кроме делителей: делитель на ленте шины строится вместе с лентой (withSplitters),
// даже если его второй выход — к будущей перекидке. Для flowCheck и bin/bot flow.
func SkipLater(cs []*cluster.Cluster) func(pl layout.Placement) bool { return skipLater(cs) }

func skipLater(cs []*cluster.Cluster) func(pl layout.Placement) bool {
	later := cluster.LaterNames(cs)
	return func(pl layout.Placement) bool {
		if pl.Name == "splitter" {
			return false
		}
		for name := range later {
			if pl.Tag == cluster.Tag(name) || strings.HasSuffix(pl.Tag, "/"+name) {
				return true
			}
		}
		return false
	}
}
