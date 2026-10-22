package stage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Общее для всех стадий (stages.md): перед стройкой — проверка, что стадия может пройти, и
// расчистка всей площади плана; строить — когда всё точно свободно.

// planOf — план постройки стадии n (что и где ставить); nil — стадия ничего не строит.
func planOf(h *act.Hands, n int) ([]layout.Placement, error) {
	switch n {
	case 2:
		sites, err := s2Layout(h)
		if err != nil {
			return nil, err
		}
		var res []layout.Placement
		for _, s := range sites {
			res = append(res, s.Items...)
		}
		return res, nil
	case 6:
		return s6Layout(h)
	case 7:
		return s7Layout(h)
	case 8:
		return s8Layout()
	case 11:
		return s11Layout(h)
	case 12:
		return s12Layout(h)
	case 13:
		return s13Layout(h)
	case 14:
		list, _, err := makeLayout(h, "iron-gear-wheel", 14)
		return list, err
	case 15:
		list, _, err := makeLayout(h, "automation-science-pack", 15)
		return list, err
	case 16:
		return s16Layout(h)
	}
	return nil, nil
}

// clearable — что расчищать (stages.md): легко устранимое — всегда; вода и скалы — когда есть
// чем (засыпка, динамит в инвентаре или хранении; кластер, который их делает, — потом).
type clearable struct {
	Water, Cliffs bool
}

func clearOptions(h *act.Hands) clearable {
	stock := map[string]int{}
	if s, err := s9Stock(h); err == nil {
		stock = s
	} else if p, err := h.B.Player(); err == nil {
		stock = p.Inventory
	}
	return clearable{Water: stock["landfill"] > 0, Cliffs: stock["cliff-explosives"] > 0}
}

// prepare — перед стадией n: проверка (prepCheck) и расчистка площади плана (prepClear).
func prepare(ctx context.Context, h *act.Hands, n int) error {
	list, err := planOf(h, n)
	if err != nil {
		return fmt.Errorf("план стадии: %w", err)
	}
	if err := prepCheck(ctx, h, n, list); err != nil {
		return fmt.Errorf("проверка перед стройкой: %w", err)
	}
	if len(list) == 0 {
		return nil
	}
	return prepClear(ctx, h, list)
}

// footprint — тайлы под постройками плана.
func footprint(h *act.Hands, list []layout.Placement) map[world.Tile]bool {
	res := map[world.Tile]bool{}
	for _, pl := range list {
		for _, t := range layout.Tiles(h.Protos, pl) {
			res[t] = true
		}
	}
	return res
}

// obstacles — что убрать с площади fp: деревья, камни, обломки корабля (не свои), чья рамка
// коллизии задевает площадь.
func obstacles(h *act.Hands, fp map[world.Tile]bool) []world.Object {
	var res []world.Object
	for _, o := range h.World.Objects() {
		rock := o.Type == "simple-entity" && strings.Contains(o.Name, "rock")
		wreck := strings.HasPrefix(o.Name, "crash-site-")
		if o.Own || o.Type != "tree" && !rock && !wreck {
			continue
		}
		b := h.World.CollisionBox(o)
		if o.Type == "tree" {
			// крона над постройкой мешает кликать по ней (окно инсертера не открывалось —
			// курсор выделял дерево): у деревьев — рамка выделения
			b = h.World.SelectionBox(o)
		}
		hit := false
		for y := int(math.Floor(b.LeftTop.Y)); y < int(math.Ceil(b.RightBottom.Y)) && !hit; y++ {
			for x := int(math.Floor(b.LeftTop.X)); x < int(math.Ceil(b.RightBottom.X)) && !hit; x++ {
				hit = fp[world.Tile{X: x, Y: y}]
			}
		}
		if hit {
			res = append(res, o)
		}
	}
	return res
}

// prepClear — расчистить площадь плана: убрать всё устранимое (ближайшее к персонажу — сначала,
// чтобы не бегать туда-сюда), место в инвентаре — через хранение; в конце всё свободно.
func prepClear(ctx context.Context, h *act.Hands, list []layout.Placement) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	fp := footprint(h, list)
	todo := obstacles(h, fp)
	opt := clearOptions(h)
	if opt.Water || opt.Cliffs {
		h.Log("расчистка: засыпку воды и подрыв скал бот пока не умеет — обходит их планом")
	}
	if len(todo) == 0 {
		return nil
	}
	h.Say("расчистка площади стадии: убрать %d (деревья, камни, обломки)", len(todo))
	s := &s2{h: h}
	if _, err := s8Layout(); err == nil {
		s.useStore = true
	}
	skipped := 0
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := h.B.Player()
		if err != nil {
			return err
		}
		sort.Slice(todo, func(i, j int) bool { return dist2(todo[i].Pos, p.Position) < dist2(todo[j].Pos, p.Position) })
		o := todo[0]
		todo = todo[1:]
		e, err := h.B.EntityAt(o.Name, o.Pos)
		if err != nil {
			return err
		}
		if e == nil {
			continue // уже убрано (срублено по дороге)
		}
		if s.useStore {
			if err := s.store(ctx, s8Free, keepBuilt(p)...); err != nil {
				return err
			}
		}
		// подойти (время рубки — только на рубку), потом рубить; не вышло — стройка потом
		// уберёт сама (clearSpots)
		if err := h.WalkToMine(ctx, *e); err != nil && ctx.Err() != nil {
			return err
		}
		if err := h.Mine(ctx, *e, 20*time.Second); err != nil {
			if ctx.Err() != nil {
				return err
			}
			skipped++
			h.Log("расчистка: %s (%.1f, %.1f) пропущено: %v", o.Name, o.Pos.X, o.Pos.Y, err)
		}
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	if left := obstacles(h, fp); len(left) > 0 {
		h.Log("расчистка: осталось %d (не подойти) — уберёт стройка", len(left))
	}
	return nil
}

// keepBuilt — что не класть в хранение при расчистке: всё, кроме сырья с вырубки.
func keepBuilt(p bridge.Player) []string {
	var keep []string
	for it := range p.Inventory {
		if !slices.Contains(s8Raw, it) {
			keep = append(keep, it)
		}
	}
	return keep
}

func dist2(a, b bridge.Pos) float64 { return (a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y) }

// prepCheck — может ли стадия пройти (stages.md), ничего не тратя. Жёстко (стадия падает
// сразу): постройки на воде/скалах без средства, недостижимые места, неоткрытые рецепты.
// Мягко (сказать и продолжить): материалы, время, планы следующих стадий.
func prepCheck(ctx context.Context, h *act.Hands, n int, list []layout.Placement) error {
	var hard []string
	if len(list) > 0 {
		if _, err := h.World.Sync(h.B, false); err != nil {
			return err
		}
		hard = append(hard, prepTiles(h, list)...)
		hard = append(hard, prepReach(h, list)...)
		hard = append(hard, prepRecipes(h, list)...)
		prepMaterials(ctx, h, list)
		prepTime(ctx, h, list)
	}
	prepForward(h, n)
	if len(hard) > 0 {
		if len(hard) > 5 {
			hard = append(hard[:5], fmt.Sprintf("… ещё %d", len(hard)-5))
		}
		return errors.New(strings.Join(hard, "; "))
	}
	return nil
}

// prepTiles — постройка на воде или скале, когда засыпать/взорвать нечем (план должен был
// обойти). Помпа — на воде по замыслу.
func prepTiles(h *act.Hands, list []layout.Placement) []string {
	opt := clearOptions(h)
	var res []string
	for _, pl := range list {
		if h.Protos.Entities[pl.Name].Type == "offshore-pump" {
			continue
		}
		for _, t := range layout.Tiles(h.Protos, pl) {
			if h.World.TileAt(t) == world.Water && !opt.Water {
				res = append(res, fmt.Sprintf("%s в (%.1f, %.1f) на воде", pl.Name, pl.Pos.X, pl.Pos.Y))
				break
			}
		}
	}
	return res
}

// prepReach — до каждого места стройки дойти и дотянуться: заливка суши от персонажа (вода не
// проходится, деревья и камни — расчистятся), место — в досягаемости (9) от залитого тайла.
func prepReach(h *act.Hands, list []layout.Placement) []string {
	p, err := h.B.Player()
	if err != nil {
		return []string{err.Error()}
	}
	lo, hi := world.TileOf(p.Position), world.TileOf(p.Position)
	for _, pl := range list {
		t := world.TileOf(pl.Pos)
		lo = world.Tile{X: min(lo.X, t.X), Y: min(lo.Y, t.Y)}
		hi = world.Tile{X: max(hi.X, t.X), Y: max(hi.Y, t.Y)}
	}
	const pad = 64
	lo, hi = world.Tile{X: lo.X - pad, Y: lo.Y - pad}, world.Tile{X: hi.X + pad, Y: hi.Y + pad}
	land := func(t world.Tile) bool {
		if t.X < lo.X || t.Y < lo.Y || t.X > hi.X || t.Y > hi.Y {
			return false
		}
		tt := h.World.TileAt(t)
		return tt == world.Land || tt == world.Shallow
	}
	seen := map[world.Tile]bool{}
	start := world.TileOf(p.Position)
	queue := []world.Tile{start}
	seen[start] = true
	for len(queue) > 0 {
		t := queue[0]
		queue = queue[1:]
		for _, d := range []world.Tile{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
			q := world.Tile{X: t.X + d.X, Y: t.Y + d.Y}
			if !seen[q] && land(q) {
				seen[q] = true
				queue = append(queue, q)
			}
		}
	}
	var res []string
	const reach = 9
	for _, pl := range list {
		c := world.TileOf(pl.Pos)
		ok := false
		for dy := -reach; dy <= reach && !ok; dy++ {
			for dx := -reach; dx <= reach && !ok; dx++ {
				ok = dx*dx+dy*dy <= reach*reach && seen[world.Tile{X: c.X + dx, Y: c.Y + dy}]
			}
		}
		if !ok {
			res = append(res, fmt.Sprintf("до %s в (%.1f, %.1f) не дойти (вода)", pl.Name, pl.Pos.X, pl.Pos.Y))
		}
	}
	return res
}

// prepRecipes — что ставить, есть или можно скрафтить (рецепт открыт).
func prepRecipes(h *act.Hands, list []layout.Placement) []string {
	f, err := h.B.Force()
	if err != nil {
		return []string{err.Error()}
	}
	stock := prepStock(h)
	var res []string
	seen := map[string]bool{}
	for _, pl := range list {
		item := h.Protos.ItemFor(pl.Name)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		if stock[item] > 0 {
			continue
		}
		ok := false
		for _, r := range h.Protos.RecipesFor(item) {
			ok = ok || slices.Contains(f.Recipes, r)
		}
		if !ok {
			res = append(res, fmt.Sprintf("%s нет и рецепт не открыт", item))
		}
	}
	return res
}

// prepStock — что есть: инвентарь и хранение (если построено).
func prepStock(h *act.Hands) map[string]int {
	if s, err := s9Stock(h); err == nil {
		return s
	}
	if p, err := h.B.Player(); err == nil {
		return p.Inventory
	}
	return map[string]int{}
}

// missing — постройки плана, которых ещё нет.
func missing(h *act.Hands, list []layout.Placement) []layout.Placement {
	var res []layout.Placement
	for _, pl := range list {
		if e, err := h.B.EntityAt(pl.Name, pl.Pos); err == nil && e == nil {
			res = append(res, pl)
		}
	}
	return res
}

// prepMaterials — сколько чего поставить против того, что есть (инвентарь, хранение, буферы
// выхода); недостающее — ручной крафт: сколько на него сырья (пластины, камень, дерево) и
// времени, и хватит ли сырья с тем, что дадут свои печи за оставшийся лимит. Не хватит —
// сказать (мягко: руду и камень бот добывает и сам).
func prepMaterials(ctx context.Context, h *act.Hands, list []layout.Placement) {
	need := map[string]int{}
	for _, pl := range missing(h, list) {
		if it := h.Protos.ItemFor(pl.Name); it != "" {
			need[it]++
		}
	}
	stock := prepStock(h)
	for it, n := range prepBuffers(h) {
		stock[it] += n
	}
	f, err := h.B.Force()
	if err != nil {
		return
	}
	av := h.Protos.NewAvailability(f.Researched, false)
	base := map[string]float64{}
	secs := 0.0
	var lack []string
	for it, n := range need {
		d := n - stock[it]
		if d <= 0 {
			continue
		}
		lack = append(lack, fmt.Sprintf("%s ×%d", it, d))
		hc := calc.Handcraft(h.Protos, av, it, float64(d))
		for b, v := range hc.Base {
			base[b] += v
		}
		secs += hc.Seconds
	}
	if len(lack) == 0 {
		return
	}
	sort.Strings(lack)
	h.Say("проверка: скрафтить по ходу стадии: %s (крафт ~%.0f мин)", strings.Join(lack, ", "), secs/60)
	left := 60 * time.Minute
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	rate := prepSmelting(h)
	var short []string
	for b, v := range base {
		have := float64(stock[b]) + rate[b]*left.Seconds()
		if v > have {
			short = append(short, fmt.Sprintf("%s нужно %.0f, есть %d + печи ~%.0f", b, v, stock[b], rate[b]*left.Seconds()))
		}
	}
	sort.Strings(short)
	if len(short) > 0 {
		h.Say("проверка: сырья может не хватить: %s", strings.Join(short, "; "))
	}
	if secs > left.Seconds() {
		h.Say("проверка: ручной крафт (~%.0f мин) дольше лимита (%v)", secs/60, left.Round(time.Minute))
	}
}

// prepBuffers — что лежит в буферах выхода (сундуки с тегом outbuf/ в плане кластеров).
func prepBuffers(h *act.Hands) map[string]int {
	res := map[string]int{}
	plan, err := LoadPlan()
	if err != nil {
		return res
	}
	for _, pl := range plan.Layout {
		if pl.Name != chest || !strings.HasPrefix(pl.Tag, "outbuf/") {
			continue
		}
		if st, err := h.B.GetEntity(pl.Name, pl.Pos); err == nil && st != nil {
			for it, n := range st.Inventories["chest"] {
				res[it] += n
			}
		}
	}
	return res
}

// prepSmelting — сколько пластин в секунду дают свои печи (по последнему рецепту каждой).
func prepSmelting(h *act.Hands) map[string]float64 {
	res := map[string]float64{}
	for _, o := range h.World.Objects() {
		if !o.Own || h.Protos.Entities[o.Name].Type != "furnace" {
			continue
		}
		st, err := h.B.GetEntity(o.Name, o.Pos)
		if err != nil || st == nil || st.Recipe == "" {
			continue
		}
		r := h.Protos.Recipes[st.Recipe]
		speed := h.Protos.Entities[o.Name].CraftingSpeed
		if r.Energy <= 0 || speed <= 0 {
			continue
		}
		for _, p := range r.Products {
			res[p.Name] += p.Amount * speed / r.Energy
		}
	}
	return res
}

// prepTime — оценка времени стройки (постановка ~2 с, ходьба 8.9 тайла/с между местами вне
// досягаемости, расчистка ~2.5 с на дерево) против оставшегося лимита: не уложится — сказать.
func prepTime(ctx context.Context, h *act.Hands, list []layout.Placement) {
	todo := missing(h, list)
	p, err := h.B.Player()
	if err != nil {
		return
	}
	at := p.Position
	walk := 0.0
	for _, pl := range todo {
		if d := math.Sqrt(dist2(at, pl.Pos)); d > 9 {
			walk += d
			at = pl.Pos
		}
	}
	trees := len(obstacles(h, footprint(h, list)))
	est := time.Duration((float64(len(todo))*2+walk/8.9+float64(trees)*2.5)*1e9) * time.Nanosecond
	h.Log("проверка: поставить %d, расчистить %d, пройти ~%.0f тайлов — оценка %v", len(todo), trees, walk, est.Round(time.Minute))
	if dl, ok := ctx.Deadline(); ok && est > time.Until(dl) {
		h.Say("проверка: по оценке стройка (%v) дольше лимита (%v) — может не уложиться", est.Round(time.Minute), time.Until(dl).Round(time.Minute))
	}
}

// prepForward — планы следующих стадий «на бумаге» (по нынешней карте и плану стадии 5):
// ошибка плана дальше — видна сразу. Файлы состояния, созданные при этом, удаляются (стадия
// построит свой план, когда до неё дойдёт).
func prepForward(h *act.Hands, n int) {
	before := map[string]bool{}
	files, _ := filepath.Glob(runPath("state*.json"))
	for _, f := range files {
		before[f] = true
	}
	defer func() {
		files, _ := filepath.Glob(runPath("state*.json"))
		for _, f := range files {
			if !before[f] {
				os.Remove(f)
			}
		}
	}()
	if _, err := LoadPlan(); err != nil {
		return // плана кластеров ещё нет (стадии до 5) — дальше не заглянуть
	}
	for _, m := range List() {
		if m <= n {
			continue
		}
		if _, err := planOf(h, m); err != nil {
			h.Say("проверка: план стадии %d не строится: %v", m, err)
		}
	}
}
