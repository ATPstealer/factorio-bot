package stage

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 13 (stages.md): кластер хранения (склад бота) из плана стадии 3.

// freeSlots — свободных ячеек инвентаря перед постройкой не меньше (стадии с хранением): место
// под срубленное на месте постройки (стадия 11: «дерево не поднять — инвентарь полон»).
const freeSlots = 5

// buildBatch — сколько одинаковых построек ставить одним вызовом (рука не меняется).
const buildBatch = 40

// rawItems — что убирать в хранение при постройке: сырьё с вырубки и руда.
var rawItems = []string{"wood", "stone", "iron-ore", "copper-ore"}

func init() {
	register(Stage{N: 13, Name: "хранение", Run: runStorage, Check: checkStorage})
}

// storageNear — сундуки хранения, ближние к игроку первыми (кладёт и берёт — из ближних).
func storageNear(h *act.Hands) ([]layout.Placement, error) {
	list, err := storageLayout()
	if err != nil {
		return nil, err
	}
	if p, err := h.B.Player(); err == nil {
		slices.SortStableFunc(list, func(a, b layout.Placement) int {
			da := math.Hypot(a.Pos.X-p.Position.X, a.Pos.Y-p.Position.Y)
			db := math.Hypot(b.Pos.X-p.Position.X, b.Pos.Y-p.Position.Y)
			return cmp.Compare(da, db)
		})
	}
	return list, nil
}

// storageLayout — постройки кластера хранения из плана.
func storageLayout() ([]layout.Placement, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, err
	}
	var res []layout.Placement
	for _, c := range plan.Clusters {
		if c.Kind != cluster.Storage {
			continue
		}
		for _, pl := range plan.Layout {
			if pl.Tag == cluster.Tag(c.Name) {
				res = append(res, pl)
			}
		}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("в плане нет кластера хранения")
	}
	// расширение хранения обслуживанием (стадия 22) — те же сундуки хранения
	if data, err := os.ReadFile(runPath(storageExtraFile)); err == nil {
		var extra []layout.Placement
		if err := json.Unmarshal(data, &extra); err != nil {
			return nil, fmt.Errorf("%s: %w", storageExtraFile, err)
		}
		res = append(res, extra...)
	}
	return res, nil
}

func runStorage(ctx context.Context, h *act.Hands) error {
	list, err := storageLayout()
	if err != nil {
		return err
	}
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	return (&worker{h: h, sites: sites}).build(ctx, list)
}

// build — поставить недостающее из list: по видам в порядке первого появления, каждый вид —
// скрафтить разом и поставить.
func (s *worker) build(ctx context.Context, list []layout.Placement) error {
	var names []string
	for _, pl := range list {
		if !slices.Contains(names, pl.Name) {
			names = append(names, pl.Name)
		}
	}
	for _, name := range names {
		var these []layout.Placement
		for _, pl := range list {
			if pl.Name == name {
				these = append(these, pl)
			}
		}
		todo, err := notBuilt(s.h, these) // пачками: по одной — тысячи запросов на стадии 30
		if err != nil {
			return err
		}
		if len(todo) == 0 {
			continue
		}
		if strings.HasPrefix(name, "underground") {
			// стоящие не тем концом (вход/выход не как в раскладке; беспорядок прошлых заходов) —
			// снять и поставить заново по порядку: новые иначе встают в пары с ними (вход спарен
			// с будущим входом — «встал не тем направлением», стадия 30 на 260647970)
			if fixed, err := s.unpairWrong(ctx, these); err != nil {
				return err
			} else if fixed {
				if todo, err = notBuilt(s.h, these); err != nil {
					return err
				}
			}
			todo = ugPairs(todo)
		}
		// одинаковое — подряд одним вызовом (рука не меняется между постановками), пачками по
		// buildBatch: между пачками — место в инвентаре под срубленное. Добирать — сколько
		// влезает в инвентарь, не всё сразу (1244 лент в него не помещались — 1028292478,
		// стадия 16)
		for len(todo) > 0 {
			n := min(len(todo), buildBatch)
			if s.inv(name) < n && s.useStore {
				// к хранению — за всем, что нужно на оставшуюся стройку, одним заходом (stages.md,
				// инвентарь); чего там нет — have (крафт)
				all, from := list, name
				if s.all != nil { // по участкам — на всю оставшуюся стройку стадии
					all, from = s.all, s.all[0].Name
				}
				if err := s.restock(ctx, all, from); err != nil {
					return err
				}
			}
			if s.inv(name) < n {
				if err := s.have(ctx, name, min(len(todo), max(n, s.roomFor(name)))); err != nil {
					return err
				}
			}
			batch := todo[:min(n, s.inv(name))]
			if len(batch) == 0 {
				return fmt.Errorf("нет %s в инвентаре для стройки", name)
			}
			todo = todo[len(batch):]
			if s.useStore { // место под срубленное на месте постройки (деревья, камни): в
				// хранение — только сырьё с вырубки, постройки и запас стадии остаются
				p, err := s.h.B.Player()
				if err != nil {
					return err
				}
				if err := s.store(ctx, freeSlots, keepBuilt(p)...); err != nil {
					return err
				}
			}
			pos, dirs := make([]bridge.Pos, len(batch)), make([]int, len(batch))
			for i, pl := range batch {
				pos[i], dirs[i] = pl.Pos, pl.Dir
			}
			_, err := s.h.PlaceDirs(ctx, name, pos, dirs)
			for tries := 0; errors.Is(err, act.ErrNoItem) && tries < 3; tries++ {
				// предмет ушёл в хранение по дороге (OnFull: место под подъём своей постройки) —
				// добрать и ещё раз, только не поставленное (260647970, стадия 16)
				pos, dirs = pos[:0], dirs[:0]
				for _, pl := range batch {
					if e, eerr := s.entity(pl); eerr == nil && e == nil {
						pos, dirs = append(pos, pl.Pos), append(dirs, pl.Dir)
					}
				}
				if len(pos) == 0 {
					err = nil
					break
				}
				if herr := s.have(ctx, name, min(len(pos), s.roomFor(name))); herr != nil {
					return herr
				}
				_, err = s.h.PlaceDirs(ctx, name, pos, dirs)
			}
			if err != nil {
				return fmt.Errorf("поставить %s ×%d с (%.1f, %.1f): %w", name, len(batch), pos[0].X, pos[0].Y, err)
			}
		}
		if s.placed != nil {
			if err := s.placed(ctx, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// restock — один заход к хранению за всем, что нужно на оставшуюся стройку list начиная с вида
// from (stages.md, инвентарь; просьба пользователя 27.10: раньше — ходка за каждым видом,
// на стадии 16 восемь): по видам в порядке стройки, недостающее в инвентаре — из сундуков
// хранения, пока есть свободные ячейки (freeSlots — под срубленное). Чего в хранении нет —
// не берёт (have потом скрафтит).
func (s *worker) restock(ctx context.Context, list []layout.Placement, from string) error {
	var names []string
	for _, pl := range list {
		if !slices.Contains(names, pl.Name) {
			names = append(names, pl.Name)
		}
	}
	if i := slices.Index(names, from); i > 0 {
		names = names[i:]
	}
	var rest []layout.Placement
	for _, pl := range list {
		if slices.Contains(names, pl.Name) {
			rest = append(rest, pl)
		}
	}
	left, err := notBuilt(s.h, rest)
	if err != nil {
		return err
	}
	need := map[string]int{}
	for _, pl := range left {
		need[s.h.Protos.ItemFor(pl.Name)]++
	}
	chests, err := storageNear(s.h)
	if err != nil {
		return nil // хранения ещё нет — как раньше (have)
	}
	type stock struct {
		e    bridge.Entity
		have map[string]int
	}
	var store []stock
	for _, pl := range chests {
		st, err := s.h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		store = append(store, stock{st.Entity, st.Inventories["chest"]})
	}
	took := false
	for _, name := range names {
		it := s.h.Protos.ItemFor(name)
		want := need[it] - s.inv(it)
		if want <= 0 {
			continue
		}
		stack := max(s.h.Protos.Items[it].StackSize, 1)
		if room := (s.freeSlots() - freeSlots) * stack; room < want {
			want = room
		}
		for _, c := range store {
			if want <= 0 {
				break
			}
			n := min(want, c.have[it])
			if n <= 0 {
				continue
			}
			if !took { // перед первым взятием — ненужное в хранение (место)
				if err := s.tidy(ctx); err != nil && !errors.Is(err, errNoStorage) {
					return err
				}
				took = true
			}
			before := s.inv(it)
			if err := s.h.TakeItem(ctx, c.e, it, n); err != nil {
				return err
			}
			got := s.inv(it) - before
			c.have[it] -= got
			want -= got
			if got == 0 {
				break // инвентарь полон
			}
		}
	}
	return nil
}

// roomFor — сколько item поместится в инвентарь: что уже есть плюс свободные ячейки пачками.
func (s *worker) roomFor(item string) int {
	p, err := s.h.B.Player()
	if err != nil {
		return 0
	}
	stack := max(s.h.Protos.Items[item].StackSize, 1)
	return p.Inventory[item] + (p.InventorySize-len(p.InventorySlots))*stack
}

func checkStorage(h *act.Hands) error {
	list, err := storageLayout()
	if err != nil {
		return err
	}
	return checkBuilt(h, list)
}

// checkBuilt — всё из list стоит.
func checkBuilt(h *act.Hands, list []layout.Placement) error {
	miss, err := notBuilt(h, list) // пачками: по одной — минуты на больших стадиях
	if err != nil {
		return err
	}
	if len(miss) > 0 {
		return fmt.Errorf("не стоит %s в (%.1f, %.1f)", miss[0].Name, miss[0].Pos.X, miss[0].Pos.Y)
	}
	return nil
}

// ugPairs — подземки ленты парами «вход, потом выход» (выход, поставленный раньше входа,
// игра делает входом): на одной линии одного направления — по ходу ленты, по две подряд.
func ugPairs(list []layout.Placement) []layout.Placement {
	type key struct{ dir, line int }
	along := func(pl layout.Placement) float64 { // координата по ходу ленты
		switch pl.Dir {
		case layout.N:
			return -pl.Pos.Y
		case layout.S:
			return pl.Pos.Y
		case layout.W:
			return -pl.Pos.X
		}
		return pl.Pos.X
	}
	groups := map[key][]layout.Placement{}
	var keys []key
	for _, pl := range list {
		t := world.TileOf(pl.Pos)
		k := key{pl.Dir, t.Y}
		if pl.Dir == layout.N || pl.Dir == layout.S {
			k.line = t.X
		}
		if groups[k] == nil {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], pl)
	}
	var res []layout.Placement
	for _, k := range keys {
		g := groups[k]
		sort.SliceStable(g, func(i, j int) bool { return along(g[i]) < along(g[j]) })
		res = append(res, g...)
	}
	return res
}

// unpairWrong — подземки list, что стоят не тем концом (ug_type ≠ вход/выход раскладки), —
// снять; true — снято что-то.
func (s *worker) unpairWrong(ctx context.Context, list []layout.Placement) (bool, error) {
	states, err := entityStates(s.h, list)
	if err != nil {
		return false, err
	}
	var wrong []bridge.Entity
	for i, st := range states {
		if st != nil && list[i].Recipe != "" && st.UGType != "" && st.UGType != list[i].Recipe {
			wrong = append(wrong, st.Entity)
		}
	}
	if len(wrong) == 0 {
		return false, nil
	}
	s.h.Say("%d подземок стоят не тем концом — снимаю и ставлю по порядку", len(wrong))
	return true, s.h.PickupAll(ctx, wrong)
}
