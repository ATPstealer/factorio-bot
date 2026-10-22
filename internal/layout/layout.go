package layout

import (
	"fmt"
	"sort"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

// Layout — раскладка на карте: размещения, занятые тайлы, что убрать перед стройкой.
type Layout struct {
	P     *knowledge.Prototypes
	W     *world.World // nil — пустая суша (для тестов модулей)
	List  []Placement
	occ   map[world.Tile]int // тайл → индекс в List
	Clear map[world.Tile]bool
	// Reserved — тайлы, где строить нельзя (запас под будущее, проходы)
	Reserved map[world.Tile]bool
	ends     map[world.Tile]bool
	// Usable — доступна ли постройка (изучено); nil — всё. Без подземок лента только поверху.
	Usable func(entity string) bool
	// Anchors — уже стоящие в игре свои столбы: узлы сети, к которым тянуть столбы
	// раскладки (не строятся)
	Anchors []Placement
	// PoleTag — Poles только для размещений с этим тегом (внутренность кластера): покрыть его
	// машины и связать его столбы между собой, без чужих сетей и Anchors
	PoleTag string
}

// poleList — столбы раскладки и уже стоящие (Anchors).
func (l *Layout) poleList() []Placement {
	var poles []Placement
	if l.PoleTag == "" {
		poles = append(poles, l.Anchors...)
	}
	for _, p := range l.List {
		if isPole(p.Name) && (l.PoleTag == "" || p.Tag == l.PoleTag) {
			poles = append(poles, p)
		}
	}
	return poles
}

func (l *Layout) anchorAt(t world.Tile) bool {
	for _, a := range l.Anchors {
		if world.TileOf(a.Pos) == t {
			return true
		}
	}
	return false
}

func (l *Layout) usable(entity string) bool { return l.Usable == nil || l.Usable(entity) }

func New(p *knowledge.Prototypes, w *world.World) *Layout {
	return &Layout{P: p, W: w, occ: map[world.Tile]int{}, Clear: map[world.Tile]bool{}, Reserved: map[world.Tile]bool{}}
}

// Chest — сундук раскладок (буферы, хранение): деревянный до первого обслуживания, после —
// железный (cluster.md, раздел 6; стадия 17).
var Chest = "wooden-chest"

// Rule — что можно под постройкой.
type Rule int

const (
	OffOre  Rule = iota // не на руде (не перекрывать месторождения)
	AnyLand             // где угодно на суше (ленты шахты, столбы)
)

// At — размещение на тайле (или nil).
func (l *Layout) At(t world.Tile) *Placement {
	if i, ok := l.occ[t]; ok {
		return &l.List[i]
	}
	return nil
}

// Free — тайл свободен для стройки по правилу.
func (l *Layout) Free(t world.Tile, r Rule) bool {
	if _, ok := l.occ[t]; ok || l.Reserved[t] {
		return false
	}
	if l.W == nil {
		return true
	}
	ok, _ := l.W.Buildable(t)
	if !ok {
		return false
	}
	if r == OffOre {
		if _, ore := l.W.ResourceAt(t); ore {
			return false
		}
	}
	return true
}

// CanPlace — встанет ли постройка (все её тайлы свободны по правилу).
func (l *Layout) CanPlace(pl Placement, r Rule) bool {
	for _, t := range Tiles(l.P, pl) {
		if !l.Free(t, r) {
			return false
		}
	}
	return true
}

// Add — добавить (без проверки; Validate проверит всё разом). Тайлы с деревьями — в расчистку.
func (l *Layout) Add(pl Placement) {
	i := len(l.List)
	l.List = append(l.List, pl)
	for _, t := range Tiles(l.P, pl) {
		l.occ[t] = i
		if l.W != nil {
			if _, clear := l.W.Buildable(t); clear {
				l.Clear[t] = true
			}
		}
	}
}

// Count — сколько каких построек.
func (l *Layout) Count() map[string]int {
	m := map[string]int{}
	for _, p := range l.List {
		m[p.Name]++
	}
	return m
}

func (l *Layout) Summary() string {
	m := l.Count()
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	s := ""
	for _, n := range names {
		s += fmt.Sprintf("%s ×%d; ", n, m[n])
	}
	return s + fmt.Sprintf("убрать перед стройкой: %d тайлов", len(l.Clear))
}

// Index — номер размещения на тайле.
func (l *Layout) Index(t world.Tile) (int, bool) {
	i, ok := l.occ[t]
	return i, ok
}

// Replace — заменить i-е размещение (номера ролей остаются).
func (l *Layout) Replace(i int, pl Placement) {
	l.List[i] = pl
	l.Rollback(-1)
}

// Mark/Rollback — откат неудачной попытки раскладки.
func (l *Layout) Mark() int { return len(l.List) }

func (l *Layout) Rollback(mark int) {
	if mark >= len(l.List) {
		return
	}
	if mark >= 0 {
		l.List = l.List[:mark]
	}
	l.occ = map[world.Tile]int{}
	l.Clear = map[world.Tile]bool{}
	list := l.List
	l.List = nil
	for _, p := range list {
		l.Add(p)
	}
}
