package cluster

import (
	"fmt"
	"math"

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

// Built — сколько основных машин кластера c в раскладке list.
func Built(list []layout.Placement, c *Cluster) int {
	n := 0
	m := c.MainMachine()
	for _, p := range list {
		if p.Tag == Tag(c.Name) && p.Name == m {
			n++
		}
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
	case Mine: // буры парами по обе стороны ленты; ряд — на руде, сколько встанет
		c.Units[0].Machines = float64(n)
		room = (n + 1) / 2
	case Smelt, Power:
		c.Count, room = n, n
	default:
		return fail(fmt.Errorf("вид %s так не растёт", c.Kind))
	}
	var last error
	for extra := 0; extra <= 8; extra++ {
		c.Room = max(old.Room, room+extra)
		c.Area = grown(old.Area, c)
		c.Rect = c.Area
		if err := b.extFree(l, c, old.Area); err != nil {
			if last != nil {
				err = fmt.Errorf("%w (до того: %v)", err, last)
			}
			return fail(err)
		}
		err := b.growInterior(l, c, n)
		last = err
		if err == nil {
			if b.g != nil {
				b.g.mark(b.occupied(c))
			}
			return nil
		}
		if c.Kind != Mine { // у добычи руда кончилась — длиннее ряд; прочим длиннее не поможет
			return fail(err)
		}
	}
	return fail(fmt.Errorf("руды под рядом не хватает"))
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
		if x.Kind == Mine { // соседняя руда того же месторождения — на недостающие буры, без минимума мест
			x.Count = max((n-built+1)/2, 1)
			x.Room = x.Count
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
func (b *Base) growInterior(l *layout.Layout, c *Cluster, n int) error {
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
		l.Add(p)
	}
	l.AllowEndsOf(tl)
	return nil
}
