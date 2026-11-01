package cluster

import (
	"fmt"

	"factorio-bot/internal/layout"
)

// Блок добычи до цели под бурами (stages.md, стадия 30): буры — дальше по тому же шаблону в своей
// площади, стоящее — на месте.

// MainMachine — основная машина кластера: печь, сборщик, бур, бойлер.
func (c *Cluster) MainMachine() string {
	if c.Kind == Power {
		return "boiler"
	}
	return c.machine()
}

// FillMineTo — блок добычи c дописан до ore руды под бурами (не дальше своего пятна): буры — дальше
// по тому же шаблону, старые на месте; дописанное — с тегом tag.
func (b *Base) FillMineTo(l *layout.Layout, c *Cluster, ore int, tag string) error {
	if c.Kind != Mine {
		return fmt.Errorf("cluster: %s — не добыча", c.Name)
	}
	if ore <= c.OreUnder {
		return nil
	}
	c.Ore = ore
	return b.refill(l, c, tag)
}

// OreIn — руды ресурса c в площади блока добычи c (оценка: всё пятно в ней).
func (b *Base) OreIn(c *Cluster) int { return b.oreIn(c.Area, c.Product) }

// refill — внутренность кластера c заново по шаблону в своей площади: новые постройки — в l с
// тегом tag, стоящие — на месте (новые на старые не встают; столбы рядом со старыми не нужны).
func (b *Base) refill(l *layout.Layout, c *Cluster, tag string) error {
	own := Tag(c.Name)
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
	was := map[key]bool{}
	for _, p := range l.List {
		if p.Tag == own {
			was[key{p.Name, p.Pos.X, p.Pos.Y}] = true
		}
		// старые столбы — на месте: новые машины — мимо них (шаблон ставил бы на них)
		if p.Tag != own || p.Name == "small-electric-pole" {
			tl.Add(p)
		}
	}
	mark := tl.Mark()
	if err := Interior(tl, c, own); err != nil {
		return err
	}
	var add []layout.Placement
	for _, p := range tl.List[mark:] {
		if was[key{p.Name, p.Pos.X, p.Pos.Y}] || p.Tag != own {
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
		p.Tag = tag
		l.Add(p)
	}
	l.AllowEndsOf(tl)
	return nil
}
