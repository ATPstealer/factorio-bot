package exec

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/plan"
	"factorio-bot/internal/world"
)

// StageTargets — цели этапов кластерной базы (game-stages.md), шт/с.
var StageTargets = map[int]map[string]float64{
	1: {"iron-plate": 1, "copper-plate": 0.5},
	2: {"automation-science-pack": 0.1},
	3: {"automation-science-pack": 0.45, "logistic-science-pack": 0.45},
}

// StageUntil — до каких технологий изучать на этапе (пусто — всё на пакетах этапа для ракеты).
var StageUntil = map[int][]string{
	3: {"oil-gathering", "automation-2"}, // нужное до нефти и стали
}

// labSeconds — секунд исследования на пакет (лаборатория со скоростью 1): технологии на пути
// к ракете — красные 1225 с / 175, красные+зелёные 65 700 с / 2435.
var labSeconds = map[int]float64{2: 7, 3: 27}

// clusterMode — база строится кластерами: есть кластеры или цель — этап ("stage-N").
func (r *Runner) clusterMode() bool {
	if len(r.Saved.Clusters) > 0 {
		return true
	}
	for _, g := range r.Saved.Goals {
		if g.Kind == "stage" || strings.HasPrefix(g.Item, "stage-") {
			return true
		}
	}
	return false
}

// stageReserve — запас роста out: этапы 0–4 — ×5, 5–6 — ×2, дальше ×1.5.
func stageReserve(n int) float64 {
	switch {
	case n <= 4:
		return 5
	case n <= 6:
		return 2
	}
	return 1.5
}

// clusterStage — раскладка этапа n кластерами (задача "stage-N"): кластеры под цели этапа,
// места (вне раскладок прошлых задач), внутренности, логистика по коридорам, столбы к
// стоящей сети. Кластеры — в Saved.Clusters, картинка — run/clusters.png.
func (r *Runner) clusterStage(s *plan.State, n int) (*layout.Layout, error) {
	targets := StageTargets[n]
	if targets == nil {
		return nil, fmt.Errorf("нет целей этапа %d", n)
	}
	p := r.H.Protos
	// свободное у кластеров прошлых этапов: выход минус то, что берут их потребители
	have, powerFree := map[string]float64{}, 0.0
	for _, c := range r.Saved.Clusters {
		for _, f := range c.Out {
			have[f.Item] += f.Rate
		}
		for _, f := range c.In {
			have[f.Item] -= f.Rate
		}
		if c.Kind == cluster.Power {
			powerFree += c.PowerW
		} else {
			powerFree -= c.PowerW
		}
	}
	for it, v := range have {
		if v <= 1e-9 {
			delete(have, it)
		}
	}
	// снабжение стройки — с первого этапа кластеров (один на базу): его пластины — в расчёт
	var supply *cluster.Cluster
	hasSupply := false
	for _, c := range r.Saved.Clusters {
		hasSupply = hasSupply || c.Kind == cluster.Supply
	}
	if !hasSupply {
		in := map[string]float64{"iron-plate": 0.3, "copper-plate": 0.1}
		t := map[string]float64{}
		for it, v := range targets {
			t[it] = v
		}
		for it, v := range in {
			t[it] += v
		}
		targets = t
		supply = cluster.SupplyCluster(p, in)
	}
	cp, err := calc.Solve(p, targets, calc.Options{All: true, Have: have,
		Machines: map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"}})
	if err != nil {
		return nil, err
	}
	for it, v := range cp.Given {
		have[it] -= v
	}
	cs, err := cluster.Split(p, cp, stageReserve(n), have)
	if err != nil {
		return nil, err
	}
	packs := map[string]float64{}
	for it, v := range targets {
		if strings.HasSuffix(it, "-science-pack") {
			packs[it] = v
		}
	}
	if len(packs) > 0 {
		cs = append(cs, cluster.Labs(p, packs, labSeconds[n], stageReserve(n)))
	}
	if n == 1 {
		cs = append(cs, cluster.StorageCluster())
	}
	if supply != nil {
		cs = append(cs, supply)
	}
	need := 0.0
	for _, c := range cs {
		need += c.PowerW
	}
	if need > powerFree { // станции прошлых этапов не тянут — своя
		if cs, err = cluster.AddPower(p, cs, stageReserve(n), have); err != nil {
			return nil, err
		}
	}
	// сначала — запас роста кластеров прошлых этапов (тот же продукт): больше машин в их
	// площади, а не новый кластер где-то ещё
	// (на копиях: раскладка может не выйти — тогда прошлые кластеры не тронуты)
	var grown []*cluster.Cluster
	copyOf := map[*cluster.Cluster]*cluster.Cluster{}
	kept := cs[:0]
	for _, c := range cs {
		g := false
		for _, ex := range r.Saved.Clusters {
			cp := copyOf[ex]
			if cp == nil {
				x := *ex
				x.In = append([]cluster.Flow(nil), ex.In...)
				x.Out = append([]cluster.Flow(nil), ex.Out...)
				x.Internal = append([]cluster.Flow(nil), ex.Internal...)
				x.Units = append([]cluster.Unit(nil), ex.Units...)
				cp = &x
			}
			if cluster.Grow(cp, c) {
				if copyOf[ex] == nil {
					grown = append(grown, cp)
				}
				copyOf[ex], g = cp, true
				r.journal("кластер %s растёт: %d мест из %d", cp.Name, cp.Count, cp.Room)
				break
			}
		}
		if !g {
			kept = append(kept, c)
		}
	}
	cs = kept
	// точки нулевого этапа — не помеха: кластер займёт их место, стройка их снимет
	var startObjs []world.Object
	startTag := map[bridge.Pos]string{}
	for _, o := range r.Saved.Owned {
		if tag, _, ok := plan.ModuleOf(o.Role); ok {
			startObjs = append(startObjs, world.Object{Name: o.Name, Pos: o.Pos})
			startTag[o.Pos] = tag
		}
	}
	restore := r.H.World.Hide(startObjs)
	defer restore()
	base := cluster.NewBase(p, r.H.World, world.TileOf(s.PlayerPos))
	base.Usable = func(e string) bool { return p.EntityAvailable(e, s.Av) }
	base.Log = r.journal
	base.Avoid = map[world.Tile]bool{}
	for k, list := range r.Saved.Designs {
		if _, start := plan.ModuleOre(k); k == fmt.Sprintf("stage-%d", n) || start {
			continue
		}
		for _, pl := range list {
			for _, t := range layout.Tiles(p, pl) {
				base.Avoid[t] = true
			}
		}
	}
	for _, c := range r.Saved.Clusters { // кластеры прошлых этапов
		base.Clusters = append(base.Clusters, c)
	}
	var existing []layout.Placement // логистика прошлых этапов
	for k, list := range r.Saved.Designs {
		if strings.HasPrefix(k, "stage-") && k != fmt.Sprintf("stage-%d", n) {
			for _, pl := range list {
				if strings.HasPrefix(pl.Tag, cluster.LogisticsTag) {
					existing = append(existing, pl)
				}
			}
		}
	}
	var anchors []layout.Placement
	near := s.PlayerPos
	poles, _ := r.B.FindNearest(bridge.FindArgs{Type: "electric-pole", Radius: 1000, Position: &near, Limit: 80})
	for _, e := range poles {
		anchors = append(anchors, layout.Placement{Name: e.Name, Pos: e.Position})
	}
	// ленту к новому кластеру не провести (без подземок ленты не пересекаются — встал по ту
	// сторону чужой ленты) — его место запретить и разметить заново
	old := len(base.Clusters)
	var fresh []*cluster.Cluster
	var l *layout.Layout
	for try := 0; ; try++ {
		base.Clusters = base.Clusters[:old]
		base.Reset()
		if err := base.PlaceAll(cs); err != nil {
			return nil, err
		}
		fresh = base.Clusters[old:]
		var err error
		if l, err = base.Layout(fresh...); err != nil {
			return nil, err
		}
		for _, c := range grown {
			if err := r.growInterior(l, c, s); err != nil {
				return nil, err
			}
		}
		var names []string
		for _, c := range fresh {
			names = append(names, c.Name)
		}
		r.journal("этап %d, попытка %d: новые кластеры %v, растут %d", n, try+1, names, len(grown))
		err = base.Connect(l, anchors, existing, fresh...)
		if err == nil {
			break
		}
		var ce *cluster.ConnectError
		if !errors.As(err, &ce) || try >= 5 || !slices.Contains(fresh, ce.Consumer) {
			r.drawBase(base, l) // что успело — на картинку
			return nil, err
		}
		r.journal("кластер %s: %v — ставлю в другом месте", ce.Consumer.Name, err)
		a := ce.Consumer.Area
		for y := a.Min.Y; y < a.Max.Y; y++ {
			for x := a.Min.X; x < a.Max.X; x++ {
				base.Avoid[world.Tile{X: x, Y: y}] = true
			}
		}
	}
	// точки нулевого этапа, задетые раскладкой, — закрыть (не восстанавливать)
	used := map[world.Tile]bool{}
	for _, pl := range l.List {
		for _, t := range layout.Tiles(p, pl) {
			used[t] = true
		}
	}
	for _, c := range fresh {
		for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
			for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
				used[world.Tile{X: x, Y: y}] = true
			}
		}
	}
	for _, o := range r.Saved.Owned {
		tag, ok := startTag[o.Pos]
		if !ok || r.Saved.Done["evicted:"+tag] {
			continue
		}
		for _, t := range layout.Tiles(p, layout.Placement{Name: o.Name, Pos: o.Pos}) {
			if used[t] {
				if r.Saved.Done == nil {
					r.Saved.Done = map[string]bool{}
				}
				r.Saved.Done["evicted:"+tag] = true
				r.journal("точка %s — под кластером: снимается при стройке", tag)
				break
			}
		}
	}
	for ex, cp := range copyOf { // рост вышел — в кластеры
		*ex = *cp
	}
	r.Saved.Clusters = append(r.Saved.Clusters, fresh...)
	for _, c := range fresh {
		r.journal("кластер %s", c)
	}
	r.drawBase(base, l)
	return l, nil
}

// drawBase — картинка базы с кластерами и раскладкой в run/clusters.png.
func (r *Runner) drawBase(b *cluster.Base, l *layout.Layout) {
	if r.StatePath == "" {
		return
	}
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, c := range b.Clusters {
		a := c.Area.Grow(cluster.Corridor)
		minX, minY = min(minX, a.Min.X), min(minY, a.Min.Y)
		maxX, maxY = max(maxX, a.Max.X), max(maxY, a.Max.Y)
	}
	o := world.RenderOptions{Center: world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2},
		Radius: max(maxX-minX, maxY-minY)/2 + 10, Scale: 4, Labels: true}
	img := b.W.Render(o)
	b.Draw(img, o)
	l.Draw(img, o)
	f, err := os.Create(filepath.Join(filepath.Dir(r.StatePath), "clusters.png"))
	if err != nil {
		return
	}
	defer f.Close()
	png.Encode(f, img)
}

// ClusterStatus — состояние кластера по последнему замеру монитора.
type ClusterStatus struct {
	Name     string         `json:"name"`
	Machines int            `json:"machines"` // построено машин
	Planned  int            `json:"planned"`  // по плану (Count)
	Status   map[string]int `json:"status"`   // статус → машин
	Out      []cluster.Flow `json:"out"`      // план
	OutReal  []cluster.Flow `json:"out_real"` // оценка: план × доля работающих
	Buffer   map[string]int `json:"buffer,omitempty"`
	Problems []string       `json:"problems,omitempty"`
	Short    []string       `json:"short,omitempty"` // каких входов не хватает
}

// machineTypes — машины кластера, по которым мерим работу.
var machineTypes = map[string]bool{"furnace": true, "assembling-machine": true, "mining-drill": true, "lab": true, "boiler": true}

// Monitor — замер кластеров по статусам машин и содержимому ящиков (раз в минуту из Step):
// out ≈ план × доля работающих; нехватка входа (нет ингредиентов/топлива у большинства),
// нет питания, руда кончилась — проблемы; выход полон — норма. Пишет run/clusters.txt/json.
func (r *Runner) Monitor(s *plan.State) []ClusterStatus {
	tagOf := map[string]string{} // роль → тег кластера
	for kind, list := range r.Saved.Designs {
		for i, pl := range list {
			tagOf[plan.BuildRole(kind, i)] = pl.Tag
		}
	}
	byTag := map[string][]*plan.Owned{}
	for _, o := range s.Owned {
		if t := tagOf[o.Role]; t != "" {
			byTag[t] = append(byTag[t], o)
		}
	}
	var res []ClusterStatus
	for _, c := range r.Saved.Clusters {
		st := ClusterStatus{Name: c.Name, Planned: c.Count, Status: map[string]int{}, Out: c.Out, Buffer: map[string]int{}}
		for _, o := range byTag[cluster.Tag(c.Name)] {
			e := r.H.Protos.Entities[o.Name]
			if e.Type == "container" {
				for it, n := range o.Inv["chest"] {
					st.Buffer[it] += n
				}
			}
			if !machineTypes[e.Type] {
				continue
			}
			st.Machines++
			name := o.Status
			if name == "" {
				name = "unknown"
			}
			st.Status[name]++
		}
		working := st.Status["working"] + st.Status["low_power"]
		share := 0.0
		if st.Machines > 0 {
			share = float64(working) / float64(max(st.Machines, c.Count))
		}
		for _, f := range c.Out {
			st.OutReal = append(st.OutReal, cluster.Flow{Item: f.Item, Rate: f.Rate * share})
		}
		starve := st.Status["no_ingredients"] + st.Status["no_fuel"] + st.Status["no_input_fluid"]
		switch {
		case st.Machines == 0:
			st.Problems = append(st.Problems, "не построен")
		case st.Status["no_power"]*2 > st.Machines:
			st.Problems = append(st.Problems, "нет питания")
		case st.Status["no_minable_resources"] > 0:
			st.Problems = append(st.Problems, fmt.Sprintf("руда кончилась у %d буров", st.Status["no_minable_resources"]))
		case starve*2 > st.Machines:
			st.Problems = append(st.Problems, fmt.Sprintf("нехватка входа (%s): %d из %d машин ждут", flowNames(c.In), starve, st.Machines))
			// без топлива — уголь; без ингредиентов — прочие входы
			for _, f := range c.In {
				if f.Item == "coal" && st.Status["no_fuel"] > 0 || f.Item != "coal" && st.Status["no_ingredients"] > 0 {
					st.Short = append(st.Short, f.Item)
				}
			}
		}
		if len(st.Buffer) == 0 {
			st.Buffer = nil
		}
		res = append(res, st)
	}
	r.writeStatus(res)
	return res
}

func flowNames(fs []cluster.Flow) string {
	s := ""
	for i, f := range fs {
		if i > 0 {
			s += ", "
		}
		s += f.Item
	}
	return s
}

// writeStatus — run/clusters.txt (читаемо) и run/clusters.json.
func (r *Runner) writeStatus(st []ClusterStatus) {
	if r.StatePath == "" || len(st) == 0 {
		return
	}
	dir := filepath.Dir(r.StatePath)
	if data, err := json.MarshalIndent(st, "", " "); err == nil {
		os.WriteFile(filepath.Join(dir, "clusters.json"), data, 0o644)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Кластеры — %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	for _, c := range st {
		fmt.Fprintf(&b, "%-16s машин %d/%d", c.Name, c.Machines, c.Planned)
		for _, f := range c.Out {
			real := 0.0
			for _, g := range c.OutReal {
				if g.Item == f.Item {
					real = g.Rate
				}
			}
			fmt.Fprintf(&b, "  %s %.2f/с из %.2f", f.Item, real, f.Rate)
		}
		keys := make([]string, 0, len(c.Status))
		for k := range c.Status {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("  [")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(" ")
			}
			fmt.Fprintf(&b, "%s:%d", k, c.Status[k])
		}
		b.WriteString("]")
		if len(c.Buffer) > 0 {
			fmt.Fprintf(&b, "  буфер %v", c.Buffer)
		}
		for _, p := range c.Problems {
			fmt.Fprintf(&b, "\n    ✗ %s", p)
		}
		b.WriteString("\n")
	}
	os.WriteFile(filepath.Join(dir, "clusters.txt"), []byte(b.String()), 0o644)
}

// DryDesign — спроектировать раскладку kind по текущему состоянию без стройки и без
// сохранения состояния (CLI: посмотреть план этапа и run/clusters.png).
func (r *Runner) DryDesign(kind string) (plan.Built, error) {
	s, err := r.Snapshot()
	if err != nil {
		return plan.Built{}, err
	}
	b, ok := r.design(s, kind)
	if !ok {
		return b, fmt.Errorf("раскладка %s не вышла (см. журнал)", kind)
	}
	return b, nil
}

// stageOrder — очередь стройки этапа кластерами: столбы (связь с сетью), потом кластеры по
// одному с их входящими лентами — добыча, станция, плавка, сборка, лаборатории, склад.
// Не этап — nil (строится целиком).
func (r *Runner) stageOrder(kind string, list []layout.Placement) [][]string {
	if !strings.HasPrefix(kind, "stage-") {
		return nil
	}
	tags := map[string]bool{}
	for _, p := range list {
		tags[p.Tag] = true
	}
	// цепочками: кластер — сразу после своих поставщиков (уголь → станция, руда → плавка),
	// начиная со станции, потом по видам потребителей
	rank := map[cluster.Kind]int{cluster.Power: 0, cluster.Smelt: 1, cluster.Supply: 2, cluster.Make: 3, cluster.Lab: 4, cluster.Mine: 5, cluster.Storage: 6}
	var cs []*cluster.Cluster
	for _, c := range r.Saved.Clusters {
		if tags[cluster.Tag(c.Name)] {
			cs = append(cs, c)
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return rank[cs[i].Kind] < rank[cs[j].Kind] })
	order := [][]string{{cluster.LogisticsTag}}
	seen := map[*cluster.Cluster]bool{}
	var visit func(c *cluster.Cluster)
	visit = func(c *cluster.Cluster) {
		if seen[c] {
			return
		}
		seen[c] = true
		for _, in := range c.In {
			for _, o := range cs { // поставщик этого входа в том же этапе
				for _, f := range o.Out {
					if f.Item == in.Item && o != c {
						visit(o)
					}
				}
			}
		}
		order = append(order, []string{cluster.Tag(c.Name), cluster.LogisticsTag + "/" + c.Name})
	}
	for _, c := range cs {
		visit(c)
	}
	return order
}

// growInterior — машины роста кластера c (Count уже больше): внутренность целиком заново
// (свои стоящие постройки — будто их нет), в раскладку — только новые размещения.
func (r *Runner) growInterior(l *layout.Layout, c *cluster.Cluster, s *plan.State) error {
	p := r.H.Protos
	tag := cluster.Tag(c.Name)
	have := map[string]bool{}
	var objs []world.Object
	for k, list := range r.Saved.Designs {
		if !strings.HasPrefix(k, "stage-") {
			continue
		}
		for _, pl := range list {
			if pl.Tag == tag {
				have[fmt.Sprintf("%s@%.1f,%.1f/%d", pl.Name, pl.Pos.X, pl.Pos.Y, pl.Dir)] = true
				objs = append(objs, world.Object{Name: pl.Name, Pos: pl.Pos})
			}
		}
	}
	restore := r.H.World.Hide(objs)
	defer restore()
	tl := layout.New(p, r.H.World)
	tl.Usable = l.Usable
	for t := range l.Reserved {
		tl.Reserved[t] = true
	}
	for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
		for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
			delete(tl.Reserved, world.Tile{X: x, Y: y})
		}
	}
	if err := cluster.Interior(tl, c, tag); err != nil {
		return fmt.Errorf("рост %s: %w", c.Name, err)
	}
	n := 0
	for _, pl := range tl.List {
		if !have[fmt.Sprintf("%s@%.1f,%.1f/%d", pl.Name, pl.Pos.X, pl.Pos.Y, pl.Dir)] {
			l.Add(pl)
			n++
		}
	}
	r.journal("кластер %s: +%d построек роста", c.Name, n)
	return nil
}

// growShort — нехватка входа второй замер подряд: кластер-поставщик этого входа растёт в своём
// запасе (задача "grow-<имя>": машины роста рядом с его стоящими) — правило «сначала in».
func (r *Runner) growShort(st []ClusterStatus) {
	if r.short == nil {
		r.short = map[string]int{}
	}
	for _, c := range st {
		if len(c.Short) == 0 {
			delete(r.short, c.Name)
			continue
		}
		if r.short[c.Name]++; r.short[c.Name] < 2 {
			continue
		}
		for _, item := range c.Short {
			for _, src := range r.Saved.Clusters {
				if src.Name == c.Name || src.Count >= src.Room || !hasOut(src, item) {
					continue
				}
				// с номером места: раскладка роста запоминается — следующий рост — новая задача
				kind := fmt.Sprintf("grow-%s@%d", src.Name, src.Count+1)
				pending := false
				for _, g := range r.Saved.Grow {
					pending = pending || strings.HasPrefix(g, "grow-"+src.Name+"@")
				}
				if pending {
					break
				}
				r.Saved.Grow = append(r.Saved.Grow, kind)
				r.journal("кластер %s: не хватает %s — растёт %s (%d → %d из %d)", c.Name, item, src.Name, src.Count, src.Count+1, src.Room)
				break
			}
		}
		r.short[c.Name] = 0
	}
}

func hasOut(c *cluster.Cluster, item string) bool {
	for _, f := range c.Out {
		if f.Item == item {
			return true
		}
	}
	return false
}

// growDesign — раскладка роста кластера name на одно место (задача "grow-<имя>"): потоки и
// машины — пропорционально; в раскладке — только новые постройки.
func (r *Runner) growDesign(s *plan.State, spec string) (*layout.Layout, error) {
	name, n := spec, 0
	if i := strings.LastIndex(spec, "@"); i > 0 {
		name = spec[:i]
		fmt.Sscanf(spec[i+1:], "%d", &n)
	}
	var c *cluster.Cluster
	for _, x := range r.Saved.Clusters {
		if x.Name == name {
			c = x
		}
	}
	if c == nil || c.Count >= c.Room || n > 0 && c.Count >= n {
		return nil, fmt.Errorf("кластер %s: расти некуда (%s)", name, spec)
	}
	cp := *c
	k := float64(c.Count+1) / float64(c.Count)
	scale := func(fs []cluster.Flow) []cluster.Flow {
		var res []cluster.Flow
		for _, f := range fs {
			res = append(res, cluster.Flow{Item: f.Item, Rate: f.Rate * k})
		}
		return res
	}
	cp.In, cp.Out, cp.Internal, cp.PowerW = scale(c.In), scale(c.Out), scale(c.Internal), c.PowerW*k
	cp.Units = nil
	for _, u := range c.Units {
		u.Machines *= k
		cp.Units = append(cp.Units, u)
	}
	cp.Count++
	l := r.newLayout("grow-" + spec)
	for y := c.Area.Min.Y; y < c.Area.Max.Y; y++ {
		for x := c.Area.Min.X; x < c.Area.Max.X; x++ {
			delete(l.Reserved, world.Tile{X: x, Y: y})
		}
	}
	l.Usable = func(e string) bool { return r.H.Protos.EntityAvailable(e, s.Av) }
	if err := r.growInterior(l, &cp, s); err != nil {
		c.Room = c.Count // расти некуда (руды/места нет) — больше не пробовать; дальше — новый кластер
		return nil, err
	}
	*c = cp
	return l, nil
}

// stageZero — для разбора нулевого этапа: снабжение стройки уже даёт пластины (в его ящиках
// ≥ 100 железа) — точки нулевого этапа можно снимать; ящики склада storage-N — куда класть.
func (r *Runner) stageZero(s *plan.State) {
	tagOf := map[string]string{}
	for kind, list := range r.Saved.Designs {
		for i, pl := range list {
			tagOf[plan.BuildRole(kind, i)] = pl.Tag
		}
	}
	plates := 0
	for _, o := range s.Owned {
		t := tagOf[o.Role]
		switch {
		case strings.HasPrefix(t, "c/supply-"):
			plates += o.In("chest", "iron-plate")
		case strings.HasPrefix(t, "c/storage-") && r.H.Protos.Entities[o.Name].Type == "container":
			s.StorageRoles = append(s.StorageRoles, o.Role)
		}
	}
	s.StartRemovable = plates >= 100
}
