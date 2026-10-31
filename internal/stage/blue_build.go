package stage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Общее для стройки проекта синей науки (стадии 29–41): постройки, которые стадия 5 дописала в
// план, — по кластерам стадии; исследования — в очередь заранее и ждать.

// planSource — чей проект строит стадия: дописанное им (added — план после минус план до) и
// весь план с ним (load).
type planSource struct {
	stage int // стадия проекта: 5 — синяя, 6 — фиолетовая
	added func() ([]layout.Placement, *ClusterPlan, error)
	load  func() (*ClusterPlan, error)
}

var (
	blueSource   = planSource{5, blueAdded, LoadBluePlan}
	purpleSource = planSource{6, purpleAdded, LoadPurplePlan}
)

// blueAddedFor — addedFor по проекту синей (стадия 5).
func blueAddedFor(h *act.Hands, file string, pick func(*cluster.Cluster) bool, splitters bool,
	more ...func(plan *ClusterPlan, all *layout.Layout) ([]layout.Placement, error)) ([]layout.Placement, error) {
	return addedFor(h, file, blueSource, pick, splitters, more...)
}

// addedFor — дописанное проектом src для кластеров, которые выбирает pick: их постройки и
// логистика (ленты, трубы, отводы), ленты шин их продуктов (и делители на шинах, если splitters)
// и делители плана в разрывах этих лент (withSplitters), столбы к сети базы; один раз на карту
// (file).
func addedFor(h *act.Hands, file string, src planSource, pick func(*cluster.Cluster) bool, splitters bool,
	more ...func(plan *ClusterPlan, all *layout.Layout) ([]layout.Placement, error)) ([]layout.Placement, error) {
	list, err := onceLayout(file, func() ([]layout.Placement, error) {
		added, plan, err := src.added()
		if err != nil {
			return nil, err
		}
		tags := map[string]bool{}
		items := map[string]bool{}
		var allow []string
		buses := busNames(plan)
		for _, c := range plan.Clusters {
			if c.Kind == cluster.Bus || !pick(c) {
				continue
			}
			tags[cluster.Tag(c.Name)], tags[cluster.LogisticsTag+"/"+c.Name] = true, true
			for _, bus := range buses {
				tags[cluster.LogisticsTag+"/"+bus+"/"+c.Name] = true
			}
			items[c.Product] = true
			for _, o := range c.Out {
				items[o.Item] = true
			}
			allow = append(allow, c.Name)
		}
		var res []layout.Placement
		for _, pl := range added {
			onBus := slices.ContainsFunc(buses, func(b string) bool { return pl.Tag == cluster.Tag(b) }) &&
				(items[pl.Recipe] || splitters && strings.HasSuffix(pl.Name, "splitter"))
			if tags[pl.Tag] || onBus {
				res = append(res, pl)
			}
		}
		if len(more) > 0 { // своё стадии (будущая добыча, буферы выхода…) — по всему плану
			all := layout.New(h.Protos, h.World)
			for _, pl := range plan.Layout {
				all.Add(pl)
			}
			for _, m := range more {
				add, err := m(plan, all)
				if err != nil {
					return nil, err
				}
				res = append(res, add...)
			}
			for _, c := range plan.Clusters {
				if c.Later {
					allow = append(allow, c.Name)
				}
			}
		}
		if len(res) == 0 {
			return nil, fmt.Errorf("в плане нет этих построек (%v)", allow)
		}
		poles, err := connectPower(h, plan, res, allow)
		return append(res, poles...), err
	})
	if err != nil {
		return nil, err
	}
	// лента шины — без разрывов: делитель будущего отвода на ней — сейчас, иначе предмет дальше
	// него не идёт (стадия 31 на 859088572: лента стали рвалась под отводом двигателей стадии 38
	// — буфер стали пуст, печи стоят полные); и в раскладках, сохранённых до этого правила
	plan, err := src.load()
	if err != nil {
		return nil, err
	}
	// подтеги кластеров стадии — их постройки тоже: трубы качалок («c/oil-1/oil»), внутренние
	// трубы НПЗ и химии («c/…/petroleum-gas»); не строились — качалки по одной, трасса пуста,
	// НПЗ без нефти (стадии 34–41 на 859088572); «/later» — будущая добыча (стадия 30)
	for _, c := range plan.Clusters {
		if c.Kind == cluster.Bus || !pick(c) {
			continue
		}
		for _, pl := range plan.Layout {
			if strings.HasPrefix(pl.Tag, cluster.Tag(c.Name)+"/") && !strings.HasSuffix(pl.Tag, "/later") {
				list = append(list, pl)
			}
		}
	}
	// сквозные ленты шин (c/logistics/bus-3/bus-2) с продуктами стадии — тоже: их не брал ни один
	// кластер — пластик и сера с bus-2 на bus-3 не шли (стадии 37–41 на всех трёх картах). И со
	// входами стадии: потребитель проверяет ленты своей подачи — построенное раньше не тем концом
	// (подземки) чинится здесь (стадия 40 на 427965634: сера от стадии 37 стояла на обрыве)
	items := map[string]bool{}
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Bus && pick(c) {
			items[c.Product] = true
			for _, o := range c.Out {
				items[o.Item] = true
			}
			for _, in := range c.In {
				items[in.Item] = true
			}
		}
	}
	list = append(list, throughLanes(plan, items)...)
	return uniq(withSplitters(h, plan, list)), nil
}

// throughLanes — сквозные ленты между шинами (тег LogisticsTag/шина/шина), по которым идёт
// предмет из items (вверх по подаче — до ленты шины с ним).
func throughLanes(plan *ClusterPlan, items map[string]bool) []layout.Placement {
	buses := map[string]bool{}
	for _, b := range busNames(plan) {
		buses[b] = true
	}
	at, pair := beltTiles(plan.Layout)
	var res []layout.Placement
	for _, pl := range plan.Layout {
		rest, ok := strings.CutPrefix(pl.Tag, cluster.LogisticsTag+"/")
		p := strings.Split(rest, "/")
		if !ok || len(p) != 2 || !buses[p[0]] || !buses[p[1]] || !isBelt(pl) {
			continue
		}
		if items[upstreamItem(at, pair, world.TileOf(pl.Pos))] {
			res = append(res, pl)
		}
	}
	return res
}

// queueTechs — исследования techs — в очередь, если ещё не изучены и не стоят.
func queueTechs(ctx context.Context, h *act.Hands, techs ...string) error {
	f, err := h.B.Force()
	if err != nil {
		return err
	}
	for _, t := range techs {
		if slices.Contains(f.Researched, t) || f.Current == t || slices.Contains(f.Queue, t) {
			continue
		}
		if err := h.StartResearch(ctx, t); err != nil {
			h.Say("исследование %s в очередь не встало: %v", t, err)
		}
	}
	return nil
}

// waitResearch — ждать, пока techs изучены (не стоят в очереди — поставить).
func waitResearch(ctx context.Context, h *act.Hands, techs ...string) error {
	if err := queueTechs(ctx, h, techs...); err != nil {
		return err
	}
	said := false
	for {
		f, err := h.B.Force()
		if err != nil {
			return err
		}
		var miss, out []string // не изучены; из них не в очереди
		for _, t := range techs {
			if !slices.Contains(f.Researched, t) {
				miss = append(miss, t)
				if f.Current != t && !slices.Contains(f.Queue, t) {
					out = append(out, t)
				}
			}
		}
		if len(miss) == 0 {
			return nil
		}
		// не встало в очередь (была полна — stages.md, общее: исследования в начале каждой
		// стадии) — доставить, как только там место
		busy := len(f.Queue)
		if f.Current != "" && !slices.Contains(f.Queue, f.Current) {
			busy++
		}
		if len(out) > 0 && busy < 7 {
			if err := queueTechs(ctx, h, out...); err != nil {
				return err
			}
		}
		if !said {
			h.Say("жду исследований: %v", miss)
			said = true
		}
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
}
