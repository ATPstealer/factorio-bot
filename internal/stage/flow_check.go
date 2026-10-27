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
func flowCheck(h *act.Hands, l *layout.Layout, cs []*cluster.Cluster) error {
	later := cluster.LaterNames(cs)
	skip := func(pl layout.Placement) bool {
		for name := range later {
			if pl.Tag == cluster.Tag(name) || strings.HasSuffix(pl.Tag, "/"+name) {
				return true
			}
		}
		return false
	}
	errs := l.Flow(skip)
	for _, e := range errs {
		h.Log("течение: %s", e)
	}
	if len(errs) > 0 {
		return fmt.Errorf("течение: машин без входа %d, первая — %s", len(errs), errs[0])
	}
	h.Log("течение: у всех машин плана входы есть")
	return nil
}
