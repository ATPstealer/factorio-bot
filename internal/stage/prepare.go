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
	case 10:
		sites, err := outpostsLayout(h)
		if err != nil {
			return nil, err
		}
		var res []layout.Placement
		for _, s := range sites {
			res = append(res, s.Items...)
		}
		return res, nil
	case 11:
		return powerLayout(h)
	case 12:
		return researchLayout(h)
	case 13:
		return storageLayout()
	case 16:
		return coalLayout(h)
	case 17:
		return smeltingLayout(h)
	case 18:
		return busPlatesLayout(h)
	case 19, 20, 24, 25, 26:
		list, _, err := makeLayout(h, makeProduct[n])
		return list, err
	case 21:
		return labsLayout(h)
	case 23:
		grow, err := growLayout(h)
		if err != nil {
			return nil, err
		}
		list, _, err := makeLayout(h, circuitProduct)
		return append(grow, list...), err
	case 27:
		return labsGreenLayout(h)
	case 22, 28: // обслуживание: что из чертежа ещё не стоит (будущая добыча — стадии 30, не здесь)
		base := planNow
		if n == 28 {
			base = planAll
		}
		list, err := blueprint(h, base)
		if err != nil {
			return nil, err
		}
		return notBuilt(h, list)
	case 29:
		return powerBlueLayout(h)
	case 30:
		return incomeLayout(h)
	case 31:
		return steelLayout(h)
	}
	if s, ok := blueStages[n]; ok { // 32–41 (stages_blue.go)
		return s.layout(h)
	}
	return nil, nil
}

// entityStates — состояние построек list в игре, пачками (по одной — тысячи запросов); нет —
// nil на её месте.
func entityStates(h *act.Hands, list []layout.Placement) ([]*bridge.EntityState, error) {
	res := make([]*bridge.EntityState, 0, len(list))
	for i := 0; i < len(list); i += auditBatch {
		var refs []bridge.EntityRef
		for _, pl := range list[i:min(len(list), i+auditBatch)] {
			refs = append(refs, bridge.EntityRef{Name: pl.Name, Position: pl.Pos})
		}
		got, err := h.B.GetEntities(refs)
		if err != nil {
			return nil, err
		}
		res = append(res, got...)
	}
	return res, nil
}

// notBuilt — из list то, чего в игре ещё нет (пачками, как planAudit): нужное стадии и
// проверка перед стройкой — только по несделанному (решение пользователя 26.10).
func notBuilt(h *act.Hands, list []layout.Placement) ([]layout.Placement, error) {
	var res []layout.Placement
	if paper != nil { // на бумаге: построено — раскладки прошлых стадий (paper.go)
		for _, pl := range list {
			if !paper.has(pl) {
				res = append(res, pl)
			}
		}
		return res, nil
	}
	states, err := entityStates(h, list)
	if err != nil {
		return nil, err
	}
	for i, pl := range list {
		if i >= len(states) || states[i] == nil {
			res = append(res, pl)
		}
	}
	return res, nil
}

// makeProduct — продукт завода с шины по стадии (makeLayout).
var makeProduct = map[int]string{19: "iron-gear-wheel", 20: "automation-science-pack", 24: "inserter", 25: "transport-belt", 26: greenPack}

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
	if len(todo) == 0 {
		return nil
	}
	h.Say("расчистка площади стадии: убрать %d (деревья, камни, обломки)", len(todo))
	s := &worker{h: h}
	if _, err := storageLayout(); err == nil {
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
			if err := s.store(ctx, freeSlots, keepBuilt(p)...); err != nil {
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
		if !slices.Contains(rawItems, it) {
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
	if !PlanOnly { // только планы подряд: следующие стадии сами идут следом
		prepForward(h, n)
	}
	if len(hard) > 0 {
		if len(hard) > 5 {
			hard = append(hard[:5], fmt.Sprintf("… ещё %d", len(hard)-5))
		}
		return errors.New(strings.Join(hard, "; "))
	}
	return nil
}

// prepTiles — постройка на воде или утёсе: засыпки и взрывчатки не будет (решение пользователя
// 26.10), план должен был обойти. Помпа — на воде по замыслу.
func prepTiles(h *act.Hands, list []layout.Placement) []string {
	var res []string
	for _, pl := range list {
		if h.Protos.Entities[pl.Name].Type == "offshore-pump" {
			continue
		}
		for _, t := range layout.Tiles(h.Protos, pl) {
			// только вода и утёсы: свои постройки на месте плана (сундук угля у бойлера до
			// ленты, 1028292478, стадия 16) стадия убирает сама
			where := ""
			switch {
			case h.World.TileAt(t) != world.Land:
				where = "на воде"
			case h.World.CliffAt(t):
				where = "на утёсе"
			}
			if where != "" {
				res = append(res, fmt.Sprintf("%s в (%.1f, %.1f) %s", pl.Name, pl.Pos.X, pl.Pos.Y, where))
				break
			}
		}
	}
	return res
}

// prepReach — до каждого места стройки дойти и дотянуться: заливка суши от персонажа по всей
// открытой карте (вода не проходится, деревья и камни — расчистятся; путь вокруг озера бывает
// вдвое длиннее прямой — рамкой не ограничивать), место — в досягаемости (9) от залитого
// тайла; заливка — пока не найдены все места.
func prepReach(h *act.Hands, list []layout.Placement) []string {
	here, err := hereOrBase(h)
	if err != nil {
		return []string{err.Error()}
	}
	land := func(t world.Tile) bool {
		tt := h.World.TileAt(t)
		return tt == world.Land || tt == world.Shallow
	}
	// тайлы, откуда достаётся место: тайл → места
	const reach = 9
	from := map[world.Tile][]int{}
	for i, pl := range list {
		c := world.TileOf(pl.Pos)
		for dy := -reach; dy <= reach; dy++ {
			for dx := -reach; dx <= reach; dx++ {
				if dx*dx+dy*dy <= reach*reach {
					t := world.Tile{X: c.X + dx, Y: c.Y + dy}
					from[t] = append(from[t], i)
				}
			}
		}
	}
	found := make([]bool, len(list))
	left := len(list)
	seen := map[world.Tile]bool{}
	start := here
	queue := []world.Tile{start}
	seen[start] = true
	for len(queue) > 0 && left > 0 {
		t := queue[0]
		queue = queue[1:]
		for _, i := range from[t] {
			if !found[i] {
				found[i], left = true, left-1
			}
		}
		for _, d := range []world.Tile{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
			q := world.Tile{X: t.X + d.X, Y: t.Y + d.Y}
			if !seen[q] && land(q) {
				seen[q] = true
				queue = append(queue, q)
			}
		}
	}
	var res []string
	for i, pl := range list {
		if !found[i] {
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
	hand := map[string]bool{}
	if ch, ok := h.Protos.Entities["character"]; ok {
		for _, c := range ch.CraftingCategories {
			hand[c] = true
		}
	}
	// можно получить: есть; сырьё (рецепта нет); открыт рецепт не для рук (печь, сборщик) или
	// для рук — и всё, из чего он, тоже можно получить (закрытый ингредиент — крафт встанет
	// посреди стадии: inserter без electronics)
	memo := map[string]bool{}
	var can func(item string, depth int) bool
	can = func(item string, depth int) bool {
		if v, ok := memo[item]; ok {
			return v
		}
		if stock[item] > 0 || depth > 8 {
			return true
		}
		rs := h.Protos.RecipesFor(item)
		ok := len(rs) == 0
		for _, name := range rs {
			r := h.Protos.Recipes[name]
			if ok || !slices.Contains(f.Recipes, name) {
				continue
			}
			ok = !hand[r.Category]
			if !ok {
				ok = true
				for _, in := range r.Ingredients {
					ok = ok && can(in.Name, depth+1)
				}
			}
		}
		memo[item] = ok
		return ok
	}
	var res []string
	seen := map[string]bool{}
	for _, pl := range list {
		item := h.Protos.ItemFor(pl.Name)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		if !can(item, 0) {
			res = append(res, fmt.Sprintf("%s нет, и не скрафтить: рецепт (или рецепт ингредиента) не открыт", item))
		}
	}
	return res
}

// prepStock — что есть: инвентарь и хранение (если построено).
func prepStock(h *act.Hands) map[string]int {
	if s, err := materialsStock(h); err == nil {
		return s
	}
	if p, err := h.B.Player(); err == nil {
		return p.Inventory
	}
	return map[string]int{}
}

// missing — постройки плана, которых ещё нет.
func missing(h *act.Hands, list []layout.Placement) []layout.Placement {
	// пачками (notBuilt): по одной — 15 тыс. запросов на стадию 30, подготовка шла 10–20 мин
	res, err := notBuilt(h, list)
	if err != nil {
		return nil
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
	estimate.Store(int64(est)) // лимит стадии — с запасом от оценки (Do), сразу
	if lim := est * 3 / 2; Limit > 0 && lim > Limit {
		h.Say("лимит стадии — %v (полторы оценки %v)", lim.Round(time.Minute), est.Round(time.Minute))
	}
}

// forward — на сколько стадий вперёд прикидывать планы перед стадией n: обычно на одну, перед
// 11, 21, 31, … — на 10 (дальние — долго, а сети и построек, на которые они опираются, ещё нет).
func forward(n int) int {
	if n >= 11 && n%10 == 1 {
		return 10
	}
	return 1
}

// prepForward — планы следующих стадий (forward) «на бумаге» (по нынешней карте и плану стадии 3):
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
		if m <= n || m > n+forward(n) {
			continue
		}
		if _, err := planOf(h, m); err != nil {
			h.Say("проверка: план стадии %d не строится: %v", m, err)
		}
	}
}
