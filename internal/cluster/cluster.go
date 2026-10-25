// Package cluster — кластерная база (cluster.md, zones.md): кластер — прямоугольник на карте с
// известными входами и выходами (шт/с, Вт). Split режет план калькулятора на кластеры; зоны —
// MineZones и PlaceLayered (стадия 4); места кластеров в зонах — PlaceZoned, связи —
// ConnectZoned (стадия 5), дописывание — ConnectMore, рост — GrowRow/Extra (стадия 6).
package cluster

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/calc"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Kind — вид кластера.
type Kind string

const (
	Mine    Kind = "mine"    // добыча: на месторождении, out — руда лентой
	Smelt   Kind = "smelt"   // плавка
	Make    Kind = "make"    // сборка, химия
	Power   Kind = "power"   // паровая станция: in — уголь, out — Вт
	Lab     Kind = "lab"     // лаборатории: только in
	Storage Kind = "storage" // мусор бота
	Supply  Kind = "supply"  // снабжение стройки: пластины с линий в ящики, бот берёт на крафт
)

// Margin — запас расчёта: лучше перепроизвести.
const Margin = 1.2

// Flow — поток предмета, шт/с.
type Flow struct {
	Item string  `json:"item"`
	Rate float64 `json:"rate"`
}

// Unit — рецепт внутри кластера и его машины.
type Unit struct {
	Recipe   string  `json:"recipe"`
	Machine  string  `json:"machine"`
	Machines float64 `json:"machines"` // дробно, с запасом Margin
}

// Cluster — кластер: что делает и сколько, размер ряда; Rect/Area — после Base.Place.
type Cluster struct {
	Name    string `json:"name"`
	Kind    Kind   `json:"kind"`
	Product string `json:"product"`
	Units   []Unit `json:"units"`
	// пара прямой подачи: Main — основной рецепт, Sub — промежуточный (его сборщик кладёт
	// инсертером прямо в основной), SubPer — сборщиков Sub на один основной (1–2)
	Main     string  `json:"main,omitempty"`
	Sub      string  `json:"sub,omitempty"`
	SubPer   int     `json:"sub_per,omitempty"`
	In       []Flow  `json:"in,omitempty"`
	Out      []Flow  `json:"out,omitempty"`
	Internal []Flow  `json:"internal,omitempty"` // делается и тратится внутри
	PowerW   float64 `json:"power_w,omitempty"`  // потребление (у Power — выработка)
	Reserve  float64 `json:"reserve"`            // во сколько раз можно поднять out без переезда
	// ряд: Cross — ширина поперёк, Pitch — шаг машины вдоль, Count — машин в ряду сейчас,
	// Room — мест в ряду с запасом
	Cross int `json:"cross"`
	Pitch int `json:"pitch"`
	Head  int `json:"head"` // столбец портов в начале ряда
	Count int `json:"count"`
	Room  int `json:"room"`
	// порты лент и вход воды станции — после Interior
	Ports []Port       `json:"ports,omitempty"`
	Water *layout.Conn `json:"water,omitempty"`
	// Rect — занятое сейчас, Area — с запасом роста (коридор 6 тайлов — вокруг Area)
	Rect     Rect `json:"rect"`
	Area     Rect `json:"area"`
	Vertical bool `json:"vertical,omitempty"` // ряд вдоль Y
	// Ore — добыча блоком на пятно (cluster.md §2а «Добыча»): предел руды под бурами (0 — ещё
	// не размещена: OreFirst); OreUnder — сколько руды под поставленными
	Ore      int `json:"ore,omitempty"`
	OreUnder int `json:"ore_under,omitempty"`
	// Back — ряд в обратную сторону (голова — у Max: снизу у вертикального, справа у
	// горизонтального); у блока добычи — голова к шине ресурсов
	Back bool `json:"back,omitempty"`
	// Upright — ряд только вдоль Y, головой вниз (в зоне производства — поперёк шин, входы — к
	// шине снизу, выход — к шине сверху; zones.md)
	Upright bool `json:"upright,omitempty"`
	// Need — мест по темпу (до минимума MinUnits; прикидка ширины этажей)
	Need int `json:"-"`
	// MinX — место не левее (в зоне — ниже по течению шины, чем поставщики входов; zones.md)
	MinX *int `json:"-"`
	// Near — куда тянуть кластер при выборе места (nil — к поставщикам или центру базы)
	Near *world.Tile `json:"-"`
	// Zone — зона (zones.md), в которой кластер: место — только внутри неё, бордюр — ZoneBorder
	Zone *Rect `json:"zone,omitempty"`
}

func (c *Cluster) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %d×%s", c.Name, c.Kind, c.Count, c.machine())
	if c.Room > c.Count {
		fmt.Fprintf(&b, " (место до %d)", c.Room)
	}
	fmt.Fprintf(&b, " in %s out %s", flows(c.In), flows(c.Out))
	if c.PowerW > 0 {
		fmt.Fprintf(&b, " %.0f кВт", c.PowerW/1e3)
	}
	if len(c.Internal) > 0 {
		fmt.Fprintf(&b, " внутри %s", flows(c.Internal))
	}
	if c.Area != (Rect{}) {
		fmt.Fprintf(&b, " @ %v", c.Area)
	}
	return b.String()
}

// machine — основная машина кластера (самая многочисленная).
func (c *Cluster) machine() string {
	if c.Kind == Power {
		return "boiler"
	}
	best, n := "", -1.0
	for _, u := range c.Units {
		if u.Machines > n {
			best, n = u.Machine, u.Machines
		}
	}
	return best
}

func flows(fs []Flow) string {
	if len(fs) == 0 {
		return "—"
	}
	var s []string
	for _, f := range fs {
		s = append(s, fmt.Sprintf("%s %.2f/с", f.Item, f.Rate))
	}
	return strings.Join(s, ", ")
}

// Split режет план калькулятора на кластеры. Добыча — по ресурсу, плавка — по продукту,
// прочее — по продукту (с Merge промежуточный продукт, который тратит ровно один кластер
// сборки и он не цель, делается внутри него — internal). Топливо (уголь печей и бойлеров) — в
// кластер добычи угля (свободный уголь have — первым). reserve — запас роста out. separate —
// продукты, которые всегда отдельным кластером (не внутрь потребителя).
func Split(p *knowledge.Prototypes, plan *calc.Plan, reserve float64, have map[string]float64, separate ...string) ([]*Cluster, error) {
	nodes, owner := splitShared(p, plan)
	index := map[*calc.Node]int{}
	for i, n := range nodes {
		index[n] = i
	}
	kind := make([]Kind, len(nodes))
	product := make([]string, len(nodes))
	parent := make([]int, len(nodes))
	for i, n := range nodes {
		parent[i] = i
		switch {
		case strings.HasPrefix(n.Recipe, "mine:"):
			kind[i] = Mine
		case strings.HasPrefix(n.Recipe, "pump:"), strings.HasPrefix(n.Recipe, "source:"):
			kind[i] = "" // вода — внутри потребителя; source — руками, не кластер
		case p.Recipes[n.Recipe].Category == "smelting":
			kind[i] = Smelt
		default:
			kind[i] = Make
		}
		product[i] = mainOut(n)
	}
	find := func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	// промежуточное (не цель) с одним потребителем-сборкой — внутрь него парой прямой подачи
	// (сборщик промежуточного → инсертер → основной), если у пары остаётся не больше двух
	// входов с лент (шаблон — одна лента, две полосы)
	subOf := map[int]int{}
	for i := range nodes {
		if kind[i] != Make || plan.Targets[product[i]] > 0 || slices.Contains(separate, product[i]) {
			continue
		}
		var cons []int
		if o, ok := owner[nodes[i]]; ok {
			cons = []int{index[o]} // доля общего промежуточного — своему потребителю
		} else {
			for j, m := range nodes {
				if j != i && m.In[product[i]] > 0 {
					cons = append(cons, j)
				}
			}
		}
		if len(cons) != 1 || kind[cons[0]] != Make || parent[cons[0]] != cons[0] {
			continue
		}
		j := cons[0]
		if _, has := subOf[j]; has {
			continue
		}
		in := map[string]bool{}
		for it := range nodes[j].In {
			if it != product[i] {
				in[it] = true
			}
		}
		for it := range nodes[i].In {
			if it != "water" {
				in[it] = true
			}
		}
		if len(in) > 2 {
			continue
		}
		parent[i], subOf[j] = j, i
	}
	groups := map[int][]int{}
	var roots []int
	for i := range nodes {
		if kind[i] == "" {
			continue
		}
		r := find(i)
		if groups[r] == nil {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], i)
	}
	var res []*Cluster
	var fuel float64 // уголь печей кластеров (бойлеры — ниже)
	for _, r := range roots {
		c := &Cluster{Kind: kind[r], Product: product[r], Reserve: reserve}
		if si, ok := subOf[r]; ok {
			c.Main, c.Sub = nodes[r].Recipe, nodes[si].Recipe
			c.SubPer = min(2, max(1, int(math.Ceil(nodes[si].Machines/math.Max(nodes[r].Machines, 1e-9)-1e-9))))
		}
		in, out := map[string]float64{}, map[string]float64{}
		for _, i := range groups[r] {
			n := nodes[i]
			c.Units = append(c.Units, Unit{Recipe: n.Recipe, Machine: n.Machine, Machines: n.Machines * Margin})
			for it, v := range n.In {
				in[it] += v
			}
			for it, v := range n.Out {
				out[it] += v
			}
			c.PowerW += n.PowerW * Margin
			if n.Fuel > 0 {
				in["coal"] += n.Fuel
				fuel += n.Fuel
			}
		}
		// вода — помпой внутри
		if w := in["water"]; w > 0 {
			c.Internal = append(c.Internal, Flow{"water", w * Margin})
			delete(in, "water")
		}
		for it := range in {
			if d := math.Min(in[it], out[it]); d > 1e-9 {
				c.Internal = append(c.Internal, Flow{it, d * Margin})
				in[it] -= d
				out[it] -= d
			}
		}
		c.In, c.Out = toFlows(in), toFlows(out)
		res = append(res, c)
	}
	for _, c := range res {
		for i := range c.Out {
			c.Out[i].Rate *= Margin
		}
		for i := range c.In {
			c.In[i].Rate *= Margin
		}
		shape(p, c)
	}
	// уголь печей — в добычу угля
	if fuel > 0 {
		if err := addCoal(p, &res, fuel, reserve, have); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// AddPower — станция под потребление всех кластеров (с лабораториями и добычей), уголь
// бойлеров — в добычу угля. Вызывать после всех Split/Labs.
func AddPower(p *knowledge.Prototypes, cs []*Cluster, reserve float64, have map[string]float64) ([]*Cluster, error) {
	W := 0.0
	for _, c := range cs {
		W += c.PowerW
	}
	st := calc.SteamFor(p, W)
	if st.Boilers == 0 {
		return cs, nil
	}
	b := p.Entities["boiler"]
	eff := 1.0
	if b.Burner != nil && b.Burner.Effectivity > 0 {
		eff = b.Burner.Effectivity
	}
	coal := st.Boilers * b.MaxEnergyUsage * 60 / (coalJ * eff)
	water := st.Pumps * p.Entities["offshore-pump"].PumpingSpeed * 60
	c := &Cluster{Kind: Power, Product: "power", Reserve: reserve, PowerW: W,
		In:       []Flow{{"coal", coal}},
		Internal: []Flow{{"water", water}},
		Units: []Unit{
			{Recipe: "boiler", Machine: "boiler", Machines: st.Boilers},
			{Recipe: "steam-engine", Machine: "steam-engine", Machines: st.Engines},
		}}
	shape(p, c)
	cs = append(cs, c)
	// уголь бойлеров — в добычу (её буры тоже потребители, но малые — в запасе Margin)
	if err := addCoal(p, &cs, coal/Margin, reserve, have); err != nil {
		return nil, err
	}
	return cs, nil
}

// PowerStart — бойлеров станции с начала (по 2 двигателя).
const PowerStart = 3

// PumpBoilers — сколько бойлеров кормит одна помпа (вода: помпа / расход бойлера).
func PumpBoilers(p *knowledge.Prototypes) int {
	one := calc.SteamFor(p, p.Entities["steam-engine"].MaxEnergyProduction*60*2) // бойлер = 2 двигателя
	if one.Pumps <= 0 {
		return 20
	}
	return int(math.Floor(one.Boilers/one.Pumps + 1e-9))
}

// SizePower — станция cs — под одну помпу (cluster.md §2а «Станция»): столько бойлеров, сколько
// она кормит водой, по 2 двигателя; дальше — вторая станция.
func SizePower(p *knowledge.Prototypes, cs []*Cluster) {
	n := float64(PumpBoilers(p))
	st := calc.SteamFor(p, n*p.Entities["boiler"].MaxEnergyUsage*60)
	W := st.Engines * p.Entities["steam-engine"].MaxEnergyProduction * 60
	for _, c := range cs {
		if c.Kind != Power {
			continue
		}
		c.PowerW = W
		for i := range c.Units {
			switch c.Units[i].Machine {
			case "boiler":
				c.Units[i].Machines = st.Boilers
			case "steam-engine":
				c.Units[i].Machines = st.Engines
			}
		}
		shape(p, c)
	}
}

// coalJ — энергия угля, Дж.
const coalJ = 4e6

// addCoal — rate угля/с (без запаса) в добычу угля; нет её — новый кластер. have["coal"] —
// свободный уголь уже стоящих кластеров: берётся первым (и уменьшается).
func addCoal(p *knowledge.Prototypes, cs *[]*Cluster, rate, reserve float64, have map[string]float64) error {
	if v := have["coal"]; v > 0 {
		use := math.Min(v, rate*Margin)
		have["coal"] -= use
		rate -= use / Margin
		if rate <= 1e-9 {
			return nil
		}
	}
	var coal *Cluster
	for _, c := range *cs {
		if c.Kind == Mine && c.Product == "coal" {
			coal = c
		}
	}
	if coal == nil {
		drill, ok := p.BestDrill(p.Entities["coal"].ResourceCategory, p.NewAvailability(nil, true))
		if !ok {
			return fmt.Errorf("cluster: нет бура на уголь")
		}
		coal = &Cluster{Kind: Mine, Product: "coal", Reserve: reserve, Units: []Unit{{Recipe: "mine:coal", Machine: drill.Name}}}
		*cs = append(*cs, coal)
	}
	addFlow(&coal.Out, "coal", rate*Margin)
	r := p.Entities["coal"]
	d := p.Entities[coal.Units[0].Machine]
	if r.Mineable != nil && d.MiningSpeed > 0 {
		total := 0.0
		for _, f := range coal.Out {
			total += f.Rate
		}
		coal.Units[0].Machines = total * r.Mineable.MiningTime / d.MiningSpeed
		coal.PowerW = math.Ceil(coal.Units[0].Machines-1e-9) * d.EnergyUsage * 60
	}
	shape(p, coal)
	return nil
}

// splitShared — промежуточное сборки (не цель), которое тратят несколько сборок (шестерни —
// науке, лентам, инсертерам): узел делится на доли по потребителям — каждому своя пара прямой
// подачи. Копии узлов, план не меняется.
func splitShared(p *knowledge.Prototypes, plan *calc.Plan) ([]*calc.Node, map[*calc.Node]*calc.Node) {
	owner := map[*calc.Node]*calc.Node{} // копия → её потребитель
	var nodes []*calc.Node
	for _, n := range plan.Nodes {
		c := *n
		nodes = append(nodes, &c)
	}
	isMake := func(n *calc.Node) bool {
		return !strings.HasPrefix(n.Recipe, "mine:") && !strings.HasPrefix(n.Recipe, "pump:") && !strings.HasPrefix(n.Recipe, "source:") && p.Recipes[n.Recipe].Category != "smelting"
	}
	var out []*calc.Node
	for _, x := range nodes {
		prod := mainOut(x)
		var cons []*calc.Node
		for _, m := range nodes {
			if m != x && m.In[prod] > 0 {
				cons = append(cons, m)
			}
		}
		if !isMake(x) || plan.Targets[prod] > 0 || len(cons) < 2 {
			out = append(out, x)
			continue
		}
		all := true
		for _, m := range cons {
			all = all && isMake(m)
		}
		if !all {
			out = append(out, x)
			continue
		}
		total := x.Out[prod]
		for _, m := range cons {
			share := m.In[prod] / total
			c := &calc.Node{Recipe: x.Recipe, Machine: x.Machine, Crafts: x.Crafts * share, Machines: x.Machines * share,
				In: map[string]float64{}, Out: map[string]float64{}, PowerW: x.PowerW * share, Fuel: x.Fuel * share}
			for it, v := range x.In {
				c.In[it] = v * share
			}
			// выход копии — только этому потребителю (Out — по одному продукту)
			c.Out[prod] = m.In[prod]
			owner[c] = m
			out = append(out, c)
		}
	}
	return out, owner
}

// Labs — научный кластер: лаборатории под пакеты (шт/с) при среднем времени на пакет sec
// (единица исследования / пакетов в ней) — сколько лабораторий держат этот поток.
func Labs(p *knowledge.Prototypes, packs map[string]float64, sec, reserve float64) *Cluster {
	lab := p.Entities["lab"]
	speed := lab.ResearchingSpeed
	if speed == 0 {
		speed = 1
	}
	total := 0.0
	in := map[string]float64{}
	for it, v := range packs {
		in[it] = v * Margin
		total = math.Max(total, v)
	}
	c := &Cluster{Kind: Lab, Product: "lab", Reserve: reserve, In: toFlows(in),
		Units: []Unit{{Recipe: "research", Machine: "lab", Machines: total * sec / speed * Margin}}}
	c.PowerW = math.Ceil(c.Units[0].Machines) * lab.EnergyUsage * 60
	shape(p, c)
	return c
}

// StorageChest — сундук хранения: сразу железный, потом не менять (stages.md, стадия 13).
const StorageChest = "iron-chest"

// Steel — кластер стали из руды (cluster.md §2а «Сталь»): пары печей «пластины → сталь», сразу
// на MaxUnits пар (стали нужно тонны). Потоки — по печам второго уровня (как у всей плавки).
func Steel(p *knowledge.Prototypes, reserve float64) *Cluster {
	const furnace = "stone-furnace"
	n := float64(MaxUnits)
	speed := p.Entities[furnace].CraftingSpeed * upgrade(p, furnace)
	plate, steel := p.Recipes["iron-plate"], p.Recipes["steel-plate"]
	plates := n * speed / plate.Energy // пластин/с у печей пластин (столько же ест печь стали)
	c := &Cluster{Kind: Smelt, Product: "steel-plate", Reserve: reserve,
		Units: []Unit{{Recipe: "iron-plate", Machine: furnace, Machines: n}, {Recipe: "steel-plate", Machine: furnace, Machines: n}},
		In:    []Flow{{"coal", 2 * n * p.Entities[furnace].EnergyUsage * 60 / 4e6}, {"iron-ore", plates}},
		Out:   []Flow{{"steel-plate", n * speed / steel.Energy}}}
	c.Cross, c.Pitch, c.Head = steelCross, 3, 1
	c.Count, c.Room, c.Need = MaxUnits, MaxUnits, MaxUnits
	return c
}

// IsSteel — кластер стали (пары печей, свой шаблон).
func (c *Cluster) IsSteel() bool { return c.Kind == Smelt && c.Product == "steel-plate" }

// StorageCluster —склад мусора бота.
func StorageCluster() *Cluster {
	c := &Cluster{Kind: Storage, Product: "storage", Reserve: 1, Units: []Unit{{Recipe: "store", Machine: StorageChest, Machines: 8}}}
	c.Cross, c.Pitch, c.Head = geometry(Storage)
	c.Count, c.Room = 8, 8
	return c
}

// mainOut — продукт узла: самый большой выход.
func mainOut(n *calc.Node) string {
	best, v := "", -1.0
	for it, r := range n.Out {
		if r > v || r == v && it < best {
			best, v = it, r
		}
	}
	return best
}

func toFlows(m map[string]float64) []Flow {
	var res []Flow
	for it, v := range m {
		if v > 1e-9 {
			res = append(res, Flow{it, v})
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Item < res[j].Item })
	return res
}

func addFlow(fs *[]Flow, item string, rate float64) {
	for i := range *fs {
		if (*fs)[i].Item == item {
			(*fs)[i].Rate += rate
			return
		}
	}
	*fs = append(*fs, Flow{item, rate})
}

// upgrade — во сколько раз можно ускорить машину заменой на лучшую того же вида
// (сборщик 1 → 3, каменная печь → стальная/электро): запас роста out, который не требует места.
func upgrade(p *knowledge.Prototypes, machine string) float64 {
	e := p.Entities[machine]
	best := 0.0
	for _, o := range p.Entities {
		if o.Type == e.Type && o.CraftingSpeed > best && sameCats(e, o) {
			best = o.CraftingSpeed
		}
	}
	if e.CraftingSpeed == 0 || best <= e.CraftingSpeed {
		return 1
	}
	return best / e.CraftingSpeed
}

// sameCats — o умеет всё, что умеет e.
func sameCats(e, o knowledge.Entity) bool {
	has := map[string]bool{}
	for _, c := range o.CraftingCategories {
		has[c] = true
	}
	for _, c := range e.CraftingCategories {
		if !has[c] {
			return false
		}
	}
	return true
}

// MinUnits, MaxUnits — мест выхода в кластере плавки, производства и науки (машин; у пар —
// пар): жёсткие пределы, cluster.md §2 «Размер».
const (
	MinUnits = 5
	MaxUnits = 30
)

// Конечные машины и лента для расчёта размера (cluster.md §2 «Размер»): строятся пока первые
// на тех же местах, потом замена на месте.
var (
	tier2 = map[string]string{"stone-furnace": "steel-furnace", "assembling-machine-1": "assembling-machine-2"}
	// beltRate — красная лента, шт/с (по 15 на полосу)
	beltRate = 30.0
)

// IsScience — пакет науки (размер — по темпу, буфера выхода нет).
func IsScience(item string) bool { return strings.HasSuffix(item, "-science-pack") }

// byBelts — сколько основных машин (n — по темпу, машинами плана) прокормят ленты входа и
// вывезет лента выхода, если машины — второго уровня; с округлением вверх. Плавка: руда —
// своя лента целиком, уголь — своя; сборка: один вход — лента целиком, два — по полосе.
func byBelts(p *knowledge.Prototypes, c *Cluster, n float64) int {
	if n <= 0 {
		return 0
	}
	m := c.machine()
	k := 1.0 // во сколько раз быстрее машина второго уровня
	if t2, ok := tier2[m]; ok && p.Entities[m].CraftingSpeed > 0 {
		k = p.Entities[t2].CraftingSpeed / p.Entities[m].CraftingSpeed
	}
	best := math.Inf(1)
	limit := func(flows []Flow, cap func(item string) float64) {
		for _, f := range flows {
			if per := f.Rate / n * k; per > 0 {
				best = math.Min(best, cap(f.Item)/per)
			}
		}
	}
	limit(c.In, func(string) float64 {
		if c.Kind == Make && len(c.In) > 1 {
			return beltRate / 2
		}
		return beltRate
	})
	limit(c.Out, func(string) float64 { return beltRate })
	if math.IsInf(best, 1) {
		return 0
	}
	return int(math.Ceil(best - 1e-9))
}

// shape — размер ряда кластера (шаблоны — templates.go): Count — машин сейчас, Room — мест
// с запасом роста (часть роста дадут машины лучше — место только на остальное).
func shape(p *knowledge.Prototypes, c *Cluster) {
	c.Cross, c.Pitch, c.Head = geometry(c.Kind)
	if c.Kind == Storage {
		return
	}
	// один вход — буфер в начале ряда, до первой машины: ящики на u = 1, 3, …, 2n−1
	if c.Kind == Make && len(c.In) == 3 { // третий вход — ленты за выходом (thirdIn)
		c.Cross++
	}
	if len(c.In) == 1 && c.Kind != Mine || len(c.In) >= 2 && c.Kind == Make {
		// буферы входов — в голове ряда, по разные стороны ленты (cluster.md §6б)
		c.Head = 2 * bufferChests(p, c)
	}
	m := c.machine()
	n := 0.0
	for _, u := range c.Units {
		if u.Machine == m {
			n += u.Machines
		}
	}
	switch c.Kind {
	case Mine:
		n = n / 2 // пары буров через ленту
	case Power:
		for _, u := range c.Units {
			if u.Machine == "boiler" {
				n = u.Machines
			}
		}
	}
	if c.Sub != "" { // пары: шаг — основной + SubPer промежуточных, места — по основному
		c.Pitch = 4 + 4*c.SubPer
		for _, u := range c.Units {
			if u.Recipe == c.Main {
				n = u.Machines
			}
		}
	}
	c.Count = max(int(math.Ceil(n-1e-9)), 1)
	c.Need = c.Count
	switch c.Kind {
	case Smelt, Make: // по лентам (наука — по темпу), от MinUnits до MaxUnits (cluster.md §2 «Размер»)
		if !IsScience(c.Product) {
			c.Count = byBelts(p, c, n)
		}
		c.Count = min(max(c.Count, MinUnits), MaxUnits)
	case Lab:
		c.Count = max(c.Count, MinUnits)
	case Power: // с начала — не меньше PowerStart бойлеров (cluster.md §2а «Станция»)
		c.Count = max(c.Count, PowerStart)
	}
	room := c.Reserve / upgrade(p, m)
	c.Room = max(int(math.Ceil(float64(c.Count)*math.Max(room, 1)-1e-9)), c.Count)
	if c.Kind == Smelt || c.Kind == Make {
		c.Room = c.Count // кластер строится сразу целиком — запаса в ряду нет
	}
	if c.Kind == Lab { // блок сразу на все места (cluster.md §2а «Лаборатории»); в голове — ещё лента C и её инсертеры
		c.Room = max(labRoom, c.Count)
		c.Head = 2*bufferChests(p, c) + 2
	}
}
