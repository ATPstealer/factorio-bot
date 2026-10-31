package stage

import (
	"fmt"
	"slices"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Рост старых кластеров в проектах наук (стадии 6 и 7; cluster.md §7): станция — первым делом,
// потом плавка и сборка; вдоль ряда, не влезло — кластер-2 того же продукта (Extras) или ещё
// станция у воды. Общее для стадий 6 и 7.

// growth — итог роста: новые станции и чья они замена.
type growth struct {
	stations  []*cluster.Cluster
	stationOf *cluster.Cluster
}

// growProject — рост под потребность needs: новые кластеры — в *freshp (его же раскладывает
// layoutAll: список — тот, что видит её замыкание; копия теряла новые станции — 18604156,
// стадия 6: power-3 без бойлеров), раскладка *lp перекладывается заново с ростом вдоль рядов.
func growProject(h *act.Hands, pr *knowledge.Prototypes, base *cluster.Base, lp **layout.Layout, needs []*cluster.Cluster,
	zones []cluster.Zone, freshp *[]*cluster.Cluster, layoutAll func() (*layout.Layout, error)) (growth, error) {
	l := *lp
	fresh := *freshp
	var err error
	grow := map[*cluster.Cluster]int{}
	var extras []*cluster.Cluster
	var stationOf *cluster.Cluster
	var stations []*cluster.Cluster
	order := slices.Clone(base.Clusters)
	slices.SortStableFunc(order, func(a, b *cluster.Cluster) int {
		rank := func(c *cluster.Cluster) int {
			if c.Kind == cluster.Power {
				return 0
			}
			return 1
		}
		return rank(a) - rank(b)
	})
	// потребность — на продукт целиком (Need — по всем кластерам плана того же продукта), стоит —
	// сумма по всем кластерам продукта (iron-plate-1…6): нехватка раздаётся по ним вдоль рядов,
	// остаток — новым кластером
	type key struct {
		kind    cluster.Kind
		product string
	}
	done := map[key]bool{}
	for _, c := range order {
		if slices.Contains(fresh, c) || done[key{c.Kind, c.Product}] {
			continue
		}
		switch c.Kind {
		case cluster.Smelt, cluster.Power:
		case cluster.Make:
			// наука и вспомогательные (быстрые ленты и инсертеры — на срок, не на темп) не растут
			if cluster.IsScience(c.Product) || c.Product == "fast-transport-belt" || c.Product == "fast-inserter" {
				continue
			}
		default:
			continue
		}
		done[key{c.Kind, c.Product}] = true
		var group []*cluster.Cluster
		have := 0
		for _, g := range base.Clusters {
			if g.Kind == c.Kind && g.Product == c.Product && !slices.Contains(fresh, g) {
				group = append(group, g)
				have += cluster.Built(l.List, g)
			}
		}
		n := cluster.Need(needs, c)
		if c.Kind == cluster.Power { // по тому, что реально стоит в плане
			var w float64
			n, w = cluster.PowerNeed(pr, l)
			h.Say("энергия плана: %.1f МВт — бойлеров %d (стоит %d)", w/1e6, n, have)
		}
		h.Log("рост: %s — нужно %d, стоит %d (кластеров %d)", c.Product, n, have, len(group))
		if n <= have {
			continue
		}
		deficit := n - have
		for _, g := range group {
			if deficit <= 0 {
				break
			}
			bg := cluster.Built(l.List, g)
			m := min(bg+deficit, base.GrowCap(g))
			if m <= bg {
				continue
			}
			if err := base.GrowRow(l, g, m); err == nil {
				h.Say("рост: %s — %s %d → %d вдоль ряда", g.Name, g.MainMachine(), bg, m)
				grow[g], deficit = m, deficit-(m-bg)
				continue
			} else {
				h.Log("рост %s: %v", g.Name, err)
			}
			if g.Kind == cluster.Power { // вдоль ряда — сколько влезет
				for k := m - 1; k > bg; k-- {
					if err := base.GrowRow(l, g, k); err == nil {
						h.Say("рост: %s — %s %d → %d вдоль ряда", g.Name, g.MainMachine(), bg, k)
						grow[g], deficit = k, deficit-(k-bg)
						break
					}
				}
			}
		}
		if deficit <= 0 {
			continue
		}
		if c.Kind == cluster.Power {
			// остальное — ещё станция у воды (cluster.md §2а «Станция»), как на стадии 5
			stationOf = c
			for rest := deficit; rest > 0; {
				k := min(rest, cluster.PumpBoilers(pr))
				st := cluster.Station(pr, k)
				if st == nil {
					return growth{}, fmt.Errorf("станция на %d бойлеров не считается", k)
				}
				st.Name = base.Name("power", stations)
				x, y := c.Area.Center()
				near := world.Tile{X: int(x), Y: int(y)}
				st.Near = &near
				stations = append(stations, st)
				extras = append(extras, st)
				h.Say("рост: ещё станция %s на %d бойлеров", st.Name, k)
				rest -= k
			}
			continue
		}
		xs, err := cluster.Extras(pr, needs, c, n-deficit, n, base.GrowCap(c))
		if err != nil {
			return growth{}, fmt.Errorf("рост %s (%d → %d): %w", c.Product, n-deficit, n, err)
		}
		extras = append(extras, xs...)
	}
	if len(extras) > 0 {
		var err error
		if extras, _, err = base.PlaceZoned(extras, zones); err != nil {
			return growth{}, err
		}
		for _, x := range extras {
			h.Say("рост: новый кластер %s", x.Name)
		}
		*freshp = append(*freshp, extras...)
	}
	if *lp, err = layoutAll(); err != nil {
		return growth{}, err
	}
	l = *lp
	for c, n := range grow {
		if err := base.GrowRow(l, c, n); err != nil {
			return growth{}, err
		}
	}
	return growth{stations: stations, stationOf: stationOf}, nil
}
