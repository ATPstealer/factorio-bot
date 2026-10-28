package stage

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"

	"factorio-bot/internal/act"
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
