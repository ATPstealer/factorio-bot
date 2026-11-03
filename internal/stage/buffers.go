package stage

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/knowledge"
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

// bufNoLimit — буферы без ограничения (решение пользователя): быстрые ленты — буфер для стройки.
var bufNoLimit = map[string]bool{"fast-transport-belt": true}

// buildNeeds — сколько каждого продукта из buf уходит на стройку одной стадии (максимум по
// стадиям): предметы стадии (byStage: стадия → предмет → сколько) — крафтом до продуктов buf.
func buildNeeds(p *knowledge.Prototypes, byStage map[int]map[string]float64, buf map[string]bool) map[string]float64 {
	res := map[string]float64{}
	for _, items := range byStage {
		need := map[string]float64{}
		var expand func(item string, k float64, depth int)
		expand = func(item string, k float64, depth int) {
			if buf[item] {
				need[item] += k
				return
			}
			r, ok := p.Recipes[item]
			if !ok || depth > 20 {
				return
			}
			out := 1.0
			for _, pr := range r.Products {
				if pr.Name == item {
					out = pr.Mean()
				}
			}
			for _, in := range r.Ingredients {
				expand(in.Name, k*in.Mean()/out, depth+1)
			}
		}
		for it, k := range items {
			expand(it, k, 0)
		}
		for it, k := range need {
			res[it] = max(res[it], k)
		}
	}
	return res
}

// bufLimitCache — bufLimits по файлу плана (план за процесс не меняется).
var bufLimitCache = map[string]map[string]int{}

// bufLimits — ограничение буферов выхода по продукту (cluster.md §6а): ячеек — сколько продукта
// нужно на стройку одной стадии плана (максимум по стадиям 11–68; замены на второй уровень —
// стадиями их порогов, старая постройка возвращается; спутник — как постройка стадии 67), не
// меньше одной; нет в списке — без ограничения.
func bufLimits(h *act.Hands) (map[string]int, error) {
	key := runPath(planFile)
	if res, ok := bufLimitCache[key]; ok {
		return res, nil
	}
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	buf := map[string]bool{}
	for _, b := range outBufs(h, plan.Layout) {
		if b.item != "" {
			buf[b.item] = true
		}
	}
	st := planStages(plan)
	byStage := map[int]map[string]float64{}
	add := func(n int, item string, k float64) {
		if byStage[n] == nil {
			byStage[n] = map[string]float64{}
		}
		byStage[n][item] += k
	}
	for _, pl := range plan.Layout {
		n := st[pl]
		if n < 11 || n > lastStage {
			continue
		}
		// своей стадией — тем уровнем, каким ставится; стоит до порога — замена на пороге
		base := baseTier(pl.Name)
		name := base
		if from := tierFrom(base); from > 0 && n >= from {
			name = cluster.Tier2[base]
		}
		if it := h.Protos.ItemFor(name); it != "" {
			add(n, it, 1)
		}
		if from := tierFrom(base); from > 0 && n < from {
			for _, in := range h.Protos.Recipes[cluster.Tier2[base]].Ingredients {
				if in.Name != base {
					add(from, in.Name, in.Mean())
				}
			}
		}
	}
	add(satelliteStage, "satellite", 1) // спутник — руками из буферов (stages.md, стадия 67)
	need := buildNeeds(h.Protos, byStage, buf)
	res := map[string]int{}
	for it := range buf {
		if !bufNoLimit[it] {
			res[it] = max(1, int(math.Ceil(need[it]/float64(max(h.Protos.Items[it].StackSize, 1)))))
		}
	}
	bufLimitCache[key] = res
	return res, nil
}

// limitBuffers — стоящие в list буферы выхода ограничить по расчёту (stages.md «Буфер выхода — с
// ограничением»): ближайший к персонажу — первым. Ограничение не меньше сундука — без него
// (стоящее — снять).
func limitBuffers(ctx context.Context, h *act.Hands, list []layout.Placement) error {
	lim, err := bufLimits(h)
	if err != nil {
		return err
	}
	bufs := outBufs(h, list)
	chests := make([]layout.Placement, len(bufs))
	for i, b := range bufs {
		chests[i] = b.chest
	}
	states, err := entityStates(h, chests)
	if err != nil {
		return err
	}
	type todo struct {
		e    bridge.Entity
		want int
	}
	var left []todo
	for i, b := range bufs {
		st := states[i]
		if st == nil {
			continue
		}
		want, ok := lim[b.item]
		if !ok || want > st.SlotsSize {
			want = st.SlotsSize
		}
		open := st.SlotsSize
		if st.Limit != nil {
			open = *st.Limit
		}
		if open == want {
			continue
		}
		left = append(left, todo{st.Entity, want})
	}
	for len(left) > 0 {
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		k := 0
		for j, t := range left {
			if dist2(t.e.Position, p.Position) < dist2(left[k].e.Position, p.Position) {
				k = j
			}
		}
		t := left[k]
		left = slices.Delete(left, k, k+1)
		if err := h.LimitChest(ctx, t.e, t.want); err != nil {
			return fmt.Errorf("буфер в (%.1f, %.1f): %w", t.e.Position.X, t.e.Position.Y, err)
		}
	}
	return nil
}
