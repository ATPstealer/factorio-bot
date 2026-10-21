package stage

import (
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Части плана стадии 5 для стадий стройки (cluster.md: всё через шину — добыча в головы лент,
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

// clusterOfName — кластер плана по имени (пустой — нет).
func clusterOfName(plan *ClusterPlan, name string) cluster.Cluster {
	for _, c := range plan.Clusters {
		if c.Name == name {
			return *c
		}
	}
	return cluster.Cluster{}
}

// busNames — шины плана (зоны шин: bus-1 … bus-4).
func busNames(plan *ClusterPlan) []string {
	var res []string
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Bus {
			res = append(res, c.Name)
		}
	}
	return res
}

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

// busParts — ленты шин предметов items и подводы в шины от srcs (кластеры или шины снизу —
// сквозные ленты).
func busParts(plan *ClusterPlan, items []string, srcs []string) []layout.Placement {
	var res []layout.Placement
	for _, pl := range plan.Layout {
		ok := false
		for _, bus := range busNames(plan) {
			prefix := cluster.LogisticsTag + "/" + bus + "/"
			ok = ok || pl.Tag == cluster.Tag(bus) && slices.Contains(items, pl.Recipe) ||
				strings.HasPrefix(pl.Tag, prefix) && slices.Contains(srcs, strings.TrimPrefix(pl.Tag, prefix))
		}
		if ok {
			res = append(res, pl)
		}
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

// s11Base — стадия 11 из плана: добыча угля, её подвод и лента угля шины, отвод угля на станцию
// (лента, буфер, burner-инсертер у бойлера), делители на этих лентах.
func s11Base(h *act.Hands, plan *ClusterPlan) []layout.Placement {
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

// s12Base — стадия 12 из плана: добыча и плавка железа и меди, ленты руды шины с подводами от
// добычи, отводы руды и угля в плавку (с буферами входа), делители на этих лентах. Пластины на
// шину — стадия 13.
func s12Base(h *act.Hands, plan *ClusterPlan) []layout.Placement {
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

// s13Base — стадия 13 из плана: ленты пластин шины, подводы в них от плавки, делители на них
// (отводы будущих потребителей стоят в разрыве ленты).
func s13Base(h *act.Hands, plan *ClusterPlan) []layout.Placement {
	var srcs []string
	for _, it := range []string{"iron-plate", "copper-plate"} {
		if c := clusterOf(plan, cluster.Smelt, it); c != nil {
			srcs = append(srcs, c.Name)
		}
	}
	// сквозные ленты пластин на шины выше — подводы с шины снизу
	srcs = append(srcs, busNames(plan)...)
	return withSplitters(h, plan, busParts(plan, []string{"iron-plate", "copper-plate"}, srcs))
}
