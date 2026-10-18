package world

import (
	"math"
	"strings"
	"testing"

	"factorio-bot/internal/bridge"
)

// grid — мир из одного чанка по картинке: '.' суша, 'w' вода, '#' дерево (объект 1×1).
func grid(t *testing.T, rows ...string) *World {
	t.Helper()
	tiles := make([]byte, ChunkSize*ChunkSize)
	for i := range tiles {
		tiles[i] = Land
	}
	var ents []bridge.ScanEntity
	for y, row := range rows {
		for x, c := range row {
			switch c {
			case 'w':
				tiles[y*ChunkSize+x] = Water
			case '#':
				ents = append(ents, bridge.ScanEntity{Name: "tree", Type: "tree", X: float64(x) + 0.5, Y: float64(y) + 0.5})
			}
		}
	}
	w := New(nil)
	w.Apply([]bridge.ChunkScan{{X: 0, Y: 0, Charted: true, Tiles: string(tiles), Entities: ents}})
	return w
}

// walkable — каждый отрезок пути идёт по свободным ячейкам (у старта — вне рамок).
func walkable(t *testing.T, w *World, pts []bridge.Pos, from bridge.Pos) {
	t.Helper()
	prev := from
	for _, p := range pts {
		n := int(dist(prev, p)/0.1) + 1
		for i := 0; i <= n; i++ {
			f := float64(i) / float64(n)
			q := bridge.Pos{X: prev.X + (p.X-prev.X)*f, Y: prev.Y + (p.Y-prev.Y)*f}
			if free, _, core, _ := w.cellState(CellOf(q)); !free && (core || dist(q, from) > 1) {
				t.Errorf("отрезок %v → %v задевает препятствие у %v", prev, p, q)
				break
			}
		}
		prev = p
	}
}

// В чистом поле — прямо к цели под любым углом, без поворотов.
func TestPathStraight(t *testing.T) {
	w := grid(t)
	from, to := bridge.Pos{X: 1.5, Y: 1.5}, bridge.Pos{X: 20.5, Y: 7.5}
	pts, err := w.Path(from, to, 0.5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || dist(pts[0], to) > 0.5 {
		t.Errorf("прямой путь сжимается в одну точку: %v", pts)
	}
}

// Редкий лес (стволы через тайл): персонаж 0.4 проходит между ними — не обходить и не рубить.
func TestPathThroughSparseForest(t *testing.T) {
	rows := make([]string, 12)
	for y := range rows {
		row := []byte(strings.Repeat(".", 24))
		for x := 6; x < 18; x += 2 {
			if y%2 == 0 {
				row[x] = '#'
			}
		}
		rows[y] = string(row)
	}
	w := grid(t, rows...)
	from, to := bridge.Pos{X: 2.5, Y: 5.5}, bridge.Pos{X: 21.5, Y: 5.5}
	r, err := w.Route(from, to, 0.5, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Cuts) != 0 || r.Length() > 21 {
		t.Errorf("через редкий лес: длина %.1f, рубить %d: %v", r.Length(), len(r.Cuts), r.Points)
	}
	walkable(t, w, r.Points[1:], from)
}

// Плотная стена деревьев с обходом в 30+ тайлов — дешевле срубить одно дерево.
func TestPathCutsInsteadOfDetour(t *testing.T) {
	rows := make([]string, 30)
	for y := range rows {
		row := []byte(strings.Repeat(".", 20))
		if y < 29 {
			row[10] = '#'
		}
		rows[y] = string(row)
	}
	w := grid(t, rows...)
	from, to := bridge.Pos{X: 5.5, Y: 2.5}, bridge.Pos{X: 15.5, Y: 2.5}
	r, err := w.Route(from, to, 0.5, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Cuts) != 1 || r.Length() > 12 {
		t.Errorf("ждали срубить 1 дерево и пройти прямо: длина %.1f, рубить %v", r.Length(), r.Cuts)
	}
	// без рубки — обход снизу
	pts, err := w.Path(from, to, 0.5, 0)
	if err != nil {
		t.Fatal(err)
	}
	maxY := 0.0
	for _, p := range pts {
		maxY = math.Max(maxY, p.Y)
	}
	if maxY < 29 {
		t.Errorf("без рубки путь сквозь стену: %v", pts)
	}
	walkable(t, w, pts, from)
}

func TestPathAroundWall(t *testing.T) {
	// стена воды x=10 от y=0 до y=14, проход ниже
	rows := make([]string, 16)
	for y := range rows {
		row := []byte(strings.Repeat(".", 20))
		if y <= 14 {
			row[10] = 'w'
		}
		rows[y] = string(row)
	}
	w := grid(t, rows...)
	from, to := bridge.Pos{X: 5.5, Y: 2.5}, bridge.Pos{X: 15.5, Y: 2.5}
	pts, err := w.Path(from, to, 0.5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) < 2 {
		t.Fatalf("путь через стену: %v", pts)
	}
	maxY := 0.0
	for _, p := range pts {
		maxY = math.Max(maxY, p.Y)
	}
	if maxY < 15 {
		t.Errorf("путь не обходит стену снизу: %v", pts)
	}
	walkable(t, w, pts, from)
	if last := pts[len(pts)-1]; math.Hypot(last.X-to.X, last.Y-to.Y) > 0.5 {
		t.Errorf("конец пути %v, цель %v", last, to)
	}
}

func TestPathNoCornerCutting(t *testing.T) {
	// два дерева по диагонали: проход между ними по диагонали запрещён
	w := grid(t,
		"......",
		"..#...",
		"...#..",
		"......",
	)
	pts, err := w.Path(bridge.Pos{X: 2.5, Y: 2.5}, bridge.Pos{X: 3.5, Y: 1.5}, 0.5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) < 2 {
		t.Errorf("срезал угол между деревьями: %v", pts)
	}
}

func TestPathNear(t *testing.T) {
	// цель — сама постройка (непроходима): достаточно подойти на near
	w := grid(t, "........", "....#...", "........")
	to := bridge.Pos{X: 4.5, Y: 1.5}
	pts, err := w.Path(bridge.Pos{X: 0.5, Y: 1.5}, to, 1.5, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := pts[len(pts)-1]
	if d := math.Hypot(last.X-to.X, last.Y-to.Y); d > 1.5 {
		t.Errorf("остановился в %.2f от цели", d)
	}
}

func TestPathNone(t *testing.T) {
	// цель в озере, окружённом водой
	rows := []string{"wwwww", "w...w", "wwwww"}
	w := grid(t, rows...)
	pts, err := w.Path(bridge.Pos{X: 10.5, Y: 10.5}, bridge.Pos{X: 2.5, Y: 1.5}, 0.5, 5000)
	if err != ErrNoPath {
		t.Errorf("err = %v, ждали ErrNoPath", err)
	}
	// путь к ближайшей достижимой точке: снаружи у стенки озера
	if len(pts) == 0 {
		t.Fatal("нет пути к ближайшей точке")
	}
	if last := pts[len(pts)-1]; math.Hypot(last.X-2.5, last.Y-1.5) > 2.5 {
		t.Errorf("ближайшая точка %v далеко от цели", last)
	}
	// и неоткрытое не проходимо
	if _, err := w.Path(bridge.Pos{X: 10.5, Y: 10.5}, bridge.Pos{X: 50.5, Y: 10.5}, 0.5, 5000); err != ErrNoPath {
		t.Errorf("через неоткрытое: err = %v", err)
	}
}

func TestFreeSpot(t *testing.T) {
	w := grid(t,
		"#.#.....",
		"........",
		"..#.....",
	)
	p, ok := w.FreeSpot(bridge.Pos{X: 1, Y: 1}, 2, 2, 5, nil)
	if !ok {
		t.Fatal("места нет")
	}
	// 2×2 центр на углу тайлов; площадка не должна задевать деревья
	if p.X != math.Floor(p.X) || p.Y != math.Floor(p.Y) {
		t.Errorf("центр 2×2 не на углу: %v", p)
	}
	for _, tl := range []Tile{{int(p.X) - 1, int(p.Y) - 1}, {int(p.X), int(p.Y) - 1}, {int(p.X) - 1, int(p.Y)}, {int(p.X), int(p.Y)}} {
		if !w.Walkable(tl) {
			t.Errorf("площадка %v задевает занятый тайл %v", p, tl)
		}
	}
	if q, ok := w.FreeSpot(bridge.Pos{X: 5.5, Y: 5.5}, 1, 1, 2, nil); !ok || q.X != 5.5 || q.Y != 5.5 {
		t.Errorf("1×1 в свободной точке: %v %v", q, ok)
	}
}

// Персонаж в «кармане» из деревьев: обычного пути нет, сквозь деревья — есть, рубить одно.
func TestPathThroughTrees(t *testing.T) {
	w := grid(t,
		"..........",
		"...###....",
		"...#.#....",
		"...###....",
		"..........",
	)
	from := bridge.Pos{X: 4.5, Y: 2.5}
	if _, err := w.Path(from, bridge.Pos{X: 8.5, Y: 2.5}, 0.5, 0); err != ErrNoPath {
		t.Fatalf("из кармана есть обычный путь? %v", err)
	}
	r, err := w.Route(from, bridge.Pos{X: 8.5, Y: 2.5}, 0.5, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Cuts) != 1 || r.Cuts[0].Tree.Pos != (bridge.Pos{X: 5.5, Y: 2.5}) {
		t.Errorf("рубить %v, ждали одно дерево (5.5, 2.5)", r.Cuts)
	}
}
