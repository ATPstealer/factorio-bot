package stage

import (
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Части плана стадии 3 для стадий стройки (cluster.md: всё через шину — добыча в головы лент,
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

// clustersOf — все кластеры плана вида kind и продукта product, кроме будущей добычи (стадия 30):
// длинный кластер план режет на куски (iron-plate-1, iron-plate-1.2 — PlaceZoned), каждый кусок
// — свой кластер; первый — не весь (стадия 17 ставила только первый кусок плавки: 260647970).
func clustersOf(plan *ClusterPlan, kind cluster.Kind, product string) []*cluster.Cluster {
	var res []*cluster.Cluster
	for _, c := range plan.Clusters {
		if c.Kind == kind && c.Product == product && !c.Later {
			res = append(res, c)
		}
	}
	return res
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

// laterTags — теги будущей добычи плана (cluster.Later — строится на стадии 30): её кластеры, их
// логистика, дописанное на потом в блоки первой итерации (LaterTag).
func laterTags(plan *ClusterPlan) map[string]bool {
	res := map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Mine || c.Kind == cluster.Smelt {
			res[cluster.LaterTag(c.Name)] = true
		}
		if !c.Later {
			continue
		}
		res[cluster.Tag(c.Name)], res[cluster.LogisticsTag+"/"+c.Name] = true, true
		for _, bus := range busNames(plan) {
			res[cluster.LogisticsTag+"/"+bus+"/"+c.Name] = true
		}
	}
	return res
}

// withoutLater — list без будущей добычи и её лент шин (их строит стадия 30).
func withoutLater(h *act.Hands, plan *ClusterPlan, list []layout.Placement) []layout.Placement {
	later := laterTags(plan)
	lanes := map[layout.Placement]bool{}
	for _, pl := range laterLanes(h, plan) {
		lanes[pl] = true
	}
	return slices.DeleteFunc(slices.Clone(list), func(pl layout.Placement) bool { return later[pl.Tag] || lanes[pl] })
}

// laterLanes — ленты шин предметов, которые даёт только будущая добыча (камень, кирпич — кластеры
// Later), с подземками и делителями в разрыве: их строит стадия 30 вместе с добычей (stages.md:
// «добыча камня, свои ленты bus-1»). Лента камня bus-1 — в плане первой итерации, а не в
// будущей добыче: её не ставила ни одна стадия (на бумаге, 260647970).
func laterLanes(h *act.Hands, plan *ClusterPlan) []layout.Placement {
	now, later := map[string]bool{}, map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Mine && c.Kind != cluster.Smelt {
			continue
		}
		if c.Later {
			later[c.Product] = true
		} else {
			now[c.Product] = true
		}
	}
	var items []string
	for it := range later {
		if !now[it] {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return nil
	}
	sort.Strings(items)
	return withSplitters(h, plan, busParts(plan, items, nil))
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
	// подземки лент шины: в Recipe у них «вход/выход», а не предмет — берутся по соседству с
	// взятыми лентами (вход — за лентой, выход — перед), пара — вместе (медная лента шины
	// рвалась под отводом железа: стадия 18 на 954176679)
	line := map[world.Tile]bool{}
	for _, pl := range res {
		line[world.TileOf(pl.Pos)] = true
	}
	buses := map[string]bool{}
	for _, b := range busNames(plan) {
		buses[cluster.Tag(b)] = true
	}
	for added := true; added; {
		added = false
		for _, pl := range plan.Layout {
			t := world.TileOf(pl.Pos)
			if pl.Name != "underground-belt" || !buses[pl.Tag] || line[t] {
				continue
			}
			near := pl.Recipe == "input" && line[layout.Step(t, (pl.Dir+8)%16)] ||
				pl.Recipe == "output" && line[layout.Step(t, pl.Dir)]
			// пара — вместе: выход уже взятого входа (назад по ходу) и вход уже взятого выхода
			// (вперёд: начало ленты подземкой под подводом — за входом пусто; стадии ставили
			// один выход, 859088572 на бумаге)
			toward, want := (pl.Dir+8)%16, "input"
			if pl.Recipe == "input" {
				toward, want = pl.Dir, "output"
			}
			for k := 1; k <= 10 && !near; k++ {
				b := t
				for j := 0; j < k; j++ {
					b = layout.Step(b, toward)
				}
				near = line[b] && slices.ContainsFunc(res, func(q layout.Placement) bool {
					return q.Name == "underground-belt" && q.Recipe == want && world.TileOf(q.Pos) == b
				})
			}
			if near {
				res = append(res, pl)
				line[t], added = true, true
			}
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

// coalBase — стадия 11 из плана: добыча угля, её подвод и лента угля шины, отвод угля на станцию
// (лента, буфер, burner-инсертер у бойлера), делители на этих лентах.
func coalBase(h *act.Hands, plan *ClusterPlan) []layout.Placement {
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

// smeltingBase — стадия 17 из плана: добыча и плавка железа и меди, ленты руды шины с подводами от
// добычи, отводы руды и угля в плавку (с буферами входа), делители на этих лентах. Пластины на
// шину — стадия 13.
func smeltingBase(h *act.Hands, plan *ClusterPlan) []layout.Placement {
	var res []layout.Placement
	var srcs []string
	for _, it := range []string{"iron", "copper"} {
		mines, smelts := clustersOf(plan, cluster.Mine, it+"-ore"), clustersOf(plan, cluster.Smelt, it+"-plate")
		if len(mines) == 0 || len(smelts) == 0 {
			continue
		}
		var tags []string
		for _, m := range mines {
			srcs = append(srcs, m.Name)
			tags = append(tags, cluster.Tag(m.Name))
		}
		for _, s := range smelts {
			tags = append(tags, cluster.Tag(s.Name), cluster.LogisticsTag+"/"+s.Name)
		}
		res = append(res, tagged(plan, tags...)...)
	}
	res = append(res, busParts(plan, []string{"iron-ore", "copper-ore"}, srcs)...)
	return withSplitters(h, plan, res)
}

// busPlatesBase — стадия 18 из плана: ленты пластин шины, подводы в них от плавки, делители на них
// (отводы будущих потребителей стоят в разрыве ленты).
func busPlatesBase(h *act.Hands, plan *ClusterPlan) []layout.Placement {
	var srcs []string
	for _, it := range []string{"iron-plate", "copper-plate"} {
		for _, c := range clustersOf(plan, cluster.Smelt, it) {
			srcs = append(srcs, c.Name)
		}
	}
	// сквозные ленты пластин на шины выше — подводы с шины снизу
	srcs = append(srcs, busNames(plan)...)
	return withSplitters(h, plan, busParts(plan, []string{"iron-plate", "copper-plate"}, srcs))
}
