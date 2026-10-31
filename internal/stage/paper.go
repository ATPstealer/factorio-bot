package stage

import (
	"fmt"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадии стройки на бумаге (preflight; правила скорости 27.10): после планов, без игры, —
// раскладка каждой стадии 10–ДО по очереди и её проверка (вода и утёсы под постройками,
// достижимость от центра базы, наложения на построенное прошлыми стадиями). Ошибка раскладки
// стадии (нет места под буфер, столбы не дотянуть…) — видна сразу, а не через часы игры.
// «Построенное» — раскладки прошлых стадий (paper), вместо игры: столбы сети (connectPower),
// что ещё не стоит (notBuilt).

// paper — построенное на бумаге (nil — игра).
var paper *paperBuilt

type paperBuilt struct {
	list []layout.Placement
	at   map[world.Tile]int // тайл → индекс в list
}

func (p *paperBuilt) has(pl layout.Placement) bool {
	i, ok := p.at[world.TileOf(pl.Pos)]
	return ok && p.list[i].Name == pl.Name && p.list[i].Pos == pl.Pos
}

// replaces — new ставится поверх old штатно: делитель и подземка заменяют ленту (в игре — замена
// на месте, PlaceDirs).
func replaces(newName, oldName string) bool {
	belt := strings.HasSuffix(oldName, "transport-belt")
	return belt && (strings.HasSuffix(newName, "splitter") || strings.HasSuffix(newName, "underground-belt"))
}

// add — построить list; занятый другим тайл — наложение (в ответ).
func (p *paperBuilt) add(h *act.Hands, list []layout.Placement) []string {
	var res []string
	for _, pl := range list {
		if p.has(pl) {
			continue
		}
		var gone []layout.Placement // ленты под делителем/подземкой — заменяются
		for _, t := range layout.Tiles(h.Protos, pl) {
			if i, ok := p.at[t]; ok && replaces(pl.Name, p.list[i].Name) {
				gone = append(gone, p.list[i])
			}
		}
		if len(gone) > 0 {
			p.remove(h, gone)
		}
		for _, t := range layout.Tiles(h.Protos, pl) {
			if i, ok := p.at[t]; ok && (p.list[i].Name != pl.Name || p.list[i].Pos != pl.Pos) {
				res = append(res, fmt.Sprintf("%s в (%.1f, %.1f) [%s] на месте %s [%s]", pl.Name, pl.Pos.X, pl.Pos.Y, pl.Tag, p.list[i].Name, p.list[i].Tag))
				break
			}
		}
		p.list = append(p.list, pl)
		for _, t := range layout.Tiles(h.Protos, pl) {
			p.at[t] = len(p.list) - 1
		}
		h.World.Put([]world.Object{paperObject(h, pl)}) // занимает тайлы, как в игре
	}
	return res
}

// paperObject — постройка на бумаге как свой объект модели мира.
func paperObject(h *act.Hands, pl layout.Placement) world.Object {
	return world.Object{Name: pl.Name, Type: h.Protos.Entities[pl.Name].Type, Pos: pl.Pos, Direction: pl.Dir, Own: true}
}

// remove — разобрать (точки стадии 10 — на стадии 15).
func (p *paperBuilt) remove(h *act.Hands, list []layout.Placement) {
	gone := map[layout.Placement]bool{}
	for _, pl := range list {
		gone[pl] = true
	}
	var keep []layout.Placement
	var objs []world.Object
	for _, pl := range p.list {
		if !gone[pl] {
			keep = append(keep, pl)
		} else {
			objs = append(objs, paperObject(h, pl))
		}
	}
	h.World.Hide(objs) // разобрано — тайлы свободны
	p.list, p.at = keep, map[world.Tile]int{}
	for i, pl := range keep {
		for _, t := range layout.Tiles(h.Protos, pl) {
			p.at[t] = i
		}
	}
}

// flowSeen — нарушения течения на бумаге у прошлой стадии (в журнал — только новые).
var flowSeen map[string]bool

// isMaintenance — стадия обслуживания (чертёж: что ещё не стоит).
func isMaintenance(n int) bool { return n == 22 || n == 28 || n == 42 }

// paperStages — стадии стройки 10…to на бумаге; первая с ошибкой — ошибка.
func paperStages(h *act.Hands, to int) error {
	paper = &paperBuilt{at: map[world.Tile]int{}}
	flowSeen = nil
	t2 := tier2 // по стадиям на бумаге, после — как было у текущей
	defer func() { paper, tier2 = nil, t2 }()
	var outposts []layout.Placement
	for _, n := range List() {
		if n < 10 || n > to {
			continue
		}
		s, _ := Get(n)
		if s.Plan || s.Empty {
			continue
		}
		curStage, needCache.items = n, nil
		if n >= 22 {
			layout.Chest, chest = ironChest, ironChest
		}
		tier2 = n >= tier2From
		switch n {
		case 15: // разбор точек
			paper.remove(h, outposts)
		case 16: // сундук и электроинсертер стадии 11 у бойлера — убирает (runCoal)
			if station, err := powerLayout(h); err == nil {
				paper.remove(h, []layout.Placement{findPlaced(station, chest), findPlaced(station, "inserter")})
			}
		default: // стадии проектов: буферы выхода прошлых стадий не на новом месте — снимают
			if s, ok := projectStage(n); ok {
				if stale, err := s.staleBuffers(h); err == nil {
					for _, list := range stale {
						paper.remove(h, list)
					}
				}
			}
		}
		list, err := planOf(h, n)
		if err != nil {
			return fmt.Errorf("стадия %d на бумаге: раскладка: %w", n, err)
		}
		if n == 10 {
			outposts = list
		}
		var bad []string
		bad = append(bad, prepTiles(h, list)...)
		bad = append(bad, prepReach(h, list)...)
		// обслуживание: всё из чертежа уже поставили стадии (при обновлении чертежа в игре — не
		// ошибка: недостающее обслуживание и достроит)
		if isMaintenance(n) && len(list) > 0 && !freshLayouts {
			bad = append(bad, unbuilt(list))
		}
		// обслуживание при обновлении чертежа — на бумагу не кладёт: его список — из состояния,
		// а там ещё старые раскладки следующих стадий (их столбы — потом лишние: новые столбы
		// стадий 23–25 «держались» за них, после разбора — острова без тока, 1028292478)
		if !freshLayouts || !isMaintenance(n) {
			bad = append(bad, paper.add(h, list)...)
		}
		// проверки чертежа стадии (решение пользователя 28.10): лента до своего буфера в конце —
		// жёстко; течение — пока в журнал (ложные срабатывания не выяснены)
		if !freshLayouts {
			bad = append(bad, laneToBuffer(h, paper.list)...)
			l := layout.New(h.Protos, h.World) // руда под бурами — с карты
			for _, pl := range paper.list {
				l.Add(pl)
			}
			now := map[string]bool{}
			for _, e := range l.Flow(nil) {
				now[e] = true
				if !flowSeen[e] { // только новое против прошлой стадии
					h.Log("стадия %d на бумаге, течение: %s", n, e)
				}
			}
			flowSeen = now
		}
		h.Log("стадия %d на бумаге: %d построек, нарушений %d", n, len(list), len(bad))
		if len(bad) > 0 {
			for _, b := range bad {
				h.Log("  %s", b)
			}
			if len(bad) > 5 {
				bad = append(bad[:5], fmt.Sprintf("… ещё %d", len(bad)-5))
			}
			return fmt.Errorf("стадия %d на бумаге: %s", n, strings.Join(bad, "; "))
		}
	}
	// весь план — в раскладках стадий (решение пользователя 28.10: трубы месторождений, НПЗ и
	// сквозные ленты не входили ни в одну стадию — синей не было на всех картах): до 41 — план
	// синей, до 48 — весь (с фиолетовой)
	if (to == 41 || to >= lastPurpleStage) && !freshLayouts {
		if miss := planCoverage(h, paper.list, to >= lastPurpleStage); miss != "" {
			return fmt.Errorf("на бумаге: %s", miss)
		}
	}
	return nil
}

// planCoverage — постройки полного плана (whole — с фиолетовой, иначе до синей), которых нет в
// built (раскладки стадий): по тегам; "" — все есть.
func planCoverage(h *act.Hands, built []layout.Placement, whole bool) string {
	plan, err := LoadBluePlan()
	if whole {
		plan, err = LoadPurplePlan()
	}
	if err != nil {
		if plan, err = LoadPlan(); err != nil {
			return ""
		}
	}
	type key struct {
		name string
		x, y int
	}
	have := map[key]bool{}
	for _, pl := range built {
		have[key{pl.Name, int(pl.Pos.X * 2), int(pl.Pos.Y * 2)}] = true
	}
	var miss []layout.Placement
	for _, pl := range plan.Layout {
		if !have[key{pl.Name, int(pl.Pos.X * 2), int(pl.Pos.Y * 2)}] {
			miss = append(miss, pl)
		}
	}
	if len(miss) == 0 {
		return ""
	}
	return "плана нет ни в одной раскладке стадий стройки: " + unbuilt(miss)
}

// laneToBuffer — лента предмета на шине доходит до своего буфера в конце: инсертер буфера выхода
// берёт с тайла, куда течёт лента от её головы (разрыв ленты стали под делителем будущего
// отвода — буфер стали пуст, стадии 31–33 на 859088572).
func laneToBuffer(h *act.Hands, list []layout.Placement) []string {
	at, pair := beltTiles(list)
	var res []string
	for _, pl := range list {
		if !strings.HasPrefix(pl.Tag, "outbuf/") || !strings.HasSuffix(pl.Name, "inserter") || pl.Recipe == "" {
			continue
		}
		ins := world.TileOf(pl.Pos)
		from := layout.Step(ins, pl.Dir) // инсертер берёт с тайла, куда смотрит
		if q, ok := at[from]; !ok || !isBelt(q) {
			from = layout.Step(ins, (pl.Dir+8)%16)
		}
		if q, ok := at[from]; !ok || !isBelt(q) || !strings.HasPrefix(q.Tag, "c/bus-") {
			continue // берёт не с ленты шины (плавка, кирпич — с выходной ленты кластера)
		}
		// головы лент предмета: ленты шины с ним, в которые сзади никто не въезжает
		reached := false
		for t, q := range at {
			if reached || !strings.HasSuffix(q.Name, "transport-belt") || q.Recipe != pl.Recipe || !strings.HasPrefix(q.Tag, "c/bus-") {
				continue
			}
			if b, ok := at[layout.Step(t, (q.Dir+8)%16)]; ok && isBelt(b) && b.Dir == q.Dir {
				continue // не голова
			}
			reached = downstream(at, pair, t)[from]
		}
		if !reached {
			res = append(res, fmt.Sprintf("лента %s не доходит до буфера в конце (инсертер в (%.1f, %.1f))", pl.Recipe, pl.Pos.X, pl.Pos.Y))
		}
	}
	return res
}

// unbuilt — к обслуживанию из чертежа не поставлено стадиями list: по тегам плана (второй кусок
// кластера плавки не строился ни одной стадией — 957 построек на обслуживании, 260647970).
func unbuilt(list []layout.Placement) string {
	byTag := map[string]int{}
	var tags []string
	for _, pl := range list {
		if byTag[pl.Tag] == 0 {
			tags = append(tags, pl.Tag)
		}
		byTag[pl.Tag]++
	}
	sort.Slice(tags, func(i, j int) bool { return byTag[tags[i]] > byTag[tags[j]] })
	var parts []string
	for _, t := range tags {
		parts = append(parts, fmt.Sprintf("%s ×%d", t, byTag[t]))
	}
	return fmt.Sprintf("чертежа не поставила ни одна стадия: %d (%s)", len(list), strings.Join(parts, ", "))
}

// refreshLayouts — обновить чертёж (stages.md, стадия 22: код поменялся, а карта построена по
// старому): раскладки стадий стройки 10…to — заново текущим кодом, на бумаге по очереди; своё
// построенное в расчёт не берётся; файлы раскладок перезаписываются. Модель мира и общее
// состояние стадии после — как были.
func refreshLayouts(h *act.Hands, to int) error {
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	restore := h.World.Keep()
	defer restore()
	var own []world.Object
	for _, o := range h.World.Objects() {
		// обломки корабля — сила игрока, но не постройки бота: остаются (корабль крушения
		// прятался — столб по чертежу встал на него, стадия 28 на 1028292478)
		if o.Own && o.Type != "character" && !strings.HasPrefix(o.Name, "crash-site-") {
			own = append(own, o)
		}
	}
	h.World.Hide(own)
	stage, need, lc, c, t2 := curStage, needCache, layout.Chest, chest, tier2
	freshLayouts = true
	defer func() {
		curStage, needCache, layout.Chest, chest, tier2 = stage, need, lc, c, t2
		freshLayouts = false
	}()
	h.Say("обновляю чертёж: раскладки стадий 10–%d заново текущим кодом", to)
	return paperStages(h, to)
}

// hereOrBase — где персонаж; на бумаге (без игры) — центр базы (голова bus-1).
func hereOrBase(h *act.Hands) (world.Tile, error) {
	if h.B == nil {
		if c, ok := baseCenter(); ok {
			return c, nil
		}
		return world.Tile{}, fmt.Errorf("на бумаге: нет разметки базы")
	}
	p, err := h.B.Player()
	if err != nil {
		return world.Tile{}, err
	}
	return world.TileOf(p.Position), nil
}
