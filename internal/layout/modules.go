package layout

import (
	"fmt"
	"math"
	"sort"

	"factorio-bot/internal/world"
)

// Port — вход/выход модуля: тайл ленты и её направление.
type Port struct {
	T   world.Tile
	Dir int
}

// Module — что получилось: границы и порты.
type Module struct {
	Name     string
	LT, RB   world.Tile // занимаемый прямоугольник
	In       []Port     // куда подводить ленты (первый тайл входной ленты модуля)
	Out      []Port     // откуда лента выходит (последний тайл выходной ленты модуля)
	Electric bool
}

// SteamPower — паровая станция у воды: помпа на кромке (суша, вода рядом с любой стороны),
// бойлер стыкуется водяным входом с помпой, двигатели цепочкой от паровыхода бойлера —
// по трубным соединениям прототипов.
func (l *Layout) SteamPower(near world.Tile, engines int, tag string) (Module, error) {
	pump, err := l.WaterPump(near, tag, func(pump Placement) bool { return l.steamChain(pump, engines) })
	if err != nil {
		return Module{}, err
	}
	t := world.TileOf(pump.Pos)
	return Module{Name: "паровая станция", LT: t, RB: t, Electric: true}, nil
}

// WaterPump — помпа на ближайшей к near кромке воды, для которой fit (ставит остальное
// через Add) вернул true; при false всё, что fit добавил, откатывается.
func (l *Layout) WaterPump(near world.Tile, tag string, fit func(pump Placement) bool) (Placement, error) {
	if l.W == nil {
		return Placement{}, fmt.Errorf("layout: помпе нужна карта")
	}
	type cand struct {
		t world.Tile
		d float64
	}
	var cands []cand
	r := 150
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			t := world.Tile{X: near.X + dx, Y: near.Y + dy}
			if l.W.TileAt(t) == world.Land {
				cands = append(cands, cand{t, math.Hypot(float64(dx), float64(dy))})
			}
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].d < cands[b].d })
	tries := 0
	for _, c := range cands {
		if tries >= maxPumpTries {
			break // ближние кромки не подошли — дальние тем более (каждая — поиск трубы)
		}
		for _, wd := range []int{N, E, S, W} {
			// 2.0: перед помпой (со стороны воды) — полоса воды в 3 тайла
			front := Step(c.t, wd)
			side := (wd + 4) % 16
			if l.W.TileAt(front) != world.Water || l.W.TileAt(Step(front, side)) != world.Water ||
				l.W.TileAt(Step(front, (side+8)%16)) != world.Water {
				continue
			}
			tries++
			mark := l.Mark()
			pump := Placement{Name: "offshore-pump", Pos: c.t.Center(), Dir: wd, Tag: tag}
			if fit(pump) {
				return pump, nil
			}
			l.Rollback(mark)
		}
	}
	return Placement{}, fmt.Errorf("layout: нет кромки воды в %d тайлах", r)
}

// maxPumpTries — сколько ближайших мест на кромке пробовать под помпу.
const maxPumpTries = 40

// steamChain — помпа, бойлер, двигатели; ставит (Add) по очереди, false — не встало.
func (l *Layout) steamChain(pump Placement, engines int) bool {
	pc := Connections(l.P, pump)
	if len(pc) == 0 || !l.CanPlace(pump, OffOre) {
		return false
	}
	l.Add(pump)
	out := pc[0]
	for _, b := range Attach(l.P, out, "boiler", "water", pump.Tag) {
		if !l.CanPlace(b, OffOre) {
			continue
		}
		mark := l.Mark()
		l.Add(b)
		var cur Conn
		for _, cn := range Connections(l.P, b) {
			if cn.Filter == "steam" {
				cur = cn
			}
		}
		ok := true
		for i := 0; i < engines && ok; i++ {
			ok = false
			for _, e := range Attach(l.P, cur, "steam-engine", "steam", pump.Tag) {
				if !l.CanPlace(e, OffOre) {
					continue
				}
				l.Add(e)
				for _, cn := range Connections(l.P, e) {
					if !Joined(cn, cur) {
						cur = cn
						break
					}
				}
				ok = true
				break
			}
		}
		if ok {
			l.boilerFeed(b)
			return true
		}
		l.Rollback(mark)
	}
	return false
}

// boilerFeed — сундук с углём и инсертер в бойлер с тыльной стороны (не трубы): бот
// доливает сундук раз в долгое время, а не бегает к бойлеру. Нет места — без него.
func (l *Layout) boilerFeed(b Placement) {
	var steam Conn
	for _, cn := range Connections(l.P, b) {
		if cn.Filter == "steam" {
			steam = cn
		}
	}
	back := (steam.Dir + 8) % 16
	tiles := Tiles(l.P, b)
	in := map[world.Tile]bool{}
	for _, t := range tiles {
		in[t] = true
	}
	// ближе к центру бойлера — раньше
	c := world.TileOf(b.Pos)
	best := -1
	var bi, bc world.Tile
	for _, t := range tiles {
		it := Step(t, back)
		if in[it] {
			continue
		}
		ct := Step(it, back)
		ins := Placement{Name: "inserter", Pos: it.Center(), Dir: InserterDir(l.P, "inserter", it, ct), Tag: b.Tag}
		ch := Placement{Name: "wooden-chest", Pos: ct.Center(), Tag: b.Tag}
		if !l.CanPlace(ins, OffOre) || !l.CanPlace(ch, OffOre) {
			continue
		}
		if d := abs(t.X-c.X) + abs(t.Y-c.Y); best < 0 || d < best {
			best, bi, bc = d, it, ct
		}
	}
	if best < 0 {
		return
	}
	l.Add(Placement{Name: "wooden-chest", Pos: bc.Center(), Tag: b.Tag})
	l.Add(Placement{Name: "inserter", Pos: bi.Center(), Dir: InserterDir(l.P, "inserter", bi, bc), Tag: b.Tag})
}
