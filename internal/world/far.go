package world

import "factorio-bot/internal/bridge"

// farMargin — насколько грубый путь может уходить за рамку старта и цели, тайлов.
const farMargin = 1500

// FarPath — грубый путь по суше для дальней ходьбы: поиск в ширину по тайлам (8 соседей, углы
// не срезать) от to до from; преграда — вода, неоткрытое и утёсы, а деревья, камни и постройки —
// нет: их обходит или рубит обычный путь на участке. A* по ячейкам в полтайла на обход большого
// озера (1700 тайлов) не хватало и 2 млн ячеек (778631482), поиск по тайлам — плотный массив, за
// доли секунды. Точки — центры тайлов от from до to; пути нет — false.
func (w *World) FarPath(from, to bridge.Pos) (Route, bool) {
	ft, tt := TileOf(from), TileOf(to)
	// рамка поиска: открытая карта, не дальше farMargin от старта и цели
	x0, y0 := min(ft.X, tt.X)-farMargin, min(ft.Y, tt.Y)-farMargin
	x1, y1 := max(ft.X, tt.X)+farMargin, max(ft.Y, tt.Y)+farMargin
	cx0, cy0, cx1, cy1 := x1, y1, x0, y0
	for cp := range w.Chunks {
		cx0, cy0 = min(cx0, cp.X*ChunkSize), min(cy0, cp.Y*ChunkSize)
		cx1, cy1 = max(cx1, cp.X*ChunkSize+ChunkSize-1), max(cy1, cp.Y*ChunkSize+ChunkSize-1)
	}
	x0, y0, x1, y1 = max(x0, cx0), max(y0, cy0), min(x1, cx1), min(y1, cy1)
	if x0 > x1 || y0 > y1 {
		return Route{}, false
	}
	W, H := x1-x0+1, y1-y0+1
	in := func(t Tile) bool { return t.X >= x0 && t.X <= x1 && t.Y >= y0 && t.Y <= y1 }
	idx := func(t Tile) int { return (t.Y-y0)*W + t.X - x0 }
	if !in(ft) || !in(tt) {
		return Route{}, false
	}
	pass := make([]bool, W*H)
	for cp, c := range w.Chunks {
		for i := 0; i < ChunkSize*ChunkSize; i++ {
			if k := c.Tiles[i]; k == Land || k == Shallow {
				if t := (Tile{X: cp.X*ChunkSize + i%ChunkSize, Y: cp.Y*ChunkSize + i/ChunkSize}); in(t) {
					pass[idx(t)] = true
				}
			}
		}
	}
	for _, c := range w.Chunks {
		for _, o := range c.Objects {
			if o.Type != "cliff" {
				continue
			}
			for _, t := range BoxTiles(w.CollisionBox(o)) {
				if in(t) {
					pass[idx(t)] = false
				}
			}
		}
	}
	pass[idx(ft)], pass[idx(tt)] = true, true // стоит у берега, краем на воде
	// par — откуда пришли (номер соседа + 1), 0 — не были
	dirs := [8]Tile{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}, {X: 1, Y: 1}, {X: 1, Y: -1}, {X: -1, Y: 1}, {X: -1, Y: -1}}
	par := make([]uint8, W*H)
	par[idx(tt)] = 9
	queue := []Tile{tt}
	for len(queue) > 0 && par[idx(ft)] == 0 {
		q := queue[0]
		queue = queue[1:]
		for k, d := range dirs {
			n := Tile{X: q.X + d.X, Y: q.Y + d.Y}
			if !in(n) || par[idx(n)] != 0 || !pass[idx(n)] {
				continue
			}
			if d.X != 0 && d.Y != 0 && (!pass[idx(Tile{X: q.X + d.X, Y: q.Y})] || !pass[idx(Tile{X: q.X, Y: q.Y + d.Y})]) {
				continue // угол не срезать
			}
			par[idx(n)] = uint8(k + 1)
			queue = append(queue, n)
		}
	}
	if par[idx(ft)] == 0 {
		return Route{}, false
	}
	// от старта к цели — по родителям; точки — только на поворотах
	r := Route{Points: []bridge.Pos{from}}
	prev := -1
	for t := ft; t != tt; {
		k := int(par[idx(t)]) - 1
		if k != prev && t != ft {
			r.Points = append(r.Points, t.Center())
		}
		prev = k
		t = Tile{X: t.X - dirs[k].X, Y: t.Y - dirs[k].Y}
	}
	r.Points = append(r.Points, to)
	return r, true
}
