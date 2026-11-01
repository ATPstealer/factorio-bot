package stage

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// План всей базы (stages.md, стадия 2): в зонах стадии 2 — сразу все кластеры до стадии 61 по
// таблице «что ставим» (cluster.Builds): места, внутренности, ленты и трубы шин, добыча по руде
// под бурами (стадии 16–17 и 30), нефть (не меньше 30 скважин, месторождения целиком) с трассами
// в склад нефти, вода химии своей помпой, насосы не реже чем через 290 труб. Кластер, к которому
// ленту не провести, — в другое место в своей зоне. Картинка — план поверх карты; состояние —
// state-plan.json.

// planTries — попыток переложить кластер, к которому ленту не провести (как в плане жёлтой — до 10).
const planTries = 10

// floorSpare — этажам зон шире ещё на столько процентов (план всей базы не встал: на этаже не
// хватило места — этаж шире на spareStep и зоны заново, до spareTries раз).
var floorSpare [4]int

const (
	spareStep  = 20
	spareTries = 3
)

// mineNeed — сколько руды нужно в зонах добычи на игру (zones.md §4): цели добычи таблицы (руда
// под бурами) с поправкой на долю пятна под бурами (MineCover).
func mineNeed() map[string]float64 {
	need := map[string]float64{}
	for _, b := range cluster.Builds {
		if b.Kind == cluster.Mine && b.Ore > 0 {
			need[b.Product] += float64(b.Ore) / cluster.MineCover
		}
	}
	return need
}

// planClusters — кластеры плана по таблице, кроме добычи: поставщики раньше потребителей
// (PlaceZoned ставит слева направо, потребитель — правее поставщиков, и для жидкостей: zones.md),
// при равенстве — по стадиям; смазка — раньше крекинга тяжёлого (stages.md, стадия 52: выше по
// трубе тяжёлого, ближе к НПЗ‑2).
func planClusters(pr *knowledge.Prototypes) []*cluster.Cluster {
	var cs []*cluster.Cluster
	for _, b := range cluster.Builds {
		if b.Kind != cluster.Mine {
			cs = append(cs, b.Cluster(pr))
		}
	}
	rank := func(c *cluster.Cluster) float64 {
		if c.Product == "light-oil" { // крекинг тяжёлого — после смазки (53)
			return 53.5
		}
		return float64(c.Stage)
	}
	makes := func(c *cluster.Cluster, it string) bool {
		return slices.ContainsFunc(c.Out, func(f cluster.Flow) bool { return f.Item == it })
	}
	// коридор по факту (cluster.md §3): бордюр — 1 (коридор между соседями — не меньше 2) и по
	// тайлу на каждую ленту вдоль кластера — вход с шины над этажом идёт вниз по коридору к голове
	// (снабжение: схемы с bus-3); остальное — шире по ходу плана (не проложилось — planBase)
	floor := map[string]int{}
	for _, c := range cs {
		for _, o := range c.Out {
			floor[o.Item] = cluster.FloorOf(c)
		}
	}
	for _, c := range cs {
		f := cluster.FloorOf(c)
		if f == 0 {
			continue
		}
		c.Border = 1
		if c.Kind == cluster.Make && !c.HeadAbove {
			for _, in := range c.In {
				if _, fluid := pr.Fluids[in.Item]; !fluid && floor[in.Item] == f {
					c.Border++
				}
			}
		}
	}
	done := map[*cluster.Cluster]bool{}
	var res []*cluster.Cluster
	for len(res) < len(cs) {
		var best, any *cluster.Cluster
		for _, c := range cs {
			if done[c] {
				continue
			}
			if any == nil || rank(c) < rank(any) {
				any = c
			}
			ready := true
			for _, in := range c.In {
				for _, o := range cs {
					if o != c && !done[o] && makes(o, in.Item) {
						ready = false
					}
				}
			}
			if ready && (best == nil || rank(c) < rank(best)) {
				best = c
			}
		}
		if best == nil { // круговая зависимость — по стадиям
			best = any
		}
		done[best] = true
		res = append(res, best)
	}
	return res
}

// oreTargets — цели добычи по ресурсам (руда под бурами, без угля станции): first — блоков стадий
// 16–17, total — с добычей стадии 30.
func oreTargets() (first, total map[string]int) {
	first, total = map[string]int{}, map[string]int{}
	for _, b := range cluster.Builds {
		if b.Kind != cluster.Mine || b.Ore == 0 || b.ToPower {
			continue
		}
		total[b.Product] += b.Ore
		if b.Stage < 30 {
			first[b.Product] += b.Ore
		}
	}
	return first, total
}

// shortError — руды под бурами (по раскладке) меньше цели: оценка по пятнам не сошлась.
type shortError struct {
	item      string
	got, want int
}

func (e *shortError) Error() string {
	return fmt.Sprintf("добыча %s: под бурами %d из %d", e.item, e.got, e.want)
}

// oilError — к месторождению нефти трассу не провести: в следующей попытке оно мимо.
type oilError struct {
	field cluster.Rect
	err   error
}

func (e *oilError) Error() string { return e.err.Error() }
func (e *oilError) Unwrap() error { return e.err }

// oilSkips — месторождений нефти, к которым трассу не провести, пропустить не больше стольких.
const oilSkips = 5

// wellsError — качалок в раскладке меньше нужного (на скважине бывает не встать): месторождений —
// больше.
type wellsError struct{ got, want int }

func (e *wellsError) Error() string {
	return fmt.Sprintf("нефть: качалок встало %d из %d", e.got, e.want)
}

// planRetry — что неудачная попытка плана меняет для следующей (planBase → planTry).
type planRetry struct {
	avoid map[world.Tile]bool // сюда кластерам не вставать (лента не провелась)
	boost map[string]float64  // цели добычи стадии 30 — × столько (по пятнам не сошлось)
	widen map[string]int      // коридор кластера шире на столько
	noOil []cluster.Rect      // месторождения нефти, к которым трассу не провести, — мимо
	wells int                 // скважин нефти не меньше
}

// planBase — план всей базы в зонах стадии 2; кластер, к которому ленту не провести, — в другое
// место в своей зоне (до planTries раз); руды под бурами меньше цели — ещё месторождений (до 3 раз).
func planBase(h *act.Hands) error {
	rough, err := LoadRough()
	if err != nil {
		return err
	}
	// свои постройки (точки стадии 10) — не помеха: их разберут (стадия 15)
	var own []world.Object
	for _, o := range h.World.Objects() {
		if o.Own {
			own = append(own, o)
		}
	}
	restore := h.World.Hide(own)
	defer restore()
	rt := &planRetry{avoid: map[world.Tile]bool{}, boost: map[string]float64{}, widen: map[string]int{}, wells: cluster.OilWells}
	avoid := rt.avoid
	short, shortWells := 0, 0
	fails := map[string]int{} // неудач подвода у кластера
	respared := 0             // сколько раз этажи шире и зоны заново
	for try := 0; ; try++ {
		base, l, err := planTry(h, rough, own, rt)
		if err == nil {
			return nil
		}
		var oe *oilError
		if errors.As(err, &oe) && len(rt.noOil) < oilSkips {
			rt.noOil = append(rt.noOil, oe.field)
			h.Say("%v — месторождение %v мимо, беру следующее", err, oe.field)
			continue
		}
		var we *wellsError
		if errors.As(err, &we) && shortWells < 3 {
			shortWells++
			rt.wells += we.want - we.got
			h.Say("%v — беру ещё месторождение (скважин не меньше %d)", err, rt.wells)
			continue
		}
		var se *shortError
		if errors.As(err, &se) && short < 3 {
			short++
			rt.boost[se.item] = max(rt.boost[se.item], 1) * float64(se.want) / float64(max(se.got, 1)) * 1.05
			h.Say("%v — беру ещё месторождений", se)
			continue
		}
		// на этаже не хватило места или попытки переложить кончились — этаж шире и зоны заново
		// (расстановка с коридорами по факту шире прикидки ширины)
		var ns *cluster.NoSiteError
		var tight *cluster.Cluster
		if errors.As(err, &ns) && ns.Cluster.Zone != nil {
			tight = ns.Cluster
		}
		if ce := (*cluster.ConnectError)(nil); errors.As(err, &ce) && try >= planTries-1 && ce.Consumer.Zone != nil {
			tight = ce.Consumer
		}
		if tight != nil && respared < spareTries {
			if f := cluster.FloorOf(tight); f > 0 {
				respared++
				floorSpare[f] += spareStep
				h.Say("%v — этаж %d шире на %d%%, зоны заново", err, f, floorSpare[f])
				if err := roughOnce(h); err != nil {
					return err
				}
				if rough, err = LoadRough(); err != nil {
					return err
				}
				clear(avoid)
				try = -1 // попытки переложить — заново: зоны другие
				continue
			}
		}
		var ce *cluster.ConnectError
		if !errors.As(err, &ce) || try >= planTries-1 {
			if base != nil {
				for _, c := range base.Sorted() {
					h.Log("стоит: %s @ %v (MinX %v)", c.Name, c.Area, ptrInt(c.MinX))
				}
				planDraw(h, rough.Zones, base, l, planImage()) // что вышло — на картинку
				if ce != nil {
					drawAround(h, rough.Zones, base, l, ce.Consumer, image("plan-fail"))
				}
				var ns *cluster.NoSiteError
				if errors.As(err, &ns) && ns.Cluster.Zone != nil { // зона, где не нашлось места, — крупно
					z := *ns.Cluster.Zone
					x, y := z.Center()
					drawPlan(h, rough.Zones, base, l, world.Tile{X: int(x), Y: int(y)}, max(z.W(), z.H())/2+15, image("plan-fail-zone"))
				}
				for _, z := range rough.Zones { // подводы с добычи — у головы bus-1
					if z.Name == "bus-1" {
						drawPlan(h, rough.Zones, base, l, z.Rect.Min, 60, image("plan-head"))
					}
				}
			}
			return err
		}
		if base != nil && l != nil {
			drawAround(h, rough.Zones, base, l, ce.Consumer, image(fmt.Sprintf("plan-fail%d-%d", respared, try)))
		}
		c := ce.Consumer
		fails[c.Name]++
		// коридор по факту (cluster.md §3): не проложилось — у кластера в зоне коридор шире на 1 и
		// заново; второй отказ — ещё шире и в другое место; дальше — место с запасом 15 тайлов под
		// запрет (соседнее место в той же стопке не помогает — как в плане жёлтой); подвод добычи не
		// провёлся — место блока сразу под запрет: блок встанет на другом пятне (238178543: камень
		// за 500 тайлов от головы bus-1 через всю базу)
		if c.Zone != nil && c.Kind != cluster.Mine && fails[c.Name] <= 2 {
			rt.widen[c.Name]++
			h.Say("кластер %s на %v: %v — коридор шире на 1", c.Name, c.Area, ce.Err)
			if fails[c.Name] == 1 {
				continue
			}
		} else {
			h.Say("кластер %s на %v: %v — ищу другое место", c.Name, c.Area, ce.Err)
		}
		a := c.Area
		if fails[c.Name] >= 3 {
			a = a.Grow(15)
		}
		for y := a.Min.Y; y < a.Max.Y; y++ {
			for x := a.Min.X; x < a.Max.X; x++ {
				avoid[world.Tile{X: x, Y: y}] = true
			}
		}
	}
}

// ptrInt — значение или «—» (журнал).
func ptrInt(p *int) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprint(*p)
}

// planTry — одна попытка плана всей базы; удалось — план записан.
func planTry(h *act.Hands, rough *RoughPlan, own []world.Object, rt *planRetry) (*cluster.Base, *layout.Layout, error) {
	pr := h.Protos
	base := cluster.NewBase(pr, h.World, world.Tile{})
	base.NoOil = rt.noOil
	base.Log = h.Log
	base.Avoid = rt.avoid
	boost := rt.boost
	base.Keep = planStart(own) // стартовые точки не задевать
	var bus1, power *cluster.Zone
	for i, z := range rough.Zones {
		switch {
		case z.Name == "bus-1":
			bus1 = &rough.Zones[i]
		case z.Kind == cluster.ZonePower:
			power = &rough.Zones[i]
		}
	}
	if bus1 == nil {
		return nil, nil, fmt.Errorf("нет зоны bus-1")
	}
	// добыча стадий 16–17 — блоки на ближних пятнах до цели под бурами; уголь станции — своим
	// блоком у станции (cluster.md §2а «Станция»)
	var cs []*cluster.Cluster
	for _, b := range cluster.Builds {
		if b.Kind != cluster.Mine || b.Stage >= 30 || b.Product == "crude-oil" {
			continue
		}
		c := b.Cluster(pr)
		if c.ToPower && power != nil {
			x, y := power.Rect.Center()
			c.Near = &world.Tile{X: int(x), Y: int(y)}
		}
		cs = append(cs, c)
	}
	cs = append(cs, planClusters(pr)...)
	for _, c := range cs { // имена — как дал бы PlaceZoned: по ним коридоры шире (planBase)
		if c.Name == "" {
			c.Name = base.Name(c.Product, cs)
		}
		c.Border += rt.widen[c.Name]
	}
	var tank *cluster.Cluster
	cs = slices.DeleteFunc(cs, func(c *cluster.Cluster) bool {
		if c.Kind == cluster.Tank { // склад нефти — потом, когда стоят месторождения (вне зон)
			tank = c
			return true
		}
		return false
	})
	_, buses, err := base.PlaceZoned(cs, rough.Zones)
	if err != nil {
		return base, nil, err
	}
	// добыча стадии 30 (в плане сразу: место, подвод, лента шины): остаток пятен первых блоков и
	// новые месторождения до цели, камень
	later, err := laterMines(base, rough, boost)
	if err != nil {
		return base, nil, err
	}
	h.Log("добыча стадии 30: %d кластеров", len(later))
	// нефть — не меньше 30 скважин, месторождения целиком, ближние к голове bus-1 первыми
	zones, oil, err := base.MoreOilWells(rough.Zones, rt.wells)
	if len(zones) > len(rough.Zones) {
		rough.Zones = zones
		if err := writeState(roughFile, rough); err != nil {
			return base, nil, err
		}
	}
	if err != nil {
		return base, nil, err
	}
	for _, c := range oil {
		c.Stage = 34
	}
	// склад нефти — 30 цистерн под bus-1 у её головы, левее хранения (cluster.md §2а «Нефть»)
	if tank == nil {
		return base, nil, fmt.Errorf("в таблице нет склада нефти")
	}
	tank.Name = "oil-tank-1"
	tank.Near = &world.Tile{X: bus1.Rect.Min.X + 10, Y: bus1.Rect.Max.Y + 12}
	if _, _, err := base.PlaceZoned([]*cluster.Cluster{tank}, rough.Zones); err != nil {
		return base, nil, err
	}
	l, err := base.Layout()
	if err != nil {
		return base, l, err
	}
	// блоки стадий 16–17 — дальше по своему пятну до цели стадии 30 (за вычетом новых блоков
	// стадии 30): буры сверх цели стадии 16–17 — на стадию 30 (тег «later»)
	_, total := oreTargets()
	for _, c := range base.Clusters {
		if c.Kind != cluster.Mine || c.IsOil() || c.Later || c.ToPower {
			continue
		}
		want := int(float64(total[c.Product]) * max(boost[c.Product], 1))
		for _, o := range base.Clusters {
			if o.Kind == cluster.Mine && o.Product == c.Product && o.Later {
				want -= o.OreUnder
			}
		}
		if err := base.FillMineTo(l, c, want, cluster.LaterTag(c.Name)); err != nil {
			return base, l, fmt.Errorf("добыча %s до цели: %w", c.Name, err)
		}
	}
	// качалок — по факту раскладки (на скважине бывает не встать): меньше 30 — ещё месторождение
	jacks := 0
	for _, pl := range l.List {
		if pl.Name == "pumpjack" {
			jacks++
		}
	}
	if jacks < cluster.OilWells {
		return base, l, &wellsError{jacks, cluster.OilWells}
	}
	// под бурами — по факту раскладки: меньше цели — ещё месторождений и заново
	if err := oreShort(base.Clusters, boost); err != nil {
		return base, l, err
	}
	if err := base.ConnectZoned(l, buses); err != nil {
		return base, l, err
	}
	// нефтетрассы: от каждого месторождения — в склад нефти через насос на входе (соединения
	// склада заняты — через свой насос в трубу соседней трассы у склада)
	for _, c := range oil {
		if err := base.OilTrunk(l, c, tank); err != nil {
			return base, l, &oilError{c.Area, err}
		}
		h.Say("нефтетрасса %s → %s", c.Name, tank.Name)
	}
	// вода химии — своя помпа и своя труба в bus-1 (stages.md, стадия 35), до последнего
	// потребителя
	end := 0
	for _, c := range base.Clusters {
		if c.Kind != cluster.Mine && slices.ContainsFunc(c.In, func(f cluster.Flow) bool { return f.Item == "water" }) && c.Water == nil {
			end = max(end, c.Area.Max.X+10)
		}
	}
	if end > 0 {
		if _, err := base.WaterLane(l, buses, end); err != nil {
			return base, l, err
		}
	}
	// трубы шин: все жидкостные порты (выходы — раньше входов) и склад нефти через насос на
	// выходе; потом — под землю, где к ним ничего не подходит; насосы — не реже чем через 290 труб
	if err := base.FluidPipes(l, buses, base.Clusters, cluster.FluidBus); err != nil {
		return base, l, err
	}
	cluster.UnderPipes(l, buses, nil)
	if err := splitPipes(h, l); err != nil {
		return base, l, err
	}
	var busNames []string
	for _, b := range buses {
		busNames = append(busNames, b.Name)
	}
	// делители балансировки между лентами пары — в плане (cluster.md §5)
	if n := cluster.BalancePlan(l, busNames); n > 0 {
		h.Say("план: +%d делителей между лентами одного предмета", n)
	}
	for _, e := range l.Validate() {
		if strings.Contains(e, "столбы не связаны") {
			continue // связность столбов — не здесь: питание каждая стадия тянет сама
		}
		var x, y float64 // чья постройка — в ошибку (разбор): «{x y}» или «(x, y)»
		ok := false
		if i := strings.Index(e, "{"); i >= 0 {
			_, err := fmt.Sscanf(e[i:], "{%g %g}", &x, &y)
			ok = err == nil
		}
		if i := strings.Index(e, "("); !ok && i >= 0 {
			_, err := fmt.Sscanf(e[i:], "(%g, %g)", &x, &y)
			ok = err == nil
		}
		if ok {
			t := world.Tile{X: int(math.Floor(x)), Y: int(math.Floor(y))}
			if p := l.At(t); p != nil {
				e += " (" + p.Tag + ")"
			}
			drawPlan(h, rough.Zones, base, l, t, 30, image("plan-err"))
		}
		return base, l, fmt.Errorf("раскладка: %v", e)
	}
	all := base.Sorted()
	for _, bus := range buses { // шины — в план кластерами (теги лент c/<шина>)
		all = append(all, &cluster.Cluster{Name: bus.Name, Kind: cluster.Bus, Product: "bus", Area: bus.Rect, Rect: bus.Rect})
	}
	if err := flowCheck(h, l, all); err != nil {
		return base, l, err
	}
	for _, c := range all {
		h.Say("кластер %v", c)
	}
	if err := planDraw(h, rough.Zones, base, l, planImage()); err != nil {
		return base, l, err
	}
	drawPlan(h, rough.Zones, base, l, bus1.Rect.Min, 70, image("plan-head")) // подводы у головы bus-1
	for _, c := range base.Clusters {                                        // нефтехимия и лаборатории крупно
		if c.Kind == cluster.Tank || c.Kind == cluster.Lab || c.IsRefinery() || c.IsRefineryAdv() || c.IsChem() {
			drawAround(h, rough.Zones, base, l, c, image("plan-"+c.Name))
		}
	}
	return base, l, writeState(planFile, ClusterPlan{Clusters: all, Placed: len(l.List), Layout: l.List, Image: planImage()})
}

// laterMines — добыча стадии 30 (Later: в плане сразу, строится на стадии 30): железо, медь, уголь
// шины — месторождения целиком до цели под бурами (остаток пятен первых блоков — в счёт), камень —
// до своей цели; boost — поправка цели (оценка по пятнам не сошлась).
func laterMines(base *cluster.Base, rough *RoughPlan, boost map[string]float64) ([]*cluster.Cluster, error) {
	first, total := oreTargets()
	var res []*cluster.Cluster
	for _, it := range slices.Sorted(maps.Keys(total)) {
		want := int(float64(total[it]) * max(boost[it], 1))
		var like *cluster.Cluster
		have := 0
		for _, c := range base.Clusters {
			if c.Kind == cluster.Mine && c.Product == it && !c.ToPower {
				like = c
				have += int(float64(base.OreIn(c)) * cluster.MineCover) // дополнится по своему пятну
			}
		}
		have = min(have, want) // своего пятна хватает — новых блоков нет
		if like == nil {       // ресурса нет в первых блоках (камень) — образец из таблицы
			if first[it] > 0 {
				return nil, fmt.Errorf("в плане нет добычи %s", it)
			}
			like = &cluster.Cluster{Kind: cluster.Mine, Product: it, Reserve: 1,
				Units: []cluster.Unit{{Recipe: "mine:" + it, Machine: "electric-mining-drill", Machines: 1}}}
		}
		// зон — на цель с запасом и на уголь станции (его блок — на тех же пятнах)
		power := 0
		for _, b := range cluster.Builds {
			if b.Kind == cluster.Mine && b.Product == it && b.ToPower {
				power += b.Ore
			}
		}
		if zones := base.MoreMineZones(rough.Zones, it, float64(want+power)/cluster.MineCover*1.1); len(zones) > len(rough.Zones) {
			rough.Zones = zones
			if err := writeState(roughFile, rough); err != nil {
				return nil, err
			}
		}
		ms, err := base.MoreMines(like, rough.Zones, have, want)
		// в зонах не добралось (пятна заняты, остатки мимо соседей — мелкие) — ещё зона добычи и
		// заново, до 3 раз
		for k := 0; err != nil && k < 3; k++ {
			got := have
			for _, m := range ms {
				got += int(float64(base.OreIn(m)) * cluster.MineCover)
			}
			zoneOre := 0.0
			for _, z := range rough.Zones {
				zoneOre += z.Resources[it]
			}
			zones := base.MoreMineZones(rough.Zones, it, zoneOre+float64(want-got)/cluster.MineCover*1.5)
			if len(zones) == len(rough.Zones) {
				break
			}
			rough.Zones = zones
			if err := writeState(roughFile, rough); err != nil {
				return nil, err
			}
			var more []*cluster.Cluster
			more, err = base.MoreMines(like, rough.Zones, got, want)
			ms = append(ms, more...)
		}
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			m.Later, m.Stage = true, 30
		}
		res = append(res, ms...)
	}
	return res, nil
}

// oreShort — под бурами (по раскладке) меньше цели: ресурс, сколько и цель (shortError).
func oreShort(cs []*cluster.Cluster, boost map[string]float64) error {
	_, total := oreTargets()
	for _, it := range []string{"iron-ore", "copper-ore", "coal", "stone"} {
		got := 0
		for _, c := range cs {
			if c.Kind == cluster.Mine && c.Product == it && !c.ToPower {
				got += c.OreUnder
			}
		}
		if got < total[it] {
			return &shortError{it, got, total[it]}
		}
	}
	return nil
}
