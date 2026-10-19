package cluster

import (
	"fmt"
	"math"

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
	case Lab:
		return 7, 3, 1
	case Power:
		return 17, 4, 1
	}
	return 3, 1, 0 // склад
}

// Len — длина Area вдоль ряда.
func (c *Cluster) Len() int { return c.Head + c.Room*c.Pitch }

type frame struct {
	r        Rect
	vertical bool
}

func (f frame) tile(u, v int) world.Tile {
	if f.vertical {
		return world.Tile{X: f.r.Min.X + v, Y: f.r.Min.Y + u}
	}
	return world.Tile{X: f.r.Min.X + u, Y: f.r.Min.Y + v}
}

// dir — направление из системы ряда в мир (транспонирование: N↔W, E↔S).
func (f frame) dir(d int) int {
	if !f.vertical {
		return d
	}
	return map[int]int{layout.N: layout.W, layout.W: layout.N, layout.E: layout.S, layout.S: layout.E}[d]
}

// at — размещение name с левым верхним углом (u, v) в системе ряда, направлением d.
func (f frame) at(l *layout.Layout, name string, u, v, d int, tag string) layout.Placement {
	wd := f.dir(d)
	lt := f.tile(u, v) // у транспонированного прямоугольника левый верхний угол тот же
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
	f := frame{r: c.Area, vertical: c.Vertical}
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
	case Smelt, Make, Lab:
		mach, mh := "stone-furnace", 2
		ins := "inserter"
		belts := 2
		if c.Kind != Smelt {
			mach, mh = c.machine(), 3
		}
		for i := 0; i < c.Count && c.Sub != ""; i++ {
			if err := pair(l, f, c, 1+i*c.Pitch, belts, mach, ins, tag, add); err != nil {
				return err
			}
		}
		for i := 0; i < c.Count && c.Sub == ""; i++ {
			u := 1 + i*c.Pitch
			m := f.at(l, mach, u, belts+2, layout.N, tag)
			if c.Kind == Make {
				m.Recipe = c.Units[0].Recipe
			}
			if err := add(m); err != nil {
				return err
			}
			if err := add(f.inserter(l, ins, u+1, belts+1, belts, tag)); err != nil {
				return err
			}
			if c.Kind != Lab {
				ov := belts + 2 + mh
				if err := add(f.inserter(l, ins, u+1, ov, ov-1, tag)); err != nil {
					return err
				}
			}
		}
		if err := sideIn(belts); err != nil {
			return err
		}
		// лента входа — до последней машины (и вдоль буфера), не на весь запас
		cur := c.Head + c.Count*c.Pitch
		blen := cur
		if len(c.In) == 1 {
			blen = min(max(cur, 2*bufferChests(l, c)), L)
		}
		if err := row(belts, 1, blen, layout.E); err != nil {
			return err
		}
		deadEnd(belts, blen)
		// буфер на общей ленте — только для одного входа; два (руда + уголь) — смешаются,
		// у каждого свой буфер на его входящей ленте (Connect)
		if len(c.In) == 1 {
			if err := buffer(l, f, c, blen, belts, tag); err != nil {
				return err
			}
		}
		if c.Kind != Lab {
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
			if err := add(f.at(l, "wooden-chest", u, 1, layout.N, tag)); err != nil {
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
// зоной добычи не меньше половины — руда; сколько нужно (Units) или сколько встанет.
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
			area := layout.MiningArea(l.P, p)
			n := 0
			for _, t := range area {
				if r, ok := l.W.ResourceAt(t); ok && r.Name == c.Product {
					n++
				}
			}
			if n*2 < len(area) || !l.CanPlace(p, layout.AnyLand) {
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
	for i := 0; i < c.Count; i++ {
		u := 1 + i*c.Pitch
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
		if i == 0 {
			// вход воды — труба в столбце портов слева от первого бойлера, к ней — из коридора
			if err := add(f.at(l, "pipe", u-1, 12, layout.N, tag)); err != nil {
				return err
			}
			c.Water = &layout.Conn{At: f.tile(u-1, 12), To: f.tile(u-2, 12), Dir: f.dir(layout.W), Filter: "water"}
		}
		if i+1 < c.Count {
			if err := add(f.at(l, "pipe", u+3, 12, layout.N, tag)); err != nil {
				return err
			}
		}
	}
	// лента угля — до последнего бойлера (и вдоль буфера), не на весь запас
	blen := min(max(c.Head+c.Count*c.Pitch, 2*bufferChests(l, c)), L)
	for u := 0; u < blen; u++ {
		if err := add(f.at(l, "transport-belt", u, 14, layout.E, tag)); err != nil {
			return err
		}
	}
	end := f.tile(blen, 14)
	l.AllowEnd(end)
	l.Reserved[end] = true
	c.Ports = append(c.Ports, Port{T: f.tile(-1, 14), Dir: f.dir(layout.E), Item: "coal", In: true})
	return buffer(l, f, c, blen, 14, tag)
}

// buffer — буфер входа вдоль ленты ряда v (ящики — с дальней от машин стороны): инсертер
// с ленты в ящик 1 → инсертеры по ящикам → из последнего обратно на ленту ниже по ходу.
// Ёмкость — на 10 минут входа кластера (деревянный ящик — 16 стопок самого мелкого предмета).
func buffer(l *layout.Layout, f frame, c *Cluster, L, v int, tag string) error {
	insV, chestV := v-1, v-2
	if c.Kind == Power {
		insV, chestV = v+1, v+2
	}
	// ящики на нечётных u (1, 3, …), между ними перекладывающие инсертеры
	n := min(bufferChests(l, c), L/2)
	if n < 2 {
		return nil // ряд короткий — без буфера
	}
	for i := 0; i < n; i++ {
		u := 1 + 2*i
		ch := f.at(l, "wooden-chest", u, chestV, layout.N, tag)
		if !l.CanPlace(ch, layout.AnyLand) {
			return fmt.Errorf("cluster %s: буфер не встаёт (%s)", c.Name, ch)
		}
		l.Add(ch)
		if i+1 < n {
			a := f.tile(u+1, chestV)
			l.Add(layout.Placement{Name: "inserter", Pos: a.Center(), Dir: layout.InserterDir(l.P, "inserter", a, f.tile(u, chestV)), Tag: tag})
		}
	}
	l.Add(f.inserter(l, "inserter", 1, insV, v, tag))          // с ленты в ящик 1
	l.Add(f.inserter(l, "inserter", 2*n-1, insV, chestV, tag)) // из последнего — на ленту
	return nil
}

// bufferChests — ящиков в буфере входа: на BufferSeconds входа (деревянный ящик — 16 стопок
// самого мелкого предмета), не меньше 2.
func bufferChests(l *layout.Layout, c *Cluster) int {
	need, stack := 0.0, 100
	for _, in := range c.In {
		need += in.Rate * BufferSeconds
		if s := l.P.Items[in.Item].StackSize; s > 0 && s < stack {
			stack = s
		}
	}
	return max(2, int(math.Ceil(need/float64(16*stack))))
}

// BufferSeconds — на сколько работы без подачи рассчитан буфер входа.
const BufferSeconds = 600
