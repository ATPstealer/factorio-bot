package cluster

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Рост кластера (cluster.md §7): вдоль своего ряда, в своём столбце зоны — площадь продлевается
// от конца ряда (голова с портами не двигается), новые машины — дальше по тому же шаблону;
// построенное не перестраивается, только дописывается.

// MainMachine — основная машина кластера: печь, сборщик, бур, бойлер.
func (c *Cluster) MainMachine() string {
	if c.Kind == Power {
		return "boiler"
	}
	return c.machine()
}

// Built — сколько основных машин кластера c в раскладке list; у пары — только основные (по
// рецепту Main: сборщики Sub — те же машины, считались бы вместе). Машина второго уровня на
// месте первой (Tier2) — та же основная.
func Built(list []layout.Placement, c *Cluster) int {
	n := 0
	m := c.MainMachine()
	for _, p := range list {
		if p.Tag != Tag(c.Name) || p.Name != m && Tier2[m] != p.Name {
			continue
		}
		if c.Main != "" && p.Recipe != c.Main {
			continue
		}
		n++
	}
	return n
}

// Need — сколько основных машин нужно кластеру c по кластерам плана cs того же вида и продукта
// (Split/AddPower по всей потребности базы, с запасом).
func Need(cs []*Cluster, c *Cluster) int {
	v := 0.0
	for _, o := range cs {
		if o.Kind != c.Kind || o.Product != c.Product {
			continue
		}
		m := o.MainMachine()
		for _, u := range o.Units {
			if u.Machine == m {
				v += u.Machines
			}
		}
	}
	return int(math.Ceil(v - 1e-9))
}

// GrowRow — нарастить кластер c вдоль ряда до n основных машин: площадь — дальше по ряду (в
// своей зоне, мимо чужих кластеров и построек раскладки l), внутренность — заново по шаблону,
// в l дописывается только новое. Не влезает — кластер как был и NoSiteError.
func (b *Base) GrowRow(l *layout.Layout, c *Cluster, n int) error {
	if n <= Built(l.List, c) {
		return nil
	}
	old := *c
	old.Units = append([]Unit(nil), c.Units...)
	old.Ports = append([]Port(nil), c.Ports...)
	fail := func(err error) error {
		*c = old
		return &NoSiteError{c, fmt.Errorf("cluster: %s не растёт вдоль ряда до %d: %w", c.Name, n, err)}
	}
	room := 0
	switch c.Kind {
	case Mine: // блок на пятно уже взял руду до предела — новое пятно
		return fail(fmt.Errorf("блок добычи — на пределе руды, нужно новое пятно"))
	case Smelt, Power, Make: // сборка — дальше по ряду тем же шаблоном (пары — парами)
		cnt := n
		if c.Sub != "" { // n — машин, в ряду — пары (основной + SubPer промежуточных)
			cnt = (n + c.SubPer) / (1 + c.SubPer)
		}
		if n > b.GrowCap(c) { // предел ряда (cluster.md §2, §2а «Станция»): остальное — новым кластером
			return fail(fmt.Errorf("больше предела ряда %d машин", b.GrowCap(c)))
		}
		c.Count, room = cnt, cnt
	default:
		return fail(fmt.Errorf("вид %s так не растёт", c.Kind))
	}
	c.Room = max(old.Room, room)
	c.Area = grown(old.Area, c)
	c.Rect = c.Area
	if err := b.extFree(l, c, old.Area); err != nil {
		return fail(err)
	}
	if err := b.growInterior(l, c, n); err != nil {
		return fail(err)
	}
	if b.g != nil {
		b.g.mark(b.occupied(c))
	}
	return nil
}

// FillMine — блок добычи c дописан на всё пятно в своей площади (stages.md, стадия 5:
// месторождения целиком): предел руды снят, буры — дальше по тому же шаблону, старые на месте.
func (b *Base) FillMine(l *layout.Layout, c *Cluster, tag string) error {
	if c.Kind != Mine {
		return fmt.Errorf("cluster: %s — не добыча", c.Name)
	}
	c.Ore = math.MaxInt32
	return b.growInterior(l, c, 0, tag)
}

// OreIn — руды ресурса c в площади блока добычи c (оценка: всё пятно в ней).
func (b *Base) OreIn(c *Cluster) int { return b.oreIn(c.Area, c.Product) }

// PowerNeed — сколько бойлеров (по 2 двигателя) нужно на все электрические машины раскладки l
// при полной работе (столбы — не в счёт): станция — по тому, что реально стоит в плане (буры на
// месторождениях целиком, НПЗ, химия), а не по темпу науки.
func PowerNeed(p *knowledge.Prototypes, l *layout.Layout) (int, float64) {
	w := 0.0
	for _, pl := range l.List {
		e := p.Entities[pl.Name]
		if e.Electric && !strings.HasSuffix(pl.Name, "electric-pole") && pl.Name != "substation" {
			w += math.Max(e.EnergyUsage, e.MaxEnergyUsage) * 60
		}
	}
	per := 2 * p.Entities["steam-engine"].MaxEnergyProduction * 60
	if per <= 0 {
		per = 1.8e6
	}
	return int(math.Ceil(w/per - 1e-9)), w
}

// ConnectStation — вторая станция st (cluster.md §2а «Станция»): вода — своей помпой, уголь —
// отводом делителем с ленты угля первой станции first.
func (b *Base) ConnectStation(l *layout.Layout, st, first *Cluster) error {
	// подход ко входу угля (4 тайла) — свободным, пока идёт труба воды (иначе она его займёт)
	var keep []world.Tile
	for _, p := range st.Ports {
		if !p.In {
			continue
		}
		for k, t := 0, p.T; k < 4; k, t = k+1, layout.Step(t, (p.Dir+8)%16) {
			if !l.Reserved[t] {
				l.Reserved[t] = true
				keep = append(keep, t)
			}
		}
	}
	unkeep := func() {
		for _, t := range keep {
			delete(l.Reserved, t)
		}
		keep = nil
	}
	defer unkeep()
	if st.Water != nil {
		_, err := l.WaterPump(st.Water.To, LogisticsTag+"/"+st.Name, func(pump layout.Placement) bool {
			if !l.CanPlace(pump, layout.AnyLand) {
				return false
			}
			l.Add(pump)
			_, err := l.RoutePipe(layout.Connections(l.P, pump)[0], *st.Water, LogisticsTag+"/"+st.Name)
			return err == nil
		})
		if err != nil {
			return &ConnectError{st, fmt.Errorf("вода к %s: %w", st.Name, err)}
		}
	}
	unkeep() // вода проложена — подход ко входу угля открыть под ленту
	var in Port
	ok := false
	for _, p := range st.Ports {
		if p.In && p.Item == "coal" {
			in, ok = p, true
		}
	}
	if !ok {
		return nil
	}
	var line []layout.Placement // лента угля на первую станцию
	for _, p := range l.List {
		if p.Tag == LogisticsTag+"/"+first.Name && p.Name == "transport-belt" {
			line = append(line, p)
		}
	}
	if err := SplitTap(l, line, in, LogisticsTag+"/"+st.Name, 8); err == nil {
		return nil
	}
	// не провести (станция далеко) — отвод с подвода угля в bus-1 (любого блока угля)
	lines := map[string][]layout.Placement{}
	for _, p := range l.List {
		if strings.HasPrefix(p.Tag, LogisticsTag+"/bus-1/coal") && p.Name == "transport-belt" {
			lines[p.Tag] = append(lines[p.Tag], p)
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(lines)) {
		if err := SplitTap(l, lines[tag], in, LogisticsTag+"/"+st.Name, 8); err == nil {
			return nil
		}
	}
	return &ConnectError{st, fmt.Errorf("уголь → %s: отвод не провести ни с ленты %s, ни с подводов угля", st.Name, first.Name)}
}

// GrowCap — до скольких основных машин кластер c растёт вдоль ряда (cluster.md §7 «Предел
// ряда»): сколько влезает в глубину его этажа, не больше MaxUnits, станция — бойлеров на
// одну помпу (§2а «Станция»); остальное — новыми кластерами (Extras). Сосед сверху в столбце
// — отдельный предел (extFree).
func (b *Base) GrowCap(c *Cluster) int {
	if c.Kind == Power {
		return PumpBoilers(b.P)
	}
	units := MaxUnits
	if f := floorOf(c); f > 0 && len(b.floors[f]) > 0 {
		units = min(max(c.FitUnits(b.floors[f][0].H()), MinUnits), MaxUnits)
	}
	if c.Sub != "" {
		return units * (1 + c.SubPer)
	}
	return units
}

// Extras — новые кластеры на недостающие n−built машин, каждый не больше step основных машин
// (предел ряда, GrowCap; один Extra резал бы до предела, и недостача терялась); добыча — одним
// блоком на новое пятно.
func Extras(p *knowledge.Prototypes, cs []*Cluster, c *Cluster, built, n, step int) ([]*Cluster, error) {
	if c.Kind == Mine {
		step = n - built
	}
	var res []*Cluster
	for built < n {
		m := min(n, built+step)
		x, err := Extra(p, cs, c, built, m)
		if err != nil {
			return nil, err
		}
		res = append(res, x)
		built = m
	}
	return res, nil
}

// Extra — новый кластер того же продукта, что c, на недостающие n−Built машин (рост вдоль ряда
// не влез): по кластеру плана cs того же вида и продукта, потоки — по доле. Плавка, сборка и
// добыча (их связывает ConnectMore; станция — нет).
func Extra(p *knowledge.Prototypes, cs []*Cluster, c *Cluster, built, n int) (*Cluster, error) {
	if c.Kind != Smelt && c.Kind != Make && c.Kind != Mine {
		return nil, fmt.Errorf("cluster: %s: новый кластер вида %s — пока не умею", c.Name, c.Kind)
	}
	for _, o := range cs {
		if o.Kind != c.Kind || o.Product != c.Product {
			continue
		}
		m := 0.0
		for _, u := range o.Units {
			if u.Machine == o.MainMachine() {
				m += u.Machines
			}
		}
		if m == 0 {
			break
		}
		x := scaleCluster(o, float64(n-built)/m)
		x.Name, x.Area, x.Rect, x.Ports, x.Zone = "", Rect{}, Rect{}, nil, nil
		shape(p, x)
		if x.Kind == Mine { // новое пятно — блоком, вторая итерация (cluster.md §2а «Добыча»)
			x.Ore = OreNext
		}
		return x, nil
	}
	return nil, fmt.Errorf("cluster: в плане нет %s %s", c.Kind, c.Product)
}

// grown — площадь a, продлённая до длины ряда c (голова — на месте).
func grown(a Rect, c *Cluster) Rect {
	L := c.Len()
	switch {
	case !c.Vertical:
		a.Max.X = max(a.Max.X, a.Min.X+L)
	case c.Upright:
		a.Min.Y = min(a.Min.Y, a.Max.Y-L)
	default:
		a.Max.Y = max(a.Max.Y, a.Min.Y+L)
	}
	return a
}

// extFree — прибавка площади c (сверх old) свободна: в своей зоне, с бордюром мимо чужих
// кластеров, на суше без чужих построек раскладки.
func (b *Base) extFree(l *layout.Layout, c *Cluster, old Rect) error {
	a := c.Area
	if z := c.Zone; z != nil && (a.Min.X < z.Min.X || a.Min.Y < z.Min.Y || a.Max.X > z.Max.X || a.Max.Y > z.Max.Y) {
		return fmt.Errorf("кончилась зона")
	}
	ring := a.Grow(b.border(c))
	for _, o := range b.Clusters {
		if o != c && o.Kind != Bus && ring.Overlaps(o.Area.Grow(b.border(o))) && !old.Grow(b.border(c)).Overlaps(o.Area.Grow(b.border(o))) {
			return fmt.Errorf("рядом кластер %s", o.Name)
		}
	}
	for y := a.Min.Y; y < a.Max.Y; y++ {
		for x := a.Min.X; x < a.Max.X; x++ {
			t := world.Tile{X: x, Y: y}
			if x >= old.Min.X && x < old.Max.X && y >= old.Min.Y && y < old.Max.Y {
				continue
			}
			if p := l.At(t); p != nil && p.Tag != Tag(c.Name) {
				return fmt.Errorf("постройка %s в (%d, %d)", p.Name, x, y)
			}
			if ok, _ := b.W.Buildable(t); !ok {
				return fmt.Errorf("не суша в (%d, %d)", x, y)
			}
		}
	}
	return nil
}

// growInterior — внутренность c заново (в копии раскладки без своих построек), в l — только
// новое: старое на месте (рамка ряда привязана к голове), новые столбы — где свободно.
func (b *Base) growInterior(l *layout.Layout, c *Cluster, n int, addTag ...string) error {
	tag := Tag(c.Name)
	tl := layout.New(l.P, l.W)
	tl.Usable = l.Usable
	for t, v := range l.Reserved {
		if v && !(t.X >= c.Area.Min.X && t.X < c.Area.Max.X && t.Y >= c.Area.Min.Y && t.Y < c.Area.Max.Y) {
			tl.Reserved[t] = true
		}
	}
	type key struct {
		name string
		x, y float64
	}
	own := map[key]bool{}
	for _, p := range l.List {
		if p.Tag == tag {
			own[key{p.Name, p.Pos.X, p.Pos.Y}] = true
		}
		// старые столбы — на месте: новые машины — мимо них (шаблон ставил бы на них)
		if p.Tag != tag || p.Name == "small-electric-pole" {
			tl.Add(p)
		}
	}
	mark := tl.Mark()
	if err := Interior(tl, c, tag); err != nil {
		return err
	}
	if got := Built(tl.List[mark:], c); got < n {
		return fmt.Errorf("встало %d из %d", got, n)
	}
	var add []layout.Placement
	for _, p := range tl.List[mark:] {
		if own[key{p.Name, p.Pos.X, p.Pos.Y}] {
			continue
		}
		if p.Tag != tag {
			continue
		}
		busy := false
		for _, t := range layout.Tiles(l.P, p) {
			busy = busy || l.At(t) != nil
		}
		if busy {
			if p.Name == "small-electric-pole" {
				continue // старые столбы рядом — новые там не нужны
			}
			return fmt.Errorf("%s в (%.1f, %.1f) — на старой постройке", p.Name, p.Pos.X, p.Pos.Y)
		}
		add = append(add, p)
	}
	for _, p := range add {
		if len(addTag) > 0 {
			p.Tag = addTag[0] // дописанное на потом (FillMine)
		}
		l.Add(p)
	}
	l.AllowEndsOf(tl)
	return nil
}
