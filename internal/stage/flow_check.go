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
// инсертерам и трубам от источников (буры, помпы, качалки, печи, сборщики), будущие блоки — в
// счёт (план всей базы). Нашлось — всё в журнал, стадия плана не выполнена.
func flowCheck(h *act.Hands, l *layout.Layout, cs []*cluster.Cluster) error {
	errs := l.Flow(nil)
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

// SkipLater — постройки блоков добычи стадии 30 (Later) и их логистики, кроме делителей:
// делитель на ленте шины строится вместе с лентой (withSplitters). Для bin/bot flow — течение
// до стадии 30.
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

// splitPipes — участки труб плана не шире 300 (cluster.md §5): насосы на пролётах труб шин
// (layout.SplitWide); после — проверка, что шире 320 не осталось (вода к НПЗ‑2 и газ к
// химзаводам не шли: стадия 51 на 427965634).
func splitPipes(h *act.Hands, l *layout.Layout) error {
	added, err := l.SplitWide(func(tag string) bool { return strings.HasPrefix(tag, "c/bus-") })
	for _, pl := range added {
		if pl.Name == "pump" {
			h.Say("участок трубы шире %d: насос %s (%s)", layout.PipeExtent, pl, pl.Tag)
		}
	}
	if err != nil {
		return err
	}
	if wide := l.WideSegments(layout.PipeExtentGame); len(wide) > 0 {
		return fmt.Errorf("%s", strings.Join(wide, "; "))
	}
	return nil
}
