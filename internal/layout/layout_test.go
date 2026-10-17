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
// руда (железо/медь/уголь/камень), '.' суша.
func synth(t *testing.T, cw, ch int, fill func(x, y int) byte) *world.World {
	t.Helper()
	w := world.New(pt(t))
	ores := map[byte]string{'i': "iron-ore", 'c': "copper-ore", 'k': "coal", 's': "stone"}
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
