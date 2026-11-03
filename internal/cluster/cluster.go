// Package cluster — кластерная база (cluster.md, zones.md): кластер — прямоугольник на карте с
// известными входами и выходами (шт/с, Вт). Что ставим — таблица Builds; зоны — MineZones и
// PlaceLayered, места кластеров в зонах — PlaceZoned, связи — ConnectZoned (всё — стадия 2).
package cluster

import (
	"fmt"
	"math"
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
	Tank    Kind = "tank"    // склад жидкости: блок цистерн (нефть, газ)
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
	// инсертером прямо в основной), SubPer — сборщиков промежуточного на один основной (1–2);
	// тройка — второй справа от основного, рецепт Sub2 ("" — тот же Sub)
	Main   string `json:"main,omitempty"`
	Sub    string `json:"sub,omitempty"`
	Sub2   string `json:"sub2,omitempty"`
	SubPer int    `json:"sub_per,omitempty"`
	// SubMid — тройка с промежуточным посередине: один Sub кормит два Main (ракетное топливо,
	// rocket.go)
	SubMid bool `json:"sub_mid,omitempty"`
	// SubChest — промежуточное (рецепт Sub) ещё и в сундук бота: из первого сборщика Sub
	// инсертер в сундук в голове ряда (трубы двигателей — stages.md, стадия 38)
	SubChest string  `json:"sub_chest,omitempty"`
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
	// Later — в плане сразу (место, подвод, лента шины), строится позже (будущая добыча до 2M,
	// камень, кирпич — стадия 30; zones.md)
	Later bool `json:"later,omitempty"`
	// Stage — стадия, что строит кластер (stages.md, строки «Ставит»; cluster.Builds); 0 — старый
	// план по стадиям проектов
	Stage int `json:"stage,omitempty"`
	// ToPower — добыча угля станции: один выход, своя лента прямо к станции, мимо шины (cluster.md
	// §2а «Станция»)
	ToPower bool `json:"to_power,omitempty"`
	// Border — бордюр кластера в зоне по факту (cluster.md §3: коридор между соседями — сумма их
	// бордюров, не меньше 2); 0 — ZoneBorder
	Border int `json:"border,omitempty"`
	// Back — ряд в обратную сторону (голова — у Max: снизу у вертикального, справа у
	// горизонтального); у блока добычи — голова к шине ресурсов
	Back bool `json:"back,omitempty"`
	// Upright — ряд только вдоль Y, головой вниз (в зоне производства — поперёк шин, входы — к
	// шине снизу, выход — к шине сверху; zones.md)
	Upright bool `json:"upright,omitempty"`
	// HeadAbove — голова ряда — к шине сверху (как у лабораторий): предметы на входе — с шины
	// над этажом (серная кислота: сера и железо с bus-2 над переработкой)
	HeadAbove bool `json:"-"`
	// Need — мест по темпу (до минимума MinUnits; прикидка ширины этажей)
	Need int `json:"-"`
	// MinX — место не левее (в зоне — ниже по течению шины, чем поставщики входов; zones.md)
	MinX *int `json:"-"`
	// MaxX — правый край площади не правее (шины снизу и сверху зоны кончаются: zones.md — «нужно
	// только, чтобы шины были сверху и снизу зоны»)
	MaxX *int `json:"-"`
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

// Station — станция на boilers бойлеров (cluster.md §2а «Станция»): строится сразу вся.
func Station(p *knowledge.Prototypes, boilers int) *Cluster {
	W := float64(boilers) * p.Entities["boiler"].MaxEnergyUsage * 60
	cs, err := AddPower(p, []*Cluster{{PowerW: W}}, 1, nil)
	var c *Cluster
	for _, o := range cs {
		if o.Kind == Power {
			c = o
		}
	}
	if err != nil || c == nil {
		return nil
	}
	c.Count = max(boilers, c.Count)
	c.Room = c.Count
	return c
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

// Steel — кластер стали из руды (cluster.md §2а «Сталь»): pairs пар печей «пластины → сталь».
// Потоки — по печам второго уровня (как у всей плавки).
func Steel(p *knowledge.Prototypes, pairs int) *Cluster {
	const furnace = "stone-furnace"
	n := float64(pairs)
	speed := p.Entities[furnace].CraftingSpeed * upgrade(p, furnace)
	plate, steel := p.Recipes["iron-plate"], p.Recipes["steel-plate"]
	plates := n * speed / plate.Energy // пластин/с у печей пластин (столько же ест печь стали)
	c := &Cluster{Kind: Smelt, Product: "steel-plate", Reserve: 1,
		Units: []Unit{{Recipe: "iron-plate", Machine: furnace, Machines: n}, {Recipe: "steel-plate", Machine: furnace, Machines: n}},
		In:    []Flow{{"coal", 2 * n * p.Entities[furnace].EnergyUsage * 60 / 4e6}, {"iron-ore", plates}},
		Out:   []Flow{{"steel-plate", n * speed / steel.Energy}}}
	c.Cross, c.Pitch, c.Head = steelCross, 3, 1
	c.Count, c.Room, c.Need = pairs, pairs, pairs
	return c
}

// TakesAbove — кластер берёт и с шины над своим этажом: сборка (снабжение), лаборатории (на этаже
// конечного продукта — пакеты с шины конечных над собой; zones.md).
func (c *Cluster) TakesAbove() bool { return c.Kind == Make || c.Kind == Lab }

// IsSteel — кластер стали (пары печей, свой шаблон).
func (c *Cluster) IsSteel() bool { return c.Kind == Smelt && c.Product == "steel-plate" }

// StorageCluster —склад мусора бота.
func StorageCluster() *Cluster {
	c := &Cluster{Kind: Storage, Product: "storage", Reserve: 1, Units: []Unit{{Recipe: "store", Machine: StorageChest, Machines: 8}}}
	c.Cross, c.Pitch, c.Head = geometry(Storage)
	c.Count, c.Room = 8, 8
	return c
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
// пар): жёсткие пределы, cluster.md §2 «Размер» (без потолка база шире на 20% — 26.10);
// длиннее глубины зоны — PlaceZoned режет на кластеры.
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
	if c.Kind == Make && len(c.In) >= 3 { // третий вход — лента C за выходом (thirdIn), четвёртый — подача боком в неё
		c.Cross += len(c.In) - 2
	}
	// буферов входа нет (cluster.md §6), кроме угля станции: ящики в голове ряда
	if c.Kind == Power {
		c.Head = bufferHead
	}
	if c.SubChest != "" { // сундук промежуточного для бота — в голове, у первого сборщика
		c.Head = 2
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
	case Smelt, Make: // по лентам (наука — по темпу), от MinUnits до MaxUnits (cluster.md §2 «Размер»);
		// длиннее глубины зоны — PlaceZoned режет на кластеры
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
	if c.Kind == Lab { // блок сразу в конечном виде, никогда не расширяем (cluster.md §2а «Лаборатории»)
		c.Room = c.Count
	}
}
