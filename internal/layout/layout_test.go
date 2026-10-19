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

// Шахта на месторождении 14×12: буры над рудой, ленты непрерывны до выхода, без пересечений.
func TestMine(t *testing.T) {
	w := synth(t, 2, 2, func(x, y int) byte {
		if x >= 20 && x < 34 && y >= 20 && y < 32 {
			return 'i'
		}
		return '.'
	})
	var patch []world.Tile
	for _, p := range w.Patches() {
		patch = p.Tiles
	}
	for _, drill := range []string{"burner-mining-drill", "electric-mining-drill"} {
		l := New(pt(t), w)
		mr, err := l.Mine(patch, "iron-ore", drill, 0, "mine")
		if err != nil {
			t.Fatal(err)
		}
		l.AllowEnd(mr.Out)
		if pt(t).Entities[drill].Electric {
			l.Poles("small-electric-pole", "power")
		}
		noErrors(t, l)
		if mr.Drills < 6 || mr.Rate <= 0 {
			t.Errorf("%s: буров %d, %.2f/с", drill, mr.Drills, mr.Rate)
		}
		// ограничение числа буров
		l2 := New(pt(t), w)
		if mr2, _ := l2.Mine(patch, "iron-ore", drill, 3, "mine"); mr2.Drills != 3 {
			t.Errorf("%s: просили 3 бура, поставлено %d", drill, mr2.Drills)
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

// Модули на пустой суше: инсертеры берут и кладут куда надо, столбы питают всё.
func TestModulesAndPoles(t *testing.T) {
	l := New(pt(t), nil)
	sm := l.SmeltingColumn(world.Tile{X: 0, Y: 0}, 3, "stone-furnace", "burner-inserter", "smelt")
	sci := l.RedScience(world.Tile{X: 10, Y: 0}, 2, "assembling-machine-1", "inserter", "sci")
	if len(sm.In) != 1 || len(sci.Out) != 1 {
		t.Fatal("порты модулей")
	}
	l.Poles("small-electric-pole", "power")
	noErrors(t, l)
	if l.Count()["small-electric-pole"] == 0 {
		t.Error("столбов нет")
	}
	// без столбов — ошибки питания
	l2 := New(pt(t), nil)
	l2.RedScience(world.Tile{X: 0, Y: 0}, 1, "assembling-machine-1", "inserter", "sci")
	if errs := l2.Validate(); len(errs) == 0 {
		t.Error("сборщики без столбов прошли проверку")
	}
}

// Критерий стадии на синтетической карте: шахты, плавильни, наука, станция — без ошибок.
func TestDemo(t *testing.T) {
	w := synth(t, 4, 4, func(x, y int) byte {
		switch {
		case x >= 10 && x < 26 && y >= 10 && y < 24:
			return 'i'
		case x >= 70 && x < 84 && y >= 12 && y < 24:
			return 'c'
		case x >= 40 && x < 52 && y >= 60 && y < 70:
			return 'k'
		case x >= 100 && y >= 90:
			return 'w'
		}
		return '.'
	})
	l := New(pt(t), w)
	res, err := l.Demo(bridge.Pos{X: 50, Y: 40}, 2)
	if err != nil {
		t.Fatalf("%v; %v", err, res.Notes)
	}
	noErrors(t, l)
	c := l.Count()
	for _, want := range []string{"burner-mining-drill", "stone-furnace", "assembling-machine-1", "offshore-pump", "steam-engine", "small-electric-pole", "transport-belt"} {
		if c[want] == 0 {
			t.Errorf("нет %s: %v", want, c)
		}
	}
	t.Logf("%s; %v", l.Summary(), res.Notes)
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

// Фабрика красной науки: ряды плавки на рудах, блок науки, лаборатории, ленты; столбы
// дотягиваются до уже стоящей сети (Anchors); без ошибок.
func TestScienceFactory(t *testing.T) {
	w := synth(t, 4, 4, func(x, y int) byte {
		switch {
		case x >= 10 && x < 26 && y >= 10 && y < 24:
			return 'i'
		case x >= 70 && x < 84 && y >= 12 && y < 24:
			return 'c'
		}
		return '.'
	})
	l := New(pt(t), w)
	l.Anchors = []Placement{{Name: "small-electric-pole", Pos: bridge.Pos{X: 50.5, Y: 60.5}}}
	if err := l.ScienceFactory(bridge.Pos{X: 45, Y: 30}, 4, 3, 2, 6); err != nil {
		t.Fatal(err)
	}
	noErrors(t, l)
	c := l.Count()
	for name, n := range map[string]int{"electric-mining-drill": 5, "stone-furnace": 5, "assembling-machine-1": 8, "lab": 6, "burner-inserter": 0, "burner-mining-drill": 0} {
		if c[name] != n {
			t.Errorf("%s: %d, ждали %d", name, c[name], n)
		}
	}
	// медь — сбоку с севера на ленту науки (левая полоса; правая — железо)
	var sciIn *Placement
	for i, p := range l.List {
		if p.Tag == "science" && p.Name == "transport-belt" && (sciIn == nil || p.Pos.X < sciIn.Pos.X || p.Pos.X == sciIn.Pos.X && p.Pos.Y < sciIn.Pos.Y) {
			sciIn = &l.List[i]
		}
	}
	side := l.At(world.Tile{X: world.TileOf(sciIn.Pos).X + 1, Y: world.TileOf(sciIn.Pos).Y - 1})
	if side == nil || side.Tag != "belt-copper" || side.Dir != S {
		t.Errorf("медь не подаётся сверху на ленту науки: %+v", side)
	}
	// тупики лент (вход науки, лаборатории) — за ними пусто: иначе чужая лента подхватит
	for _, tag := range []string{"science", "labs"} {
		var end *Placement
		for i, p := range l.List {
			if p.Tag == tag && p.Name == "transport-belt" && p.Dir == E && (end == nil || p.Pos.X > end.Pos.X || p.Pos.X == end.Pos.X && p.Pos.Y < end.Pos.Y) {
				end = &l.List[i]
			}
		}
		if tag == "science" { // верхняя (вход) — с меньшим Y среди самых правых
			for i, p := range l.List {
				if p.Tag == tag && p.Name == "transport-belt" && p.Pos.X == end.Pos.X && p.Pos.Y < end.Pos.Y {
					end = &l.List[i]
				}
			}
		}
		if next := l.At(Step(world.TileOf(end.Pos), E)); next != nil {
			t.Errorf("за тупиком ленты %s стоит %s", tag, next)
		}
	}
	// железо — на правую (южную) полосу: инсертеры ряда кладут на ленту с севера
	for _, p := range l.List {
		if p.Tag == "sci-iron" && p.Name == "inserter" {
			if _, drop := InserterEnds(pt(t), p); l.At(drop) == nil || l.At(drop).Name != "transport-belt" || drop.Y <= world.TileOf(p.Pos).Y {
				t.Errorf("инсертер ряда кладёт не на ленту под собой: %v", p)
			}
		}
	}
	t.Logf("%s", l.Summary())
}

// Зелёная наука к стоящей фабрике красной: ряды плавки, блок прямой подачи, ленты; наука —
// сбоку сверху на ленту лабораторий (левая полоса; красная — правая); без ошибок.
func TestGreenFactory(t *testing.T) {
	w := synth(t, 5, 5, func(x, y int) byte {
		switch {
		case x >= 10 && x < 40 && y >= 10 && y < 30:
			return 'i'
		case x >= 90 && x < 110 && y >= 12 && y < 30:
			return 'c'
		}
		return '.'
	})
	l := New(pt(t), w)
	l.Anchors = []Placement{{Name: "small-electric-pole", Pos: bridge.Pos{X: 60.5, Y: 80.5}}}
	if err := l.ScienceFactory(bridge.Pos{X: 55, Y: 40}, 4, 3, 2, 6); err != nil {
		t.Fatal(err)
	}
	var labsIn *Placement
	for i, p := range l.List {
		if p.Tag == "labs" && p.Name == "transport-belt" && (labsIn == nil || p.Pos.X < labsIn.Pos.X) {
			labsIn = &l.List[i]
		}
	}
	mark := len(l.List)
	if err := l.GreenFactory(bridge.Pos{X: 55, Y: 40}, world.TileOf(labsIn.Pos), 2, 3, 1); err != nil {
		t.Fatal(err)
	}
	noErrors(t, l)
	c := map[string]int{}
	for _, p := range l.List[mark:] {
		c[p.Recipe]++
	}
	for _, r := range []string{"copper-cable", "electronic-circuit", "inserter", "iron-gear-wheel", "transport-belt", "logistic-science-pack"} {
		if c[r] == 0 {
			t.Errorf("нет сборщика %s: %v", r, c)
		}
	}
	// инсертеры и ленты — на общую ленту по разным полосам: инсертеры через отвод сбоку
	// (сверху), ленты — длинноруким прямо (с дальней стороны)
	var stub, longIns *Placement
	for i, p := range l.List[mark:] {
		if p.Tag == "green" && p.Name == "transport-belt" && p.Dir == S {
			stub = &l.List[mark+i]
		}
		if p.Name == "long-handed-inserter" {
			longIns = &l.List[mark+i]
		}
	}
	if stub == nil || longIns == nil {
		t.Fatalf("нет отвода или длиннорукого: %v %v", stub, longIns)
	}
	if _, drop := InserterEnds(pt(t), *longIns); l.At(drop) == nil || l.At(drop).Dir != W || l.At(Step(world.TileOf(stub.Pos), S)).Dir != W {
		t.Errorf("длиннорукий и отвод — не на общую ленту")
	}
	side := l.At(world.Tile{X: world.TileOf(labsIn.Pos).X + 1, Y: world.TileOf(labsIn.Pos).Y - 1})
	if side == nil || side.Tag != "green-belt-science" || side.Dir != S {
		t.Errorf("зелёная не подаётся сверху на ленту лабораторий: %+v", side)
	}
	t.Logf("%s", l.Summary())
}

// Сталь: столбики «бур → печь → инсертер → печь → инсертер → сундук»: бур выдаёт в печь,
// инсертеры берут из печи и кладут в печь/сундук; столбы к сети; без ошибок.
func TestSteelRow(t *testing.T) {
	w := synth(t, 2, 2, func(x, y int) byte {
		if x >= 10 && x < 30 && y >= 10 && y < 30 {
			return 'i'
		}
		return '.'
	})
	l := New(pt(t), w)
	l.Anchors = []Placement{{Name: "small-electric-pole", Pos: bridge.Pos{X: 40.5, Y: 40.5}}}
	if err := l.SteelRow(w.Patches()[0].Tiles, 3, world.Tile{X: 20, Y: 20}, "steel"); err != nil {
		t.Fatal(err)
	}
	l.Poles("small-electric-pole", "power")
	noErrors(t, l)
	for _, p := range l.List {
		if p.Name == "inserter" {
			pick, drop := InserterEnds(pt(t), p)
			if src, dst := l.At(pick), l.At(drop); src == nil || dst == nil || src.Name != "stone-furnace" {
				t.Errorf("инсертер %v: берёт %v, кладёт %v", p, src, dst)
			}
		}
	}
	if c := l.Count(); c["stone-furnace"] != 6 || c["wooden-chest"] != 3 {
		t.Errorf("состав: %v", c)
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

// Нефть: насосы → нефтезавод → химзаводы (пластик, сера), вода от помпы; жидкости не смешаны.
func TestOil(t *testing.T) {
	w := synth(t, 3, 3, func(x, y int) byte {
		switch {
		case x > 80:
			return 'w'
		case (x == 20 || x == 26 || x == 32) && (y == 20 || y == 27):
			return 'o'
		}
		return '.'
	})
	l := New(pt(t), w)
	if err := l.Oil(bridge.Pos{X: 30, Y: 30}, 3, 2, 1); err != nil {
		t.Fatal(err)
	}
	noErrors(t, l)
	c := l.Count()
	if c["pumpjack"] != 3 || c["oil-refinery"] != 1 || c["chemical-plant"] != 3 || c["offshore-pump"] != 1 {
		t.Fatalf("состав: %v", c)
	}
	var ref Placement
	var jacks, plants []Placement
	for _, p := range l.List {
		switch p.Name {
		case "oil-refinery":
			ref = p
		case "pumpjack":
			jacks = append(jacks, p)
		case "chemical-plant":
			plants = append(plants, p)
		}
	}
	ci, _ := FluidConn(l.P, ref, "crude-oil")
	gasOut, _ := FluidConn(l.P, ref, "petroleum-gas")
	crude := fluidGroup(l, ci.To)
	gas := fluidGroup(l, gasOut.To)
	if !crude[world.TileOf(ref.Pos)] || !gas[world.TileOf(ref.Pos)] {
		t.Error("нефтезавод не подключён")
	}
	for _, p := range plants {
		if !gas[world.TileOf(p.Pos)] {
			t.Errorf("газ не идёт в химзавод %v", p.Pos)
		}
	}
	for _, j := range jacks {
		if !crude[world.TileOf(j.Pos)] {
			t.Errorf("насос %v не соединён с нефтезаводом", j.Pos)
		}
	}
	for _, p := range plants {
		if crude[world.TileOf(p.Pos)] {
			t.Errorf("нефть попала в химзавод %v", p.Pos)
		}
	}
	// пластик и сера — лентой к «базе»
	if sulfur, err := l.OilBelt(world.Tile{X: 60, Y: 70}, S, "ob"); err != nil || !sulfur {
		t.Fatal(sulfur, err)
	}
	l.AllowEnd(world.Tile{X: 60, Y: 71})
	l.Poles("small-electric-pole", "ob")
	noErrors(t, l)
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

// Угольная шахта: пары электробуров навстречу, общий сундук между ними.
func TestChestMine(t *testing.T) {
	w := synth(t, 2, 2, func(x, y int) byte {
		if x >= 10 && x < 30 && y >= 10 && y < 20 {
			return 'k'
		}
		return '.'
	})
	l := New(pt(t), w)
	var patch []world.Tile
	for x := 10; x < 30; x++ {
		for y := 10; y < 20; y++ {
			patch = append(patch, world.Tile{X: x, Y: y})
		}
	}
	if err := l.ChestMine(patch, "coal", 3, world.Tile{X: 5, Y: 5}, "m"); err != nil {
		t.Fatal(err)
	}
	if c := l.Count(); c["electric-mining-drill"] != 6 || c["wooden-chest"] != 3 {
		t.Fatalf("состав %v", c)
	}
	for _, p := range l.List {
		if p.Name == "electric-mining-drill" {
			if o := l.At(DrillOutput(l.P, p)); o == nil || o.Name != "wooden-chest" {
				t.Errorf("%s выдаёт мимо сундука", p)
			}
		}
	}
}

// Лента угля от шахты мимо кормушек: инсертер с ленты в каждый сундук.
func TestFeedBelt(t *testing.T) {
	w := synth(t, 3, 2, func(x, y int) byte {
		if x >= 10 && x < 26 && y >= 10 && y < 26 {
			return 'k'
		}
		return '.'
	})
	l := New(pt(t), w)
	var patch []world.Tile
	for x := 10; x < 26; x++ {
		for y := 10; y < 26; y++ {
			patch = append(patch, world.Tile{X: x, Y: y})
		}
	}
	mr, err := l.Mine(patch, "coal", "electric-mining-drill", 6, "m")
	if err != nil {
		t.Fatal(err)
	}
	chests := []world.Tile{{X: 60, Y: 20}, {X: 70, Y: 40}, {X: 80, Y: 15}}
	for _, c := range chests {
		l.Add(Placement{Name: "wooden-chest", Pos: c.Center(), Tag: "feed"})
	}
	var groups [][]world.Tile
	for _, c := range chests {
		groups = append(groups, []world.Tile{c})
	}
	if fed, err := l.FeedBelt(mr.Out, mr.OutDir, groups, "fb"); err != nil || fed != 3 {
		t.Fatal(fed, err)
	}
	l.Poles("small-electric-pole", "fb")
	noErrors(t, l)
	for _, c := range chests {
		fed := false
		for _, p := range l.List {
			if p.Name == "inserter" && p.Tag == "fb" {
				if _, drop := InserterEnds(l.P, p); drop == c {
					pick, _ := InserterEnds(l.P, p)
					fed = l.At(pick) != nil && isBelt(l.At(pick).Name)
				}
			}
		}
		if !fed {
			t.Errorf("кормушка %v без инсертера с ленты", c)
		}
	}
}

// Склад пластин: бур → печь → инсертер → сундук.
func TestPlateRow(t *testing.T) {
	w := synth(t, 2, 2, func(x, y int) byte {
		if x >= 10 && x < 40 && y >= 10 && y < 20 {
			return 'i'
		}
		return '.'
	})
	l := New(pt(t), w)
	var patch []world.Tile
	for x := 10; x < 40; x++ {
		for y := 10; y < 20; y++ {
			patch = append(patch, world.Tile{X: x, Y: y})
		}
	}
	if err := l.PlateRow(patch, "iron-ore", 8, world.Tile{X: 5, Y: 5}, "p"); err != nil {
		t.Fatal(err)
	}
	l.Poles("small-electric-pole", "p")
	noErrors(t, l)
	if c := l.Count(); c["electric-mining-drill"] != 8 || c["wooden-chest"] != 8 {
		t.Errorf("состав %v", c)
	}
}

// Синяя наука: блок, ряды плавки, лента синей под лабораториями.
func TestBlueFactory(t *testing.T) {
	w := synth(t, 4, 4, func(x, y int) byte {
		switch {
		case x >= 10 && x < 40 && y >= 10 && y < 22:
			return 'i'
		case x >= 10 && x < 40 && y >= 90 && y < 102:
			return 'c'
		}
		return '.'
	})
	l := New(pt(t), w)
	lm := l.Labs(world.Tile{X: 70, Y: 38}, 3, "inserter", "labs")
	l.AllowEnd(world.Tile{X: lm.LT.X - 1, Y: lm.LT.Y})
	var labs []Placement
	for _, p := range l.List {
		if p.Name == "lab" {
			labs = append(labs, p)
		}
	}
	if err := l.BlueFactory(bridge.Pos{X: 60, Y: 60}, labs, 4, 3); err != nil {
		t.Fatal(err)
	}
	noErrors(t, l)
	c := l.Count()
	if c["assembling-machine-2"]+c["assembling-machine-1"] != 8 {
		t.Errorf("сборщиков %v", c)
	}
}
