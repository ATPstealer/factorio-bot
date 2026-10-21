package stage

import (
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Части плана стадии 4 для стадий стройки (cluster.md: всё через шину — добыча в головы лент,
// потребители — отводами с шины).

// clusterOf — первый кластер плана вида kind (и продукта product, если не пусто).
func clusterOf(plan *ClusterPlan, kind cluster.Kind, product string) *cluster.Cluster {
	for _, c := range plan.Clusters {
		if c.Kind == kind && (product == "" || c.Product == product) {
			return c
		}
	}
	return nil
}

// busOf — шина плана (nil — нет).
func busOf(plan *ClusterPlan) *cluster.Cluster { return clusterOf(plan, cluster.Bus, "") }

// tagged — размещения плана с тегом из tags.
func tagged(plan *ClusterPlan, tags ...string) []layout.Placement {
	set := map[string]bool{}
	for _, t := range tags {
		set[t] = true
	}
	var res []layout.Placement
	for _, pl := range plan.Layout {
		if set[pl.Tag] {
			res = append(res, pl)
		}
	}
	return res
}

// busParts — ленты шины предметов items и подводы в шину от кластеров srcs.
func busParts(plan *ClusterPlan, items []string, srcs []string) []layout.Placement {
	bus := busOf(plan)
	if bus == nil {
		return nil
	}
	var res []layout.Placement
	for _, pl := range plan.Layout {
		switch {
		case pl.Tag == cluster.Tag(bus.Name) && slices.Contains(items, pl.Recipe):
		case strings.HasPrefix(pl.Tag, cluster.LogisticsTag+"/"+bus.Name+"/") && slices.Contains(srcs, strings.TrimPrefix(pl.Tag, cluster.LogisticsTag+"/"+bus.Name+"/")):
		default:
			continue
		}
		res = append(res, pl)
	}
	return res
}

// withSplitters — res и делители плана в разрыве лент из res (отводы потребителей стоят на
// месте ленты: без них лента не непрерывна).
func withSplitters(h *act.Hands, plan *ClusterPlan, res []layout.Placement) []layout.Placement {
	line := map[world.Tile]bool{}
	have := map[layout.Placement]bool{}
	for _, pl := range res {
		have[pl] = true
		if pl.Name == "transport-belt" || pl.Name == "underground-belt" {
			line[world.TileOf(pl.Pos)] = true
		}
	}
	for _, pl := range plan.Layout {
		if pl.Name != "splitter" || have[pl] {
			continue
		}
		for _, t := range layout.Tiles(h.Protos, pl) {
			if line[layout.Step(t, pl.Dir)] || line[layout.Step(t, (pl.Dir+8)%16)] {
				res = append(res, pl)
				have[pl] = true
				break
			}
		}
	}
	return res
}

// s10Base — стадия 10 из плана: добыча угля, её подвод и лента угля шины, отвод угля на станцию
// (лента, буфер, burner-инсертер у бойлера), делители на этих лентах.
func s10Base(h *act.Hands, plan *ClusterPlan) []layout.Placement {
	coal, power := clusterOf(plan, cluster.Mine, "coal"), clusterOf(plan, cluster.Power, "")
	if coal == nil || power == nil {
		return nil
	}
	res := tagged(plan, cluster.Tag(coal.Name))
	res = append(res, busParts(plan, []string{"coal"}, []string{coal.Name})...)
	for _, pl := range plan.Layout {
		switch {
		case pl.Tag == cluster.LogisticsTag+"/"+power.Name && !strings.HasPrefix(pl.Name, "pipe") && pl.Name != "offshore-pump":
		case pl.Tag == cluster.Tag(power.Name) && (pl.Name == "transport-belt" || pl.Name == "burner-inserter" || pl.Name == chest):
		default:
			continue
		}
		res = append(res, pl)
	}
	return withSplitters(h, plan, res)
}

// s11Base — стадия 11 из плана: добыча и плавка железа и меди, ленты руды шины с подводами от
// добычи, отводы руды и угля в плавку (с буферами входа), делители на этих лентах. Пластины на
// шину — стадия 12.
func s11Base(h *act.Hands, plan *ClusterPlan) []layout.Placement {
	var res []layout.Placement
	var srcs []string
	for _, it := range []string{"iron", "copper"} {
		mine, smelt := clusterOf(plan, cluster.Mine, it+"-ore"), clusterOf(plan, cluster.Smelt, it+"-plate")
		if mine == nil || smelt == nil {
			continue
		}
		srcs = append(srcs, mine.Name)
		res = append(res, tagged(plan, cluster.Tag(mine.Name), cluster.Tag(smelt.Name), cluster.LogisticsTag+"/"+smelt.Name)...)
	}
	res = append(res, busParts(plan, []string{"iron-ore", "copper-ore"}, srcs)...)
	return withSplitters(h, plan, res)
}

// s12Base — стадия 12 из плана: ленты пластин шины, подводы в них от плавки, делители на них
// (отводы будущих потребителей стоят в разрыве ленты).
func s12Base(h *act.Hands, plan *ClusterPlan) []layout.Placement {
	var srcs []string
	for _, it := range []string{"iron-plate", "copper-plate"} {
		if c := clusterOf(plan, cluster.Smelt, it); c != nil {
			srcs = append(srcs, c.Name)
		}
	}
	return withSplitters(h, plan, busParts(plan, []string{"iron-plate", "copper-plate"}, srcs))
}
