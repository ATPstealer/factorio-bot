package layout

import (
	"fmt"
	"math"

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

func (l *Layout) fits(lt world.Tile, w, h int, r Rule) bool {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !l.Free(world.Tile{X: lt.X + x, Y: lt.Y + y}, r) {
				return false
			}
		}
	}
	return true
}

// FindArea — ближайший к near свободный прямоугольник w×h (с полем margin вокруг).
func (l *Layout) FindArea(near world.Tile, w, h, margin, radius int, r Rule) (world.Tile, bool) {
	best, bestD := world.Tile{}, math.Inf(1)
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			lt := world.Tile{X: near.X + dx, Y: near.Y + dy}
			d := math.Hypot(float64(dx), float64(dy))
			if d >= bestD {
				continue
			}
			if l.fits(world.Tile{X: lt.X - margin, Y: lt.Y - margin}, w+2*margin, h+2*margin, r) {
				best, bestD = lt, d
			}
		}
	}
	return best, !math.IsInf(bestD, 1)
}

// SmeltingColumn — n печей в колонну (сверху вниз), слева входная лента ↓ (руда + топливо
// на разных полосах), инсертеры → печи → инсертеры, справа выходная лента ↓.
// Ширина 6, высота 2n. Вход — верх левой ленты, выход — низ правой.
func (l *Layout) SmeltingColumn(lt world.Tile, n int, furnace, inserter, tag string) Module {
	fw, fh := Size(l.P, furnace, N)
	m := Module{Name: "плавильня", LT: lt, RB: world.Tile{X: lt.X + 3 + fw, Y: lt.Y + n*fh - 1}}
	inX, insIn, fX, insOut, outX := lt.X, lt.X+1, lt.X+2, lt.X+2+fw, lt.X+3+fw
	for i := 0; i < n; i++ {
		y := lt.Y + i*fh
		l.Add(Placement{Name: furnace, Pos: CenterAt(l.P, furnace, N, world.Tile{X: fX, Y: y}), Tag: tag})
		a := world.Tile{X: insIn, Y: y}
		l.Add(Placement{Name: inserter, Pos: a.Center(), Dir: InserterDir(l.P, inserter, a, world.Tile{X: inX, Y: y}), Tag: tag})
		b := world.Tile{X: insOut, Y: y}
		l.Add(Placement{Name: inserter, Pos: b.Center(), Dir: InserterDir(l.P, inserter, b, world.Tile{X: insOut - 1, Y: y}), Tag: tag})
	}
	for y := lt.Y; y < lt.Y+n*fh; y++ {
		l.Add(Placement{Name: "transport-belt", Pos: world.Tile{X: inX, Y: y}.Center(), Dir: S, Tag: tag})
		l.Add(Placement{Name: "transport-belt", Pos: world.Tile{X: outX, Y: y}.Center(), Dir: S, Tag: tag})
	}
	// низ входной ленты — тупик (печи разбирают); выход — дальше маршрутом
	l.AllowEnd(world.Tile{X: inX, Y: lt.Y + n*fh})
	l.AllowEnd(world.Tile{X: outX, Y: lt.Y + n*fh})
	m.In = []Port{{world.Tile{X: inX, Y: lt.Y}, S}}
	m.Out = []Port{{world.Tile{X: outX, Y: lt.Y + n*fh - 1}, S}}
	m.Electric = l.P.Entities[inserter].Electric
	return m
}

// RedScience — n пар «сборщик шестерён → инсертер → сборщик науки» в ряд (слева направо).
// Сверху лента пластин → (железо и медь по полосам), снизу лента науки →.
// Пара: 3 + 1 + 3 + 1 (проход под столб) = 8 тайлов; высота 7.
func (l *Layout) RedScience(lt world.Tile, n int, asm, inserter, tag string) Module {
	m := Module{Name: "красная наука", LT: lt, RB: world.Tile{X: lt.X + 8*n - 1, Y: lt.Y + 6}, Electric: true}
	topY, insY, asmY, outInsY, outY := lt.Y, lt.Y+1, lt.Y+2, lt.Y+5, lt.Y+6
	for i := 0; i < n; i++ {
		x := lt.X + 8*i
		gear := Placement{Name: asm, Pos: CenterAt(l.P, asm, N, world.Tile{X: x, Y: asmY}), Recipe: "iron-gear-wheel", Tag: tag}
		sci := Placement{Name: asm, Pos: CenterAt(l.P, asm, N, world.Tile{X: x + 4, Y: asmY}), Recipe: "automation-science-pack", Tag: tag}
		l.Add(gear)
		l.Add(sci)
		for _, ins := range []world.Tile{{X: x + 1, Y: insY}, {X: x + 5, Y: insY}} { // лента → сборщики
			l.Add(Placement{Name: inserter, Pos: ins.Center(), Dir: InserterDir(l.P, inserter, ins, world.Tile{X: ins.X, Y: topY}), Tag: tag})
		}
		mid := world.Tile{X: x + 3, Y: asmY + 1} // шестерни → наука
		l.Add(Placement{Name: inserter, Pos: mid.Center(), Dir: InserterDir(l.P, inserter, mid, world.Tile{X: x + 2, Y: mid.Y}), Tag: tag})
		out := world.Tile{X: x + 5, Y: outInsY} // наука → нижняя лента
		l.Add(Placement{Name: inserter, Pos: out.Center(), Dir: InserterDir(l.P, inserter, out, world.Tile{X: out.X, Y: out.Y - 1}), Tag: tag})
	}
	for x := lt.X; x < lt.X+8*n; x++ {
		l.Add(Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: topY}.Center(), Dir: E, Tag: tag})
		l.Add(Placement{Name: "transport-belt", Pos: world.Tile{X: x, Y: outY}.Center(), Dir: E, Tag: tag})
	}
	l.AllowEnd(world.Tile{X: lt.X + 8*n, Y: topY}) // конец ленты пластин — тупик
	l.AllowEnd(world.Tile{X: lt.X + 8*n, Y: outY}) // наука — дальше к лабораториям (стадия 7)
	m.In = []Port{{world.Tile{X: lt.X, Y: topY}, E}}
	m.Out = []Port{{world.Tile{X: lt.X + 8*n - 1, Y: outY}, E}}
	return m
}

// SteamPower — паровая станция у воды: кромка (суша, рядом вода с любой стороны), помпа,
// за ней бойлер и двигатели в линию от воды. Трубные соединения в 2.0 проверит стройка
// (стадия 7); здесь — место и габариты.
func (l *Layout) SteamPower(near world.Tile, engines int, tag string) (Module, error) {
	if l.W == nil {
		return Module{}, fmt.Errorf("layout: станции нужна карта")
	}
	type cand struct {
		t   world.Tile
		d   int // откуда вода
		pls []Placement
	}
	var best *cand
	bestD := math.Inf(1)
	r := 150
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			d0 := math.Hypot(float64(dx), float64(dy))
			if d0 >= bestD {
				continue
			}
			t := world.Tile{X: near.X + dx, Y: near.Y + dy}
			if l.W.TileAt(t) != world.Land {
				continue
			}
			for _, wd := range []int{N, E, S, W} {
				if l.W.TileAt(Step(t, wd)) != world.Water {
					continue
				}
				// линия от воды: помпа на t, затем бойлер и двигатели (повёрнуты к воде)
				away := (wd + 8) % 16
				pls := []Placement{{Name: "offshore-pump", Pos: t.Center(), Dir: wd, Tag: tag}}
				cur := Step(t, away) // первый тайл после помпы
				ok := true
				for i, name := range append([]string{"boiler"}, repeat("steam-engine", engines)...) {
					_ = i
					w, h := Size(l.P, name, wd)
					// прямоугольник w×h, примыкающий к cur со стороны «от воды», по центру линии
					var lt world.Tile
					switch away {
					case S:
						lt = world.Tile{X: cur.X - w/2, Y: cur.Y}
					case N:
						lt = world.Tile{X: cur.X - w/2, Y: cur.Y - h + 1}
					case E:
						lt = world.Tile{X: cur.X, Y: cur.Y - h/2}
					case W:
						lt = world.Tile{X: cur.X - w + 1, Y: cur.Y - h/2}
					}
					p := Placement{Name: name, Pos: CenterAt(l.P, name, wd, lt), Dir: wd, Tag: tag}
					if !l.CanPlace(p, OffOre) {
						ok = false
						break
					}
					pls = append(pls, p)
					// следующий — за этим прямоугольником
					switch away {
					case S:
						cur = world.Tile{X: cur.X, Y: lt.Y + h}
					case N:
						cur = world.Tile{X: cur.X, Y: lt.Y - 1}
					case E:
						cur = world.Tile{X: lt.X + w, Y: cur.Y}
					case W:
						cur = world.Tile{X: lt.X - 1, Y: cur.Y}
					}
				}
				if ok {
					best, bestD = &cand{t, wd, pls}, d0
					break
				}
			}
		}
	}
	if best == nil {
		return Module{}, fmt.Errorf("layout: нет кромки воды под станцию в %d тайлах", r)
	}
	for _, p := range best.pls {
		l.Add(p)
	}
	return Module{Name: "паровая станция", LT: best.t, RB: best.t}, nil
}

func repeat(s string, n int) []string {
	res := make([]string, n)
	for i := range res {
		res[i] = s
	}
	return res
}
