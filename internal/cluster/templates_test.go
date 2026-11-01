package cluster

import (
	"fmt"
	"testing"

	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Шаблоны кластеров таблицы на пустом месте: машин — ровно из таблицы (у пар и троек — ещё по
// промежуточному на основной), длина ряда — голова + машины × шаг, буферов входа нет; буфер выхода
// — один сундук с инсертером на продукт в зазоре перед последней машиной (у науки, жидкостей и
// НПЗ — нет), у двигателей ещё сундук труб; у станции — буфер угля (cluster.md §6).
func TestBuildTemplates(t *testing.T) {
	p := pt(t)
	// длина ряда, тайлов (refactor.md: этажи по самому длинному столбцу)
	wantLen := map[string]int{
		"transport-belt":     57, // 7 пар по 8 + голова
		"inserter":           25, // 3 пары
		"electronic-circuit": 81, // 10 пар
		"copper-plate":       62, // 20 печей по 3 + голова + перекладка
		"steel-plate":        46, // 15 пар печей по 3 + голова
		"engine-unit":        50, // 4 тройки по 12 + голова с сундуком труб
		"lab":                12, // 2 ряда по 4 + голова (лента C и её инсертеры)
	}
	for i, b := range Builds {
		if b.Kind == Mine || b.Kind == Tank || b.Kind == Storage {
			continue
		}
		c := b.Cluster(p)
		c.Name = fmt.Sprintf("%s-%d", c.Product, i)
		L := c.Len()
		c.Area = Rect{Min: world.Tile{X: 0, Y: 0}, Max: world.Tile{X: L, Y: c.Cross}}
		l := layout.New(p, nil)
		if err := Interior(l, c, Tag(c.Name)); err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		t.Logf("%-30s ряд %3d × %2d (голова %d, шаг %d, мест %d)", c.Name, L, c.Cross, c.Head, c.Pitch, c.Count)
		if w, ok := wantLen[c.Product]; ok && L != w {
			t.Errorf("%s: длина ряда %d, ждали %d", c.Name, L, w)
		}
		pitchLen := c.Head + c.Count*c.Pitch
		if c.Kind == Smelt && c.swaps() && !c.IsSteel() {
			pitchLen += smeltGap
		}
		if c.Kind == Lab {
			pitchLen = c.Head + (c.Count+labCols-1)/labCols*c.Pitch
		}
		if L != pitchLen {
			t.Errorf("%s: длина ряда %d, а голова %d + %d × %d = %d", c.Name, L, c.Head, c.Count, c.Pitch, pitchLen)
		}
		// машины
		recipes := map[string]int{}
		chests, buffer := 0, 0
		var chestAt []world.Tile
		for _, pl := range l.List {
			if pl.Recipe != "" && pl.Name != "inserter" && pl.Name != "long-handed-inserter" {
				recipes[pl.Recipe]++
			}
			if pl.Name == layout.Chest {
				chests++
				chestAt = append(chestAt, world.TileOf(pl.Pos))
			}
			if (pl.Name == "inserter" || pl.Name == "long-handed-inserter") && pl.Recipe == c.Product {
				buffer++
			}
		}
		switch c.Kind {
		case Power:
			if n := countName(l, "boiler"); n != b.N {
				t.Errorf("%s: бойлеров %d, ждали %d", c.Name, n, b.N)
			}
			if chests != bufferChestsN {
				t.Errorf("%s: сундуков буфера угля %d, ждали %d", c.Name, chests, bufferChestsN)
			}
			continue
		case Lab:
			if n := countName(l, "lab"); n != b.N {
				t.Errorf("%s: лабораторий %d, ждали %d", c.Name, n, b.N)
			}
			ports := map[string]bool{}
			for _, pt := range c.Ports {
				ports[pt.Item] = true
			}
			if len(ports) != 5 {
				t.Errorf("%s: подачи пакетов %v, ждали все пять", c.Name, ports)
			}
			if chests != 0 {
				t.Errorf("%s: сундуков %d — у лабораторий буферов нет", c.Name, chests)
			}
			continue
		}
		if c.Kind == Smelt && !c.IsSteel() { // печи рецепт выбирают сами — по числу печей
			recipes[b.Recipe] = countName(l, BuildMachine(p, b.Recipe, b.Stage >= Tier2From))
		}
		if recipes[b.Recipe] != b.N {
			t.Errorf("%s: машин %s — %d, ждали %d", c.Name, b.Recipe, recipes[b.Recipe], b.N)
		}
		for _, s := range b.Subs {
			if recipes[s] != b.N {
				t.Errorf("%s: промежуточных %s — %d, ждали %d", c.Name, s, recipes[s], b.N)
			}
		}
		_, fluid := p.Fluids[c.Product]
		wantChests := 1
		if IsScience(c.Product) || fluid {
			wantChests = 0
		}
		if b.SubChest != "" {
			wantChests++
		}
		if chests != wantChests {
			t.Errorf("%s: сундуков %d, ждали %d (буферов входа нет)", c.Name, chests, wantChests)
		}
		if wantChests > 0 && buffer != 1 && b.SubChest == "" {
			t.Errorf("%s: инсертеров буфера выхода с фильтром на %s — %d, ждали 1", c.Name, c.Product, buffer)
		}
		// буфер выхода — в зазоре перед последней машиной: по длине ряда — между машинами
		for _, ch := range chestAt {
			if b.SubChest == "" && (ch.X < c.Head || ch.X >= L-c.Pitch) {
				t.Errorf("%s: буфер выхода в %v — не в зазоре перед последней машиной (ряд %d…%d)", c.Name, ch, c.Head, L)
			}
		}
	}
}

func countName(l *layout.Layout, name string) int {
	n := 0
	for _, pl := range l.List {
		if pl.Name == name {
			n++
		}
	}
	return n
}
