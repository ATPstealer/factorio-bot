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
	skip := skipLater(cs)
	if all {
		skip = nil
	}
	errs := l.Flow(skip)
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
