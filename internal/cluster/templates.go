package cluster

import (
	"fmt"
	"math"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Шаблоны кластеров — ряды в своей системе координат: u — вдоль ряда (рост), v — поперёк.
// Area горизонтальная (W ≥ H) — u = X, v = Y; вертикальная — транспонирование (u = Y, v = X,
// направления N↔W, E↔S). Порты — тайлы коридора у края Area: вход ленты — куда подвести
// маршрут (там лента смотрит Dir), выход — откуда маршрут начинается.
//
// Плавка (Cross 8): 0 — ящики буфера, 1 — инсертеры буфера, 2 — лента входа (руда и уголь
// по полосам: столбец u=0 — подача сверху и снизу боком), 3 — инсертеры, 4–5 — печи, 6 —
// инсертеры, 7 — лента выхода. Сборка (Cross 9): то же с машиной 3×3. Лаборатории (Cross 7):
// буфер, лента, инсертеры, лаборатории. Добыча (Cross 7): буры сверху и снизу выходом на ленту
// в ряду 3. Станция (Cross 17): 0 — столбы, 1–10 — два двигателя, 11–12 — бойлер (вода по
// нижнему ряду, трубой через промежуток), 13 — инсертер на угле, 14 — лента угля, 15–16 — буфер.

// Port — вход/выход ленты кластера.
type Port struct {
	T    world.Tile `json:"t"`
	Dir  int        `json:"dir"`
	Item string     `json:"item"`
	In   bool       `json:"in"`
}

// geometry — размеры ряда по виду: поперёк, шаг машины, столбец портов в начале.
func geometry(k Kind) (cross, pitch, head int) {
	switch k {
	case Mine:
		return 7, 4, 0
	case Smelt:
		return 8, 3, 1
	case Make:
		return 9, 4, 1
	case Lab: // блок (cluster.md §2а «Лаборатории»): поперёк — 4 ряда лент и буферов + столбцы лабораторий
		return 4 + 4*labCols - 1, 4, 1
	case Supply:
		return 5, 2, 1
	case Power:
		return 17, 4, 1
	}
	return 3, 1, 0 // склад
}

// Len — длина Area вдоль ряда (у блока лабораторий Room — лабораторий, ряд — labCols штук).
func (c *Cluster) Len() int {
	if c.Kind == Lab {
		return c.Head + (c.Room+labCols-1)/labCols*c.Pitch
	}
	return c.Head + c.Room*c.Pitch
}

// Блок лабораторий (cluster.md §2а «Лаборатории»): labCols столбцов, всего не меньше labRoom; поперёк: 0 —
// сундуки буфера второго пакета ленты A, 1 — его инсертеры (в голове) / лента B (вдоль
// лабораторий), 2 — лента A, 3 — инсертеры буфера первого пакета (в голове) / инсертеры в
// лаборатории левого столбца (короткий с A, длинный с B), 4 — сундуки буфера первого пакета
// (в голове) / лаборатории. Вдоль: голова (подачи — u=0, буферы — 1…), лента C, её инсертеры,
// ряды лабораторий через 4 (зазоры — инсертеры лаборатория → лаборатория).
const (
	labCols = 5
	labRoom = 30
	labA    = 2 // ряд ленты A
)

type frame struct {
	r        Rect
	vertical bool
	up       bool // вертикальный ряд головой вниз (Upright: входы — к шине снизу)
}

func (f frame) tile(u, v int) world.Tile {
	if f.up {
		return world.Tile{X: f.r.Min.X + v, Y: f.r.Max.Y - 1 - u}
	}
	if f.vertical {
		return world.Tile{X: f.r.Min.X + v, Y: f.r.Min.Y + u}
	}
	return world.Tile{X: f.r.Min.X + u, Y: f.r.Min.Y + v}
}

// dir — направление из системы ряда в мир (транспонирование: N↔W, E↔S).
func (f frame) dir(d int) int {
	if f.up { // поворот: вдоль ряда — вверх
		return map[int]int{layout.E: layout.N, layout.S: layout.E, layout.W: layout.S, layout.N: layout.W}[d]
	}
	if !f.vertical {
		return d
	}
	return map[int]int{layout.N: layout.W, layout.W: layout.N, layout.E: layout.S, layout.S: layout.E}[d]
}

// at — размещение name с левым верхним углом (u, v) в системе ряда, направлением d.
func (f frame) at(l *layout.Layout, name string, u, v, d int, tag string) layout.Placement {
	wd := f.dir(d)
	lt := f.tile(u, v) // у транспонированного прямоугольника левый верхний угол тот же
	if f.up {          // вдоль ряда (u) — высота в мире, растёт вверх
		_, h := layout.Size(l.P, name, wd)
		lt = world.Tile{X: f.r.Min.X + v, Y: f.r.Max.Y - u - h}
	}
	return layout.Placement{Name: name, Pos: layout.CenterAt(l.P, name, wd, lt), Dir: wd, Tag: tag}
}

// inserter — инсертер в (u, v), берёт из (u, fromV).
func (f frame) inserter(l *layout.Layout, name string, u, v, fromV int, tag string) layout.Placement {
	a := f.tile(u, v)
	return layout.Placement{Name: name, Pos: a.Center(), Dir: layout.InserterDir(l.P, name, a, f.tile(u, fromV)), Tag: tag}
}

// Interior раскладывает внутренность кластера (Area уже выбрана) в l с тегом tag: машины
// на Count мест, ленты на всю длину, буфер входа, столбы внутри; заполняет c.Ports (и c.Water
// у станции). Тайлы коридора вокруг на время столбов закрыты — столбы только внутри.
func Interior(l *layout.Layout, c *Cluster, tag string) error {
	f := c.frame()
	L := c.Len()
	c.Ports = nil
	add := func(p layout.Placement) error {
		if !l.CanPlace(p, layout.AnyLand) {
			return fmt.Errorf("cluster %s: не встаёт %s", c.Name, p)
		}
		l.Add(p)
		return nil
	}
	belt := func(u, v, d int) error { return add(f.at(l, "transport-belt", u, v, d, tag)) }
	// row — лента ряда v на u = from..to-1 (только под построенное, не на весь запас)
	row := func(v, from, to, d int) error {
		for u := from; u < to; u++ {
			if err := belt(u, v, d); err != nil {
				return err
			}
		}
		return nil
	}
	// sideIn — вход ленты в ряд v: первый предмет сверху, второй снизу — боком в столбец u=0
	sideIn := func(v int) error {
		if err := belt(0, v, layout.E); err != nil {
			return err
		}
		for i, in := range c.In {
			if i > 1 {
				return fmt.Errorf("cluster %s: больше двух входов (%v) — шаблона нет", c.Name, flows(c.In))
			}
			if i == 0 {
				for y := 0; y < v; y++ {
					if err := belt(0, y, layout.S); err != nil {
						return err
					}
				}
				c.Ports = append(c.Ports, Port{T: f.tile(0, -1), Dir: f.dir(layout.S), Item: in.Item, In: true})
			} else {
				for y := c.Cross - 1; y > v; y-- {
					if err := belt(0, y, layout.N); err != nil {
						return err
					}
				}
				c.Ports = append(c.Ports, Port{T: f.tile(0, c.Cross), Dir: f.dir(layout.N), Item: in.Item, In: true})
			}
		}
		return nil
	}
	deadEnd := func(v, to int) {
		end := f.tile(to, v)
		l.AllowEnd(end)
		l.Reserved[end] = true
	}
	switch c.Kind {
	case Mine:
		last, err := mineRow(l, f, c, L, tag)
		if err != nil {
			return err
		}
		// лента к началу ряда (←), выход — в коридор перед ним: рост ряда выход не двигает
		if err := row(3, 0, last+3, layout.W); err != nil {
			return err
		}
		l.AllowEnd(f.tile(-1, 3)) // выход: дальше — лента к потребителю (или её ещё нет)
		c.Ports = append(c.Ports, Port{T: f.tile(-1, 3), Dir: f.dir(layout.W), Item: c.Product})
	case Lab:
		// блок лабораторий (cluster.md §2а «Лаборатории»): с левого нижнего угла по рядам; левый столбец — с
		// ленты A, остальные — от соседа слева и снизу (пакеты из лаборатории в лабораторию)
		labU := func(i int) int { return c.Head + 4*i }
		labV := func(j int) int { return 4 + 4*j }
		along := func(u, v, fromU int) layout.Placement { // инсертер вдоль ряда: из (fromU, v)
			a := f.tile(u, v)
			return layout.Placement{Name: "inserter", Pos: a.Center(), Dir: layout.InserterDir(l.P, "inserter", a, f.tile(fromU, v)), Tag: tag}
		}
		rows := 0
		for k := 0; k < c.Count; k++ {
			i, j := k/labCols, k%labCols
			rows = i + 1
			if err := add(f.at(l, "lab", labU(i), labV(j), layout.N, tag)); err != nil {
				return err
			}
			mid := labU(i) + 1
			if j == 0 { // с ленты A
				if err := add(f.inserter(l, "inserter", mid, labA+1, labA, tag)); err != nil {
					return err
				}
			} else if err := add(f.inserter(l, "inserter", mid, labV(j)-1, labV(j)-2, tag)); err != nil {
				return err
			}
			if i > 0 { // от лаборатории снизу
				if err := add(along(labU(i)-1, labV(j)+1, labU(i)-2)); err != nil {
					return err
				}
			}
		}
		if err := sideIn(labA); err != nil {
			return err
		}
		blen := labU(rows-1) + 3 // лента A — до верха последнего ряда лабораторий
		if err := row(labA, 1, blen, layout.E); err != nil {
			return err
		}
		deadEnd(labA, blen)
		if len(c.In) >= 1 {
			if err := bufferAt(l, f, c, labA, labA+1, labA+2, (c.Head-2)/2, c.In[0].Item, tag); err != nil {
				return err
			}
		}
		if len(c.In) >= 2 {
			if err := bufferAt(l, f, c, labA, labA-1, labA-2, (c.Head-2)/2, c.In[1].Item, tag); err != nil {
				return err
			}
		}
		// место под второй вход ленты A, ленты B и C с инсертерами — не под столбы (§2а «Лаборатории»)
		for _, t := range labReserve(f, c) {
			if l.At(t) == nil {
				l.Reserved[t] = true
			}
		}
	case Smelt, Make, Supply:
		mach, mh := "stone-furnace", 2
		ins := "inserter"
		belts := 2
		switch c.Kind {
		case Make, Lab:
			mach, mh = c.machine(), 3
		case Supply:
			mach, mh = "iron-chest", 1
		}
		for i := 0; i < c.Count && c.Sub != ""; i++ {
			if err := pair(l, f, c, c.Head+i*c.Pitch, belts, mach, ins, tag, add); err != nil {
				return err
			}
		}
		for i := 0; i < c.Count && c.Sub == ""; i++ {
			u := c.Head + i*c.Pitch
			iu := u + 1 // столбец инсертера: у машин 3×3 — по центру; ящик 1×1 — сам в нём
			if c.Kind == Supply {
				iu = u
			}
			m := f.at(l, mach, u, belts+2, layout.N, tag)
			if c.Kind == Make {
				m.Recipe = c.Units[0].Recipe
			}
			if err := add(m); err != nil {
				return err
			}
			in := f.inserter(l, ins, iu, belts+1, belts, tag)
			if c.Kind == Supply && len(c.In) > 0 {
				// ящики по очереди под каждый вход; инсертер берёт только своё (на общей ленте
				// обе пластины — иначе в ящике смесь)
				in.Recipe = c.In[i%len(c.In)].Item
			}
			if err := add(in); err != nil {
				return err
			}
			if c.Kind != Lab && c.Kind != Supply {
				ov := belts + 2 + mh
				if err := add(f.inserter(l, ins, u+1, ov, ov-1, tag)); err != nil {
					return err
				}
			}
		}
		if err := sideIn(belts); err != nil {
			return err
		}
		// лента входа — до последней машины, не на весь запас; конец — внутри площади (в
		// последний столбец шага, без машины): у полного ряда в коридор не смотрит — там
		// чужие ленты (медь сливалась в линию угля)
		cur := c.Head + c.Count*c.Pitch
		blen := cur - 1
		if err := row(belts, 1, blen, layout.E); err != nil {
			return err
		}
		deadEnd(belts, blen)
		// буфер на общей ленте — только для одного входа; два (руда + уголь) — смешаются,
		// у каждого свой буфер на его входящей ленте (Connect)
		if len(c.In) == 1 && c.Head > 1 {
			if err := buffer(l, f, c, belts, tag); err != nil {
				return err
			}
		}
		if c.Kind != Lab && c.Kind != Supply {
			// выход — к началу ряда (←) и вниз в коридор у столбца портов: рост его не двигает
			ov := belts + 3 + mh
			if err := row(ov, 2, cur, layout.W); err != nil {
				return err
			}
			if err := belt(1, ov, layout.S); err != nil {
				return err
			}
			item := c.Product
			if len(c.Out) > 0 {
				item = c.Out[0].Item
			}
			l.AllowEnd(f.tile(1, c.Cross))
			c.Ports = append(c.Ports, Port{T: f.tile(1, c.Cross), Dir: f.dir(layout.S), Item: item})
		}
	case Power:
		if err := powerRow(l, f, c, L, tag); err != nil {
			return err
		}
	case Storage:
		for u := 0; u < c.Count; u++ {
			if err := add(f.at(l, layout.Chest, u, 1, layout.N, tag)); err != nil {
				return err
			}
		}
	}
	// столбы — только внутри Area
	ring := c.Area.Grow(Corridor)
	var closed []world.Tile
	for y := ring.Min.Y; y < ring.Max.Y; y++ {
		for x := ring.Min.X; x < ring.Max.X; x++ {
			t := world.Tile{X: x, Y: y}
			if x >= c.Area.Min.X && x < c.Area.Max.X && y >= c.Area.Min.Y && y < c.Area.Max.Y || l.Reserved[t] {
				continue
			}
			l.Reserved[t] = true
			closed = append(closed, t)
		}
	}
	l.PoleTag = tag
	l.Poles("small-electric-pole", tag)
	l.PoleTag = ""
	for _, t := range closed {
		delete(l.Reserved, t)
	}
	return nil
}

// pair — пара прямой подачи с u: сборщик промежуточного (Sub) → инсертер → основной (Main)
// [→ инсертер ← второй Sub]; все берут с ленты входа, выход — только у основного.
func pair(l *layout.Layout, f frame, c *Cluster, u, belts int, mach, ins, tag string, add func(layout.Placement) error) error {
	mv := belts + 2       // ряд сборщиков (3×3)
	xs := []int{u, u + 4} // Sub, Main
	recs := []string{c.Sub, c.Main}
	if c.SubPer > 1 {
		xs, recs = append(xs, u+8), append(recs, c.Sub)
	}
	for k, x := range xs {
		m := f.at(l, mach, x, mv, layout.N, tag)
		m.Recipe = recs[k]
		if err := add(m); err != nil {
			return err
		}
		if err := add(f.inserter(l, ins, x+1, belts+1, belts, tag)); err != nil { // с ленты
			return err
		}
	}
	direct := func(at, from int) error { // инсертер в среднем ряду сборщиков: из Sub в Main
		a := f.tile(at, mv+1)
		return add(layout.Placement{Name: ins, Pos: a.Center(), Dir: layout.InserterDir(l.P, ins, a, f.tile(from, mv+1)), Tag: tag})
	}
	if err := direct(u+3, u+2); err != nil {
		return err
	}
	if c.SubPer > 1 {
		if err := direct(u+7, u+8); err != nil {
			return err
		}
	}
	ov := mv + 3 // выход основного
	return add(f.inserter(l, ins, u+5, ov, ov-1, tag))
}

// mineRow — буры по обе стороны ленты ряда 3 (сверху смотрят S, снизу N), только где под
// зоной добычи не меньше половины — руда и нет другого ресурса; сколько нужно (Units) или сколько встанет.
// last — u последнего поставленного бура.
func mineRow(l *layout.Layout, f frame, c *Cluster, L int, tag string) (last int, err error) {
	drill := c.Units[0].Machine
	need := int(math.Ceil(c.Units[0].Machines - 1e-9))
	placed := 0
	for u := 0; u+3 <= L && placed < need; u += c.Pitch {
		for _, s := range []struct{ v, d int }{{0, layout.S}, {4, layout.N}} {
			if placed >= need {
				break
			}
			p := f.at(l, drill, u, s.v, s.d, tag)
			n, mixed := l.OreUnder(p, c.Product)
			if mixed || n*2 < len(layout.MiningArea(l.P, p)) || !l.CanPlace(p, layout.AnyLand) {
				continue
			}
			l.Add(p)
			placed++
			last = u
		}
	}
	if placed == 0 {
		return 0, fmt.Errorf("cluster %s: ни одного бура на %s", c.Name, c.Product)
	}
	c.Count = (placed + 1) / 2
	return last, nil
}

// powerRow — бойлеры в ряд (вода — по нижнему ряду бойлеров, трубой через промежуток),
// над каждым два двигателя, уголь — лентой ряда 14 burner-инсертером (без электричества:
// станция запускается сама); буфер угля; столбы в промежутках у двигателей.
func powerRow(l *layout.Layout, f frame, c *Cluster, L int, tag string) error {
	add := func(p layout.Placement) error {
		if !l.CanPlace(p, layout.AnyLand) {
			return fmt.Errorf("cluster %s: не встаёт %s", c.Name, p)
		}
		l.Add(p)
		return nil
	}
	// вход воды — трубы по ряду 12 от края до первого бойлера (столбец портов и буфер угля)
	for u := 0; u < c.Head; u++ {
		if err := add(f.at(l, "pipe", u, 12, layout.N, tag)); err != nil {
			return err
		}
	}
	c.Water = &layout.Conn{At: f.tile(0, 12), To: f.tile(-1, 12), Dir: f.dir(layout.W), Filter: "water"}
	for i := 0; i < c.Count; i++ {
		u := c.Head + i*c.Pitch
		b := f.at(l, "boiler", u, 11, layout.N, tag)
		for _, p := range []layout.Placement{
			b,
			f.at(l, "steam-engine", u, 6, layout.N, tag),
			f.at(l, "steam-engine", u, 1, layout.N, tag),
			f.inserter(l, "burner-inserter", u+1, 13, 14, tag),
			f.at(l, "small-electric-pole", u+3, 3, layout.N, tag),
			f.at(l, "small-electric-pole", u+3, 8, layout.N, tag),
		} {
			if err := add(p); err != nil {
				return err
			}
		}
		if i+1 < c.Count {
			if err := add(f.at(l, "pipe", u+3, 12, layout.N, tag)); err != nil {
				return err
			}
		}
	}
	// лента угля — до последнего бойлера, конец — внутри площади (промежуток за бойлером)
	blen := c.Head + c.Count*c.Pitch - 1
	for u := 0; u < blen; u++ {
		if err := add(f.at(l, "transport-belt", u, 14, layout.E, tag)); err != nil {
			return err
		}
	}
	end := f.tile(blen, 14)
	l.AllowEnd(end)
	l.Reserved[end] = true
	c.Ports = append(c.Ports, Port{T: f.tile(-1, 14), Dir: f.dir(layout.E), Item: "coal", In: true})
	if c.Head > 1 {
		return buffer(l, f, c, 14, tag)
	}
	return nil
}

// buffer — буфер входа вдоль ленты ряда v в начале ряда (до первой машины: Head): инсертер с
// ленты в ящик 1 → инсертеры по ящикам → из последнего обратно на ленту ниже по ходу, но выше
// всех машин (иначе предмет обходит машины в ящики). Ящики — напротив подачи (вход — сверху,
// буфер — снизу): инсертер возврата кладёт на дальнюю полосу — ту, куда предмет и пришёл
// (cluster.md §6б). Ёмкость — на 10 минут входа кластера.
func buffer(l *layout.Layout, f frame, c *Cluster, v int, tag string) error {
	// что брать — вход кластера (буфер только у одного входа): по ленте может приехать чужое
	item := ""
	if len(c.In) > 0 {
		item = c.In[0].Item
	}
	return bufferAt(l, f, c, v, v+1, v+2, c.Head/2, item, tag)
}

// bufferAt — цепочка буфера item у ленты ряда v: инсертеры в ряду insV, n ящиков в ряду chestV
// (на нечётных u 1, 3, …, 2n−1, между ними перекладывающие инсертеры).
func bufferAt(l *layout.Layout, f frame, c *Cluster, v, insV, chestV, n int, item, tag string) error {
	ins := "inserter"
	if c.Kind == Power {
		ins = "burner-inserter" // станция без электричества стоит — буфер угля отдаёт его и тогда
	}
	for i := 0; i < n; i++ {
		u := 1 + 2*i
		ch := f.at(l, layout.Chest, u, chestV, layout.N, tag)
		if !l.CanPlace(ch, layout.AnyLand) {
			return fmt.Errorf("cluster %s: буфер не встаёт (%s)", c.Name, ch)
		}
		l.Add(ch)
		if i+1 < n {
			a := f.tile(u+1, chestV)
			l.Add(layout.Placement{Name: ins, Pos: a.Center(), Dir: layout.InserterDir(l.P, ins, a, f.tile(u, chestV)), Recipe: item, Tag: tag})
		}
	}
	first, last := f.inserter(l, ins, 1, insV, v, tag), f.inserter(l, ins, 2*n-1, insV, chestV, tag)
	first.Recipe, last.Recipe = item, item
	l.Add(first) // с ленты в ящик 1
	l.Add(last)  // из последнего — на ленту
	return nil
}

// labReserve — место блока лабораторий под будущее (cluster.md §2а «Лаборатории»): подача и буфер второго
// пакета ленты A, лента B и длинные инсертеры с неё, лента C и её инсертеры, лаборатории
// сверх Count и инсертеры к ним; столбы — только в зазорах.
func labReserve(f frame, c *Cluster) []world.Tile {
	var res []world.Tile
	n := (c.Head - 2) / 2
	for v := labA + 1; v < c.Cross; v++ { // подача второго пакета A — снизу, вдоль u=0
		res = append(res, f.tile(0, v))
	}
	for u := 1; u <= 2*n-1; u++ { // его буфер — сверху
		res = append(res, f.tile(u, labA-1), f.tile(u, labA-2))
	}
	L := c.Len()
	for u := c.Head - 2; u < L; u++ { // лента B
		res = append(res, f.tile(u, labA-1))
	}
	for v := 4; v < c.Cross; v++ { // лента C и её инсертеры
		res = append(res, f.tile(c.Head-2, v), f.tile(c.Head-1, v))
	}
	for k := 0; k < c.Room; k++ {
		i, j := k/labCols, k%labCols
		u0, v0 := c.Head+4*i, 4+4*j
		res = append(res, f.tile(u0, labA+1), f.tile(u0+2, labA+1)) // длинные с B (и запас)
		res = append(res, f.tile(u0+1, v0-1))                      // инсертер слева
		if i > 0 {
			res = append(res, f.tile(u0-1, v0+1)) // инсертер снизу
		}
		if k >= c.Count { // будущая лаборатория
			for du := 0; du < 3; du++ {
				for dv := 0; dv < 3; dv++ {
					res = append(res, f.tile(u0+du, v0+dv))
				}
			}
		}
	}
	return res
}

// bufferChests — ящиков в буфере входа: на BufferSeconds входа (деревянный ящик — 16 стопок
// самого мелкого предмета), не меньше 2.
func bufferChests(p *knowledge.Prototypes, c *Cluster) int {
	need, stack := 0.0, 100
	for _, in := range c.In {
		need += in.Rate * BufferSeconds
		if s := p.Items[in.Item].StackSize; s > 0 && s < stack {
			stack = s
		}
	}
	return max(2, int(math.Ceil(need/float64(16*stack))))
}

// BufferSeconds — на сколько работы без подачи рассчитан буфер входа.
const BufferSeconds = 600
