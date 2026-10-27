package layout

import (
	"strings"
	"sync"
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

var (
	once   sync.Once
	protos *knowledge.Prototypes
)

func pt(t *testing.T) *knowledge.Prototypes {
	t.Helper()
	once.Do(func() {
		var err error
		protos, err = knowledge.Load("../knowledge/testdata/prototypes.json")
		if err != nil {
			t.Fatal(err)
		}
	})
	return protos
}

// synth — карта из чанков cw×ch (от (0,0)): суша; fill(x, y) → 'w' вода, 'i'/'c'/'k'/'s'
// руда (железо/медь/уголь/камень), 'o' нефть, '.' суша.
func synth(t *testing.T, cw, ch int, fill func(x, y int) byte) *world.World {
	t.Helper()
	w := world.New(pt(t))
	ores := map[byte]string{'i': "iron-ore", 'c': "copper-ore", 'k': "coal", 's': "stone", 'o': "crude-oil"}
	var scans []bridge.ChunkScan
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			tiles := make([]byte, world.ChunkSize*world.ChunkSize)
			var res []bridge.ScanResource
			for y := 0; y < world.ChunkSize; y++ {
				for x := 0; x < world.ChunkSize; x++ {
					gx, gy := cx*world.ChunkSize+x, cy*world.ChunkSize+y
					c := fill(gx, gy)
					tiles[y*world.ChunkSize+x] = world.Land
					if c == 'w' {
						tiles[y*world.ChunkSize+x] = world.Water
					}
					if name, ok := ores[c]; ok {
						res = append(res, bridge.ScanResource{Name: name, X: float64(gx) + 0.5, Y: float64(gy) + 0.5, Amount: 1000})
					}
				}
			}
			scans = append(scans, bridge.ChunkScan{X: cx, Y: cy, Charted: true, Tiles: string(tiles), Resources: res})
		}
	}
	w.Apply(scans)
	return w
}

func noErrors(t *testing.T, l *Layout) {
	t.Helper()
	if errs := l.Validate(); len(errs) > 0 {
		t.Errorf("проверка: %d ошибок:\n%s", len(errs), strings.Join(errs, "\n"))
	}
}

func TestRotateAndInserter(t *testing.T) {
	if x, y := Rotate(0, -1, E); x != 1 || y != 0 {
		t.Errorf("север → восток: %v,%v", x, y)
	}
	p := pt(t)
	at := world.Tile{X: 5, Y: 5}
	for _, from := range []world.Tile{{X: 4, Y: 5}, {X: 6, Y: 5}, {X: 5, Y: 4}, {X: 5, Y: 6}} {
		d := InserterDir(p, "inserter", at, from)
		pick, drop := InserterEnds(p, Placement{Name: "inserter", Pos: at.Center(), Dir: d})
		want := world.Tile{X: 2*at.X - from.X, Y: 2*at.Y - from.Y}
		if pick != from || drop != want {
			t.Errorf("из %v: берёт %v, кладёт %v (ждали %v)", from, pick, drop, want)
		}
	}
	// бур слева/справа от колонны выдаёт в колонну
	for _, drill := range []string{"burner-mining-drill", "electric-mining-drill"} {
		s, _ := Size(p, drill, N)
		bx := 10
		l := DrillOutput(p, Placement{Name: drill, Pos: CenterAt(p, drill, E, world.Tile{X: bx - s, Y: 0}), Dir: E})
		r := DrillOutput(p, Placement{Name: drill, Pos: CenterAt(p, drill, W, world.Tile{X: bx + 1, Y: 0}), Dir: W})
		if l.X != bx || r.X != bx {
			t.Errorf("%s: выходы %v %v, колонна x=%d", drill, l, r, bx)
		}
	}
}

// Лента огибает стену воды с проходом, а сплошную стену в 2 тайла — ныряет подземкой.
func TestRouteBelt(t *testing.T) {
	gap := synth(t, 2, 2, func(x, y int) byte {
		if x == 30 && y < 50 {
			return 'w'
		}
		return '.'
	})
	l := New(pt(t), gap)
	if _, err := l.RouteBelt(world.Tile{X: 20, Y: 10}, E, world.Tile{X: 40, Y: 10}, E, "r"); err != nil {
		t.Fatal(err)
	}
	l.AllowEnd(world.Tile{X: 41, Y: 10})
	noErrors(t, l)

	wall := synth(t, 2, 2, func(x, y int) byte {
		if x == 30 || x == 31 {
			return 'w'
		}
		return '.'
	})
	l = New(pt(t), wall)
	if _, err := l.RouteBelt(world.Tile{X: 20, Y: 10}, E, world.Tile{X: 40, Y: 10}, E, "r"); err != nil {
		t.Fatal(err)
	}
	l.AllowEnd(world.Tile{X: 41, Y: 10})
	noErrors(t, l)
	if l.Count()["underground-belt"] != 2 {
		t.Errorf("через сплошную стену — подземка: %v", l.Count())
	}
	// стена шире дальности подземки — не провести
	wide := synth(t, 2, 2, func(x, y int) byte {
		if x >= 28 && x < 36 {
			return 'w'
		}
		return '.'
	})
	if _, err := New(pt(t), wide).RouteBelt(world.Tile{X: 20, Y: 10}, E, world.Tile{X: 40, Y: 10}, E, "r"); err == nil {
		t.Error("через воду шириной 8 лента прошла")
	}
}

// Подземка ленты не встаёт внутрь чужой пары той же оси (стадия 8: вход руды меди между
// концами подземок угля — игра спаривала их накрест, лента рвалась). В ряду 10 чужая пара
// 26 → 30, лента начинается между её концами (27), впереди вода 31–32; прочие ряды закрыты —
// пройти можно только парой накрест (вход 28/29 → выход за водой): нельзя, не провести.
func TestRouteBeltUGCross(t *testing.T) {
	w := synth(t, 2, 2, func(x, y int) byte {
		if x == 31 || x == 32 {
			return 'w'
		}
		return '.'
	})
	l := New(pt(t), w)
	l.Add(Placement{Name: "underground-belt", Pos: world.Tile{X: 26, Y: 10}.Center(), Dir: E, Recipe: "input", Tag: "old"})
	l.Add(Placement{Name: "underground-belt", Pos: world.Tile{X: 30, Y: 10}.Center(), Dir: E, Recipe: "output", Tag: "old"})
	for y := 0; y < 64; y++ {
		for x := 22; x < 38; x++ {
			if y != 10 {
				l.Reserved[world.Tile{X: x, Y: y}] = true
			}
		}
	}
	if _, err := l.RouteBelt(world.Tile{X: 27, Y: 10}, E, world.Tile{X: 40, Y: 10}, E, "new"); err == nil {
		var ugs []string
		for _, p := range l.List {
			if p.Name == "underground-belt" {
				ugs = append(ugs, p.String())
			}
		}
		t.Errorf("пара подземок накрест с чужой: %v", ugs)
	}
}

// Станция у воды с любой стороны: помпа → водяной вход бойлера, паровыход → двигатель →
// двигатель — соединения стыкуются.
func TestSteamPower(t *testing.T) {
	for _, water := range []func(x, y int) bool{
		func(x, y int) bool { return y < 10 },  // север
		func(x, y int) bool { return x >= 40 }, // восток
	} {
		w := synth(t, 2, 2, func(x, y int) byte {
			if water(x, y) {
				return 'w'
			}
			return '.'
		})
		l := New(pt(t), w)
		if _, err := l.SteamPower(world.Tile{X: 20, Y: 20}, 2, "power"); err != nil {
			t.Fatal(err)
		}
		if c := l.Count(); c["offshore-pump"] != 1 || c["boiler"] != 1 || c["steam-engine"] != 2 {
			t.Fatalf("состав: %v", c)
		}
		joined := 0
		for i := 0; i+1 < len(l.List); i++ {
			for _, a := range Connections(pt(t), l.List[i]) {
				for _, b := range Connections(pt(t), l.List[i+1]) {
					if Joined(a, b) {
						joined++
					}
				}
			}
		}
		if joined != 3 {
			t.Errorf("стыков %d из 3: %v", joined, l.List)
		}
		// подача угля: сундук → инсертер → бойлер
		fed := false
		for _, p := range l.List {
			if p.Name == "inserter" {
				pick, drop := InserterEnds(pt(t), p)
				fed = l.At(pick) != nil && l.At(pick).Name == "wooden-chest" && l.At(drop) != nil && l.At(drop).Name == "boiler"
			}
		}
		if !fed {
			t.Errorf("нет подачи угля в бойлер из сундука: %v", l.List)
		}
		l.Poles("small-electric-pole", "power")
		noErrors(t, l)
	}
}

// fluidGroup — тайлы построек, соединённых по жидкости с tile (трубные соединения и
// подземные пары). Жидкость идёт насквозь только через трубы и баки; машины — концы
// (у них входы и выходы — разные ящики).
func fluidGroup(l *Layout, tile world.Tile) map[world.Tile]bool {
	seen := map[world.Tile]bool{}
	var walk func(t world.Tile)
	walk = func(t world.Tile) {
		p := l.At(t)
		if p == nil || seen[world.TileOf(p.Pos)] {
			return
		}
		seen[world.TileOf(p.Pos)] = true
		if t != tile && p.Name != "pipe" && p.Name != "pipe-to-ground" && p.Name != "storage-tank" {
			return
		}
		for _, c := range Connections(l.P, *p) {
			if n := l.At(c.To); n != nil {
				for _, b := range Connections(l.P, *n) {
					if Joined(c, b) {
						walk(c.To)
					}
				}
			}
		}
		if p.Name == "pipe-to-ground" {
			d := (p.Dir + 8) % 16
			cur := world.TileOf(p.Pos)
			for k := 1; k <= l.P.Entities[p.Name].MaxUndergroundDistance+1; k++ {
				cur = Step(cur, d)
				if n := l.At(cur); n != nil && n.Name == p.Name && n.Dir == d {
					walk(cur)
					break
				}
			}
		}
	}
	walk(tile)
	return seen
}

// Труба между двумя баками: через чужую трубу и воду — подземкой, с чужой не соединяется.
func TestRoutePipe(t *testing.T) {
	w := synth(t, 2, 2, func(x, y int) byte {
		if x >= 32 && x < 35 && y < 40 {
			return 'w'
		}
		return '.'
	})
	l := New(pt(t), w)
	a := Placement{Name: "storage-tank", Pos: bridge.Pos{X: 10.5, Y: 10.5}, Tag: "a"}
	b := Placement{Name: "storage-tank", Pos: bridge.Pos{X: 45.5, Y: 10.5}, Tag: "b"}
	l.Add(a)
	l.Add(b)
	for y := 0; y < 40; y++ {
		l.Add(Placement{Name: "pipe", Pos: world.Tile{X: 25, Y: y}.Center(), Tag: "x"})
	}
	var from, to Conn
	for _, c := range Connections(l.P, a) {
		if c.Dir == E {
			from = c
		}
	}
	for _, c := range Connections(l.P, b) {
		if c.Dir == W {
			to = c
		}
	}
	res, err := l.RoutePipe(from, to, "p")
	if err != nil {
		t.Fatal(err)
	}
	g := fluidGroup(l, from.At)
	if !g[world.TileOf(b.Pos)] {
		t.Errorf("баки не соединены: %d построек, %v", len(res), l.Count())
	}
	if g[world.Tile{X: 25, Y: 0}] {
		t.Error("труба соединилась с чужой")
	}
	noErrors(t, l)
	if l.Count()["pipe-to-ground"] < 4 {
		t.Errorf("чужая труба и вода — подземками: %v", l.Count())
	}
	// третий бак — к уже проложенной трубе
	c := Placement{Name: "storage-tank", Pos: bridge.Pos{X: 15.5, Y: 30.5}, Tag: "c"}
	l.Add(c)
	if _, err := l.JoinPipe(Connections(l.P, c)[0], "p"); err != nil {
		t.Fatal(err)
	}
	noErrors(t, l)
	if g := fluidGroup(l, world.TileOf(c.Pos)); !g[world.TileOf(b.Pos)] || g[world.Tile{X: 25, Y: 0}] {
		t.Errorf("третий бак: с сетью %v, с чужой %v", g[world.TileOf(b.Pos)], g[world.Tile{X: 25, Y: 0}])
	}
	// вода шире дальности подземки — не провести
	wide := synth(t, 2, 2, func(x, y int) byte {
		if x >= 20 && x < 32 {
			return 'w'
		}
		return '.'
	})
	l = New(pt(t), wide)
	l.Add(a)
	l.Add(b)
	if _, err := l.RoutePipe(from, to, "p"); err == nil {
		t.Error("через воду шириной 12 труба прошла")
	}
}

// Далёкие сети (нефть в 450 тайлах от базы) связываются цепочкой столбов.
func TestLongPoleChain(t *testing.T) {
	w := synth(t, 16, 3, func(x, y int) byte { return '.' })
	l := New(pt(t), w)
	l.Add(Placement{Name: "small-electric-pole", Pos: bridge.Pos{X: 5.5, Y: 5.5}})
	l.Add(Placement{Name: "small-electric-pole", Pos: bridge.Pos{X: 480.5, Y: 60.5}})
	n := l.connectPoles("small-electric-pole", "p")
	noErrors(t, l)
	if n == 0 || n > 70 {
		t.Errorf("столбов в цепочке %d", n)
	}
}

// Точки начала игры: пара буров друг в друга, бур → печь, 4 бура → сундук; без ошибок.
func TestOutposts(t *testing.T) {
	w := synth(t, 2, 2, func(x, y int) byte {
		switch {
		case x >= 4 && x < 14 && y >= 4 && y < 14:
			return 'k'
		case x >= 30 && x < 40 && y >= 4 && y < 14:
			return 'i'
		case x >= 4 && x < 14 && y >= 30 && y < 40:
			return 's'
		}
		return '.'
	})
	var coal, iron, stone []world.Tile
	for _, p := range w.Patches() {
		switch p.Name {
		case "coal":
			coal = p.Tiles
		case "iron-ore":
			iron = p.Tiles
		case "stone":
			stone = p.Tiles
		}
	}
	l := New(pt(t), w)
	d := "burner-mining-drill"
	for i := 0; i < 5; i++ {
		if err := l.CoalPair(coal, "coal", d, world.Tile{X: 8, Y: 8}, "coal"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := l.DrillFurnace(iron, "iron-ore", d, "stone-furnace", world.Tile{X: 35, Y: 8}, "iron"); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.StoneChest(stone, "stone", d, "wooden-chest", 4, world.Tile{X: 8, Y: 35}, "stone"); err != nil {
		t.Fatal(err)
	}
	noErrors(t, l)
	if c := l.Count(); c[d] != 19 || c["stone-furnace"] != 5 || c["wooden-chest"] != 1 {
		t.Errorf("состав: %v", c)
	}
	// выходы: каждый бур выдаёт в постройку раскладки (бур пары, печь, сундук)
	for _, p := range l.List {
		if p.Name != d {
			continue
		}
		if l.At(DrillOutput(pt(t), p)) == nil {
			t.Errorf("%s выдаёт в пустоту", p)
		}
	}
}
