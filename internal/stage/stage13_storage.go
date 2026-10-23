package stage

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 13 (stages.md): кластер хранения (склад бота) из плана стадии 5.

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
		var todo []layout.Placement
		for _, pl := range list {
			if pl.Name != name {
				continue
			}
			if e, err := s.entity(pl); err != nil {
				return err
			} else if e == nil {
				todo = append(todo, pl)
			}
		}
		if len(todo) == 0 {
			continue
		}
		if strings.HasPrefix(name, "underground") {
			todo = ugPairs(todo)
		}
		if err := s.have(ctx, name, len(todo)); err != nil {
			return err
		}
		// одинаковое — подряд одним вызовом (рука не меняется между постановками), пачками по
		// buildBatch: между пачками — место в инвентаре под срубленное
		for len(todo) > 0 {
			batch := todo[:min(len(todo), buildBatch)]
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
			if _, err := s.h.PlaceDirs(ctx, name, pos, dirs); err != nil {
				return fmt.Errorf("поставить %s ×%d с (%.1f, %.1f): %w", name, len(batch), pos[0].X, pos[0].Y, err)
			}
		}
	}
	return nil
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
	for _, pl := range list {
		e, err := h.B.EntityAt(pl.Name, pl.Pos)
		if err != nil {
			return err
		}
		if e == nil {
			return fmt.Errorf("не стоит %s в (%.1f, %.1f)", pl.Name, pl.Pos.X, pl.Pos.Y)
		}
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
