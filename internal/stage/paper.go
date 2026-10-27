package stage

import (
	"fmt"
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
	}
	return res
}

// remove — разобрать (точки стадии 10 — на стадии 15).
func (p *paperBuilt) remove(h *act.Hands, list []layout.Placement) {
	gone := map[layout.Placement]bool{}
	for _, pl := range list {
		gone[pl] = true
	}
	var keep []layout.Placement
	for _, pl := range p.list {
		if !gone[pl] {
			keep = append(keep, pl)
		}
	}
	p.list, p.at = nil, map[world.Tile]int{}
	p.add(h, keep)
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
