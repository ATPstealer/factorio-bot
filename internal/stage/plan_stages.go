package stage

import (
	"strconv"
	"strings"

	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадии построек плана всей базы (stages.md: строки «Ставит», «Ленты и трубы шин — вместе с
// кластерами»): постройка кластера, его логистика и части — стадия кластера (блок добычи с тегом
// «later» — стадия 30, часть блока лабораторий — стадия своего пакета); лента или труба шины —
// стадия первого, кто ей пользуется (поставщик или потребитель), лента добытого — стадия блока,
// чей подвод в неё входит; вода химии — стадия 35, насосы у склада нефти — стадия склада (33).

// Стадии частей блока лабораторий по пакетам (stages.md, 21, 27, 41, 48, 61).
var labPackStage = map[string]int{redPack: 21, greenPack: 27, bluePack: 41, purplePack: 48, yellowPack: 61}

// Особые стадии: пластины на шину — стадия 18 (стадия 17 копит их в буферах выхода); вода химии —
// своя стадия 35.
const (
	platesOnBus = 18
	waterStage  = 35
)

// stageMap — стадия каждой постройки плана.
type stageMap map[layout.Placement]int

// planStages — стадии построек плана s.
func planStages(s *ClusterPlan) stageMap {
	by := map[string]*cluster.Cluster{}
	for _, c := range s.Clusters {
		by[c.Name] = c
	}
	// стадия предмета на шине: первый, кто его делает или берёт (кластеры этажей и добыча)
	itemStage := map[string]int{}
	use := func(it string, st int) {
		if v, ok := itemStage[it]; !ok || st < v {
			itemStage[it] = st
		}
	}
	for _, c := range s.Clusters {
		if c.Stage == 0 || c.Kind == cluster.Tank || c.Kind == cluster.Lab {
			continue
		}
		for _, f := range c.Out {
			st := c.Stage
			if c.Kind == cluster.Smelt && (f.Item == "iron-plate" || f.Item == "copper-plate") {
				st = max(st, platesOnBus)
			}
			use(f.Item, st)
		}
		for _, f := range c.In {
			use(f.Item, c.Stage)
		}
	}
	for _, c := range s.Clusters { // пакеты — лабораториям по своим стадиям
		if c.Kind == cluster.Lab {
			for _, f := range c.In {
				if st, ok := labPackStage[f.Item]; ok {
					use(f.Item, st)
				}
			}
		}
	}
	use("water", waterStage)
	// лента добытого в bus-1 — стадия блока, чей подвод входит в неё (у головы ряда)
	feederAt := map[world.Tile]int{}
	for _, pl := range s.Layout {
		if name, ok := strings.CutPrefix(pl.Tag, cluster.LogisticsTag+"/"); ok {
			if i := strings.LastIndex(name, "/"); i >= 0 {
				if c := by[name[i+1:]]; c != nil && c.Kind == cluster.Mine {
					feederAt[world.TileOf(pl.Pos)] = mineStage(c)
				}
			}
		}
	}
	rowStage := map[string]int{} // «шина/y» → стадия ряда (лента добытого)
	for _, pl := range s.Layout {
		if !strings.HasPrefix(pl.Tag, "c/bus-") || strings.Count(pl.Tag, "/") != 1 || pl.Recipe == "" {
			continue
		}
		t := world.TileOf(pl.Pos)
		for _, n := range []world.Tile{{X: t.X - 1, Y: t.Y}, {X: t.X, Y: t.Y - 1}, {X: t.X, Y: t.Y + 1}} {
			if st, ok := feederAt[n]; ok {
				key := pl.Tag + "/" + strconv.Itoa(t.Y)
				if v, had := rowStage[key]; !had || st < v {
					rowStage[key] = st
				}
			}
		}
	}
	tank := map[world.Tile]bool{} // у склада нефти (насосы на входе и выходе — его стадия)
	tankStage := 0
	for _, c := range s.Clusters {
		if c.Kind == cluster.Tank {
			tankStage = c.Stage
			g := c.Area.Grow(3)
			for y := g.Min.Y; y < g.Max.Y; y++ {
				for x := g.Min.X; x < g.Max.X; x++ {
					tank[world.Tile{X: x, Y: y}] = true
				}
			}
		}
	}
	res := stageMap{}
	for _, pl := range s.Layout {
		res[pl] = stageOf(pl, by, itemStage, rowStage)
		if pl.Name == "pump" && tank[world.TileOf(pl.Pos)] && tankStage > 0 {
			res[pl] = tankStage
		}
	}
	return res
}

// mineStage — стадия блока добычи (нефть — 34).
func mineStage(c *cluster.Cluster) int {
	if c.Stage > 0 {
		return c.Stage
	}
	if c.IsOil() {
		return 34
	}
	return 30
}

// stageOf — стадия постройки pl по её тегу (0 — неизвестно).
func stageOf(pl layout.Placement, by map[string]*cluster.Cluster, itemStage map[string]int, rowStage map[string]int) int {
	tag := pl.Tag
	if name, ok := strings.CutPrefix(tag, cluster.LogisticsTag+"/"); ok { // логистика кластера
		parts := strings.Split(name, "/")
		for i := len(parts) - 1; i >= 0; i-- { // подвод добычи: logistics/<шина>/<блок>
			if c := by[parts[i]]; c != nil && c.Kind != cluster.Bus {
				return clusterStage(c)
			}
		}
		return 0
	}
	name, ok := strings.CutPrefix(tag, "c/")
	if !ok {
		return 0
	}
	parts := strings.Split(name, "/")
	c := by[parts[0]]
	if c == nil {
		return 0
	}
	if c.Kind == cluster.Bus { // лента или труба шины
		if len(parts) > 1 { // труба жидкости parts[1]
			return itemStage[parts[1]]
		}
		if st, ok := rowStage[tag+"/"+strconv.Itoa(world.TileOf(pl.Pos).Y)]; ok {
			return st
		}
		return itemStage[pl.Recipe]
	}
	if len(parts) > 1 {
		switch {
		case parts[1] == "later":
			return 30
		case c.Kind == cluster.Lab:
			if st, ok := labPackStage[parts[1]]; ok {
				return st
			}
		}
	}
	return clusterStage(c)
}

// clusterStage — стадия кластера (добыча без стадии — 30, нефть — 34).
func clusterStage(c *cluster.Cluster) int {
	if c.Kind == cluster.Mine {
		return mineStage(c)
	}
	return c.Stage
}

// stageAdded — постройки плана стадий from…to (их строят эти стадии).
func stageAdded(s *ClusterPlan, from, to int) []layout.Placement {
	st := planStages(s)
	var res []layout.Placement
	for _, pl := range s.Layout {
		if n := st[pl]; n >= from && n <= to {
			res = append(res, pl)
		}
	}
	return res
}
