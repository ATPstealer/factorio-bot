package stage

import (
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// outBuf — буфер выхода кластера (cluster.md §6а): сундук и инсертер с фильтром на продукт.
type outBuf struct {
	chest, ins layout.Placement
	cluster    string // имя кластера
	item       string // продукт
}

// outBufs — буферы выхода в list: сундук кластера, в который кладёт инсертер с фильтром на продукт
// этого кластера (шаблоны: тег — тег кластера), или сундук с тегом «outbuf/<кластер>» (раскладки
// до рефакторинга). Сундук угля станции — не буфер выхода. Проверки стадий искали только
// «outbuf/» — буферов не находили (стадия 17 на 1114315936: «в буферах выхода нет iron-plate»).
func outBufs(h *act.Hands, list []layout.Placement) []outBuf {
	plan, err := LoadPlan()
	if err != nil {
		return nil
	}
	by := map[string]*cluster.Cluster{}
	for _, c := range plan.Clusters {
		by[c.Name] = c
	}
	product := func(c *cluster.Cluster) string {
		if len(c.Out) > 0 {
			return c.Out[0].Item
		}
		return c.Product
	}
	owner := func(tag string) *cluster.Cluster { // кластер по тегу «c/<имя>[/…]»
		name, ok := strings.CutPrefix(tag, "c/")
		if !ok {
			return nil
		}
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[:i]
		}
		return by[name]
	}
	chests := map[world.Tile]layout.Placement{}
	for _, pl := range list {
		if strings.HasSuffix(pl.Name, "chest") {
			chests[world.TileOf(pl.Pos)] = pl
		}
	}
	var res []outBuf
	seen := map[layout.Placement]bool{}
	for _, pl := range list {
		switch {
		case strings.HasSuffix(pl.Name, "chest") && strings.HasPrefix(pl.Tag, "outbuf/"):
			name := strings.TrimPrefix(pl.Tag, "outbuf/")
			b := outBuf{chest: pl, cluster: name}
			if c := by[name]; c != nil {
				b.item = product(c)
			}
			if !seen[pl] {
				seen[pl] = true
				res = append(res, b)
			}
		case strings.HasSuffix(pl.Name, "inserter") && pl.Recipe != "":
			c := owner(pl.Tag)
			if c == nil || c.Kind == cluster.Power || pl.Recipe != product(c) {
				continue
			}
			_, drop := layout.InserterEnds(h.Protos, pl)
			ch, ok := chests[drop]
			if !ok || owner(ch.Tag) != c || seen[ch] {
				continue
			}
			seen[ch] = true
			res = append(res, outBuf{chest: ch, ins: pl, cluster: c.Name, item: pl.Recipe})
		}
	}
	return res
}

// outBufChests — сундуки буферов выхода в list: сундук → буфер.
func outBufChests(h *act.Hands, list []layout.Placement) map[layout.Placement]outBuf {
	res := map[layout.Placement]outBuf{}
	for _, b := range outBufs(h, list) {
		res[b.chest] = b
	}
	return res
}
