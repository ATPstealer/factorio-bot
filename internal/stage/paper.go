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

// paperStages — стадии стройки 10…to на бумаге; первая с ошибкой — ошибка.
func paperStages(h *act.Hands, to int) error {
	paper = &paperBuilt{at: map[world.Tile]int{}}
	defer func() { paper = nil }()
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
		switch n {
		case 15: // разбор точек
			paper.remove(h, outposts)
		case 16: // сундук и электроинсертер стадии 11 у бойлера — убирает (runCoal)
			if station, err := powerLayout(h); err == nil {
				paper.remove(h, []layout.Placement{findPlaced(station, chest), findPlaced(station, "inserter")})
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
		if (n == 22 || n == 28) && len(list) > 0 && !freshLayouts {
			bad = append(bad, unbuilt(list))
		}
		bad = append(bad, paper.add(h, list)...)
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
	return nil
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
	stage, need, lc, c := curStage, needCache, layout.Chest, chest
	freshLayouts = true
	defer func() {
		curStage, needCache, layout.Chest, chest = stage, need, lc, c
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
