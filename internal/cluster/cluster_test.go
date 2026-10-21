package cluster

import (
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

var (
	protos *knowledge.Prototypes
	once   sync.Once
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

// synth — карта из чанков cw×ch от (0,0): fill(x, y) → 'w' вода, 'i'/'c'/'k'/'s' руда.
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
						res = append(res, bridge.ScanResource{Name: name, X: float64(gx) + 0.5, Y: float64(gy) + 0.5, Amount: 100000})
					}
				}
			}
			scans = append(scans, bridge.ChunkScan{X: cx, Y: cy, Charted: true, Tiles: string(tiles), Resources: res})
		}
	}
	w.Apply(scans)
	return w
}

// карта 320×320: озеро, железо, медь, уголь, камень вокруг центра (160,160)
func testMap(t *testing.T) *world.World {
	return synth(t, 20, 20, func(x, y int) byte { // карта в игре бесконечна — места с запасом
		switch {
		case (x-60)*(x-60)+(y-60)*(y-60) < 30*30:
			return 'w'
		case x >= 200 && x < 240 && y >= 120 && y < 160:
			return 'i'
		case x >= 200 && x < 230 && y >= 200 && y < 230:
			return 'c'
		case x >= 100 && x < 130 && y >= 200 && y < 230:
			return 'k'
		case x >= 250 && x < 270 && y >= 40 && y < 60:
			return 's'
		}
		return '.'
	})
}

func redPlan(t *testing.T, rate float64) *calc.Plan {
	t.Helper()
	plan, err := calc.Solve(pt(t), map[string]float64{"automation-science-pack": rate}, calc.Options{All: true,
		Machines: map[string]string{"crafting": "assembling-machine-1", "smelting": "stone-furnace"}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// Красная наука 0.1/с: добыча железа, меди, угля; плавка; наука (шестерни внутри); станция.
func TestSplitRed(t *testing.T) {
	cs, err := Split(pt(t), redPlan(t, 0.1), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cs, err = AddPower(pt(t), cs, 5, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]*Cluster{}
	for _, c := range cs {
		got[string(c.Kind)+":"+c.Product] = c
		t.Log(c)
	}
	for _, k := range []string{"mine:iron-ore", "mine:copper-ore", "mine:coal", "smelt:iron-plate", "smelt:copper-plate", "make:automation-science-pack", "power:power"} {
		if got[k] == nil {
			t.Errorf("нет кластера %s", k)
		}
	}
	if c := got["make:automation-science-pack"]; c != nil {
		// шестерни — парой прямой подачи внутри: на входе железо и медь
		if c.Sub != "iron-gear-wheel" || !hasIn(c, "iron-plate") || !hasIn(c, "copper-plate") {
			t.Errorf("пара науки: %v (sub %q)", c, c.Sub)
		}
		for _, f := range c.Out {
			if f.Item == "automation-science-pack" && f.Rate < 0.1*Margin-1e-6 {
				t.Errorf("out без запаса: %v", f)
			}
		}
	}
	if c := got["smelt:iron-plate"]; c != nil && !hasIn(c, "coal") {
		t.Errorf("печам нужен уголь: %v", c)
	}
	if c := got["mine:coal"]; c != nil && c.Count < 1 {
		t.Errorf("уголь: %v", c)
	}
}

func hasIn(c *Cluster, item string) bool {
	for _, f := range c.In {
		if f.Item == item {
			return true
		}
	}
	return false
}

// Места: коридоры не пересекаются, прочие — не в зоне месторождений, добыча — на своей руде,
// станция — у воды, лаборатории — рядом с наукой.
func TestPlaceRed(t *testing.T) {
	w := testMap(t)
	cs, err := Split(pt(t), redPlan(t, 0.1), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs = append(cs, Labs(pt(t), map[string]float64{"automation-science-pack": 0.1}, 10, 5), StorageCluster())
	if cs, err = AddPower(pt(t), cs, 5, nil); err != nil {
		t.Fatal(err)
	}
	b := NewBase(pt(t), w, world.Tile{X: 160, Y: 160})
	if err := b.PlaceAll(cs); err != nil {
		t.Fatal(err)
	}
	for i, a := range b.Clusters {
		t.Log(a)
		for _, o := range b.Clusters[i+1:] {
			// у каждого кластера свой коридор, шина — тоже (решение пользователя)
			if a.Area.Grow(Corridor).Overlaps(o.Area.Grow(Corridor)) {
				t.Errorf("коридоры %s и %s пересекаются", a.Name, o.Name)
			}
		}
		if a.Kind == Mine {
			want := a.Product
			x, y := a.Area.Center()
			if r, ok := w.ResourceAt(world.Tile{X: int(x), Y: int(y)}); !ok || r.Name != want {
				t.Errorf("%s не на %s", a.Name, want)
			}
			continue
		}
		for y := a.Area.Min.Y - Corridor; y < a.Area.Max.Y+Corridor; y++ {
			for x := a.Area.Min.X - Corridor; x < a.Area.Max.X+Corridor; x++ {
				if _, ok := w.ResourceAt(world.Tile{X: x, Y: y}); ok && x >= a.Area.Min.X && x < a.Area.Max.X && y >= a.Area.Min.Y && y < a.Area.Max.Y {
					t.Errorf("%s на руде (%d,%d)", a.Name, x, y)
				}
			}
		}
	}
	names := map[string]*Cluster{}
	for _, c := range b.Clusters {
		names[c.Name] = c
	}
	for _, n := range []string{"iron-ore-1", "coal-1", "iron-plate-1", "red-science-1", "power-1", "lab-1", "storage-1"} {
		if names[n] == nil {
			t.Errorf("нет %s: %v", n, keys(names))
		}
	}
	if l, s := names["lab-1"], names["red-science-1"]; l != nil && s != nil {
		lx, ly := l.Area.Center()
		sx, sy := s.Area.Center()
		if d := (lx-sx)*(lx-sx) + (ly-sy)*(ly-sy); d > 80*80 {
			t.Errorf("лаборатории далеко от науки: %v %v", l.Area, s.Area)
		}
	}
}

func keys(m map[string]*Cluster) string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	return strings.Join(s, " ")
}

// Картинка базы на синтетической карте: CLUSTER_PNG=файл go test -run Draw ./internal/cluster
func TestDraw(t *testing.T) {
	path := os.Getenv("CLUSTER_PNG")
	if path == "" {
		t.Skip("CLUSTER_PNG не задан")
	}
	b := redBase(t, 0.45)
	l, err := b.Layout()
	if err != nil {
		t.Fatal(err)
	}
	o := world.RenderOptions{Center: world.Tile{X: 150, Y: 150}, Radius: 90, Scale: 6, Labels: true}
	img := b.W.Render(o)
	b.Draw(img, o)
	l.Draw(img, o)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// Внутренности кластеров красной науки встают и проходят проверку раскладки (ленты к
// портам ещё не проложены — их концы не ошибка).
func TestInteriors(t *testing.T) {
	b := redBase(t, 0.45)
	l, err := b.Layout()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Clusters {
		n := 0
		for _, p := range l.List {
			if p.Tag == Tag(c.Name) {
				n++
			}
		}
		t.Logf("%s: %d построек, порты %v", c.Name, n, c.Ports)
		if n == 0 && c.Kind != Bus { // ленты шины — в Connect
			t.Errorf("%s пуст", c.Name)
		}
		for _, p := range l.List {
			if p.Tag != Tag(c.Name) {
				continue
			}
			for _, tt := range layout.Tiles(b.P, p) {
				if tt.X < c.Area.Min.X || tt.X >= c.Area.Max.X || tt.Y < c.Area.Min.Y || tt.Y >= c.Area.Max.Y {
					t.Errorf("%s: %s вне Area %v", c.Name, p, c.Area)
				}
			}
		}
	}
	for _, e := range l.Validate() {
		if !strings.HasPrefix(e, "столбы не связаны") { // сети кластеров связывает 9.3
			t.Error(e)
		}
	}
}

func redBase(t *testing.T, rate float64) *Base {
	t.Helper()
	cs, err := Split(pt(t), redPlan(t, rate), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs = append(cs, Labs(pt(t), map[string]float64{"automation-science-pack": rate}, 7, 5), StorageCluster())
	if cs, err = AddPower(pt(t), cs, 5, nil); err != nil {
		t.Fatal(err)
	}
	b := NewBase(pt(t), testMap(t), world.Tile{X: 160, Y: 160})
	if err := b.PlaceAll(cs); err != nil {
		t.Fatal(err)
	}
	return b
}

// Этап 1: уголь → станция и печи, руда → пластины; логистика по коридорам, проверка чистая,
// ничего логистики внутри Area кластеров.
func TestConnectStage1(t *testing.T) {
	b := stage1Base(t)
	l, err := b.Layout()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(l, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Validate() {
		t.Error(e)
	}
	for _, p := range l.List {
		if !strings.HasPrefix(p.Tag, LogisticsTag) {
			continue
		}
		for _, c := range b.Clusters {
			if c.Kind == Bus {
				continue // подводы шины — в её площади
			}
			for _, tt := range layout.Tiles(b.P, p) {
				if tt.X >= c.Area.Min.X && tt.X < c.Area.Max.X && tt.Y >= c.Area.Min.Y && tt.Y < c.Area.Max.Y {
					t.Errorf("%s внутри %s", p, c.Name)
				}
			}
		}
	}
	for _, c := range b.Clusters {
		for _, p := range c.Ports {
			if p.In && l.At(p.T) == nil {
				t.Errorf("%s: к входу %s не подведено", c.Name, p.Item)
			}
		}
	}
	// буферы плавки (руда + уголь) — на входящих лентах, по предмету: ящики у лент
	boxes := map[string]int{}
	for _, p := range l.List {
		if p.Name == "wooden-chest" && strings.HasPrefix(p.Tag, LogisticsTag+"/") {
			boxes[p.Tag]++
		}
	}
	for _, n := range []string{"iron-plate-1", "copper-plate-1"} {
		if boxes[LogisticsTag+"/"+n] < 2 {
			t.Errorf("%s: буфера на входящих лентах нет: %v", n, boxes)
		}
	}
	t.Log(l.Summary())
	if path := os.Getenv("CLUSTER_PNG"); path != "" {
		o := world.RenderOptions{Center: world.Tile{X: 150, Y: 150}, Radius: 100, Scale: 5, Labels: true}
		img := b.W.Render(o)
		b.Draw(img, o)
		l.Draw(img, o)
		f, _ := os.Create(path)
		png.Encode(f, img)
		f.Close()
	}
}

func stage1Base(t *testing.T) *Base {
	t.Helper()
	plan, err := calc.Solve(pt(t), map[string]float64{"iron-plate": 1, "copper-plate": 0.5}, calc.Options{All: true,
		Machines: map[string]string{"smelting": "stone-furnace"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := Split(pt(t), plan, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs = append(cs, StorageCluster(), SupplyCluster(pt(t), map[string]float64{"iron-plate": 0.3, "copper-plate": 0.1}))
	if cs, err = AddPower(pt(t), cs, 5, nil); err != nil {
		t.Fatal(err)
	}
	b := NewBase(pt(t), testMap(t), world.Tile{X: 160, Y: 160})
	if err := b.PlaceAll(cs); err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Clusters {
		t.Log(c)
	}
	return b
}

// Красная наука целиком: шестерни и наука — свои кластеры, наука лентой в лаборатории.
func TestConnectRed(t *testing.T) {
	b := redBase(t, 0.1)
	l, err := b.Layout()
	if err != nil {
		t.Fatal(err)
	}
	err = b.Connect(l, nil, nil)
	if path := os.Getenv("CLUSTER_PNG"); path != "" {
		o := world.RenderOptions{Center: world.Tile{X: 150, Y: 150}, Radius: 100, Scale: 5, Labels: true}
		img := b.W.Render(o)
		b.Draw(img, o)
		l.Draw(img, o)
		f, _ := os.Create(path)
		png.Encode(f, img)
		f.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Validate() {
		t.Error(e)
	}
	t.Log(l.Summary())
}

// Рост: тот же вид и продукт, в пределах Room — машин больше, потоки складываются.
func TestGrow(t *testing.T) {
	old := &Cluster{Kind: Mine, Product: "coal", Count: 1, Room: 5, Out: []Flow{{"coal", 0.2}}, Units: []Unit{{"mine:coal", "electric-mining-drill", 1}}}
	add := &Cluster{Kind: Mine, Product: "coal", Count: 2, Out: []Flow{{"coal", 0.5}}, Units: []Unit{{"mine:coal", "electric-mining-drill", 3}}}
	if !Grow(old, add) || old.Count != 3 || old.Out[0].Rate != 0.7 || old.Units[0].Machines != 4 {
		t.Errorf("рост: %+v", old)
	}
	if Grow(old, &Cluster{Kind: Mine, Product: "coal", Count: 3}) {
		t.Error("сверх Room")
	}
	if Grow(old, &Cluster{Kind: Mine, Product: "iron-ore", Count: 1}) {
		t.Error("другой продукт")
	}
}

// Зелёная наука кластерами: у каждого кластера сборки не больше двух входов с лент
// (промежуточное — парой прямой подачи), внутренности и логистика без ошибок проверки.
func TestConnectGreen(t *testing.T) {
	plan, err := calc.Solve(pt(t), map[string]float64{"logistic-science-pack": 0.45, "automation-science-pack": 0.45}, calc.Options{All: true,
		Machines: map[string]string{"crafting": "assembling-machine-1", "smelting": "stone-furnace"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := Split(pt(t), plan, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs = append(cs, Labs(pt(t), map[string]float64{"automation-science-pack": 0.45, "logistic-science-pack": 0.45}, 7, 5))
	if cs, err = AddPower(pt(t), cs, 5, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		t.Log(c, c.Sub)
		if c.Kind == Make && len(c.In) > 2 {
			t.Errorf("%s: входов %d", c.Product, len(c.In))
		}
	}
	b := NewBase(pt(t), testMap(t), world.Tile{X: 160, Y: 160})
	if err := b.PlaceAll(cs); err != nil {
		t.Fatal(err)
	}
	l, err := b.Layout()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(l, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Validate() {
		t.Error(e)
	}
	t.Log(l.Summary())
	// шина: сборка, наука, лаборатории — на ней; все ленты шины текут в одну сторону,
	// входы — делителями
	bus := b.Bus()
	if bus == nil {
		t.Fatal("нет шины")
	}
	dirs := map[int]bool{}
	splitters := 0
	for _, p := range l.List {
		if p.Tag == Tag(bus.Name) && p.Name == "transport-belt" {
			dirs[p.Dir] = true
		}
		if p.Name == "splitter" {
			splitters++
		}
	}
	if len(dirs) != 1 {
		t.Errorf("ленты шины в разные стороны: %v", dirs)
	}
	// клиенты — у шины (записаны), но не в её площади: у каждого свой коридор
	for _, c := range b.Clusters {
		if OnBus(c) && !b.onBusArea(bus, c) {
			t.Errorf("%s не у шины", c.Name)
		}
		if c != bus && bus.Area.Grow(Corridor).Overlaps(c.Area.Grow(Corridor)) {
			t.Errorf("%s: коридор пересекает коридор шины", c.Name)
		}
	}
	if splitters == 0 {
		t.Error("входы не делителями")
	}
}

// Полный ряд (все места запаса заняты): ленты кластера не смотрят в коридор — только внутрь
// площади или в свой выходной порт (иначе чужая лента коридора примет его груз).
func TestFullRowStaysInside(t *testing.T) {
	cs, err := Split(pt(t), redPlan(t, 0.45), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs = append(cs, Labs(pt(t), map[string]float64{"automation-science-pack": 0.45}, 7, 5), SupplyCluster(pt(t), map[string]float64{"iron-plate": 0.3, "copper-plate": 0.1}))
	if cs, err = AddPower(pt(t), cs, 5, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Kind != Mine {
			c.Units[0].Machines *= float64(c.Room) / float64(c.Count)
			c.Count = c.Room
		}
	}
	b := NewBase(pt(t), testMap(t), world.Tile{X: 160, Y: 160})
	if err := b.PlaceAll(cs); err != nil {
		t.Fatal(err)
	}
	l, err := b.Layout()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Clusters {
		out := map[world.Tile]bool{}
		for _, p := range c.Ports {
			if !p.In {
				out[p.T] = true
			}
		}
		for _, p := range l.List {
			if p.Tag != Tag(c.Name) || p.Name != "transport-belt" {
				continue
			}
			n := layout.Step(world.TileOf(p.Pos), p.Dir)
			inside := n.X >= c.Area.Min.X && n.X < c.Area.Max.X && n.Y >= c.Area.Min.Y && n.Y < c.Area.Max.Y
			if !inside && !out[n] {
				t.Errorf("%s: лента %v смотрит наружу в %v", c.Name, p, n)
			}
		}
	}
}
