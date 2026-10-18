//go:build e2e

// Стадия 2 — сенсоры: справочник прототипов, сканирование открытой карты, месторождения, PNG.
// Критерий: бот строит в памяти карту стартовой зоны (ресурсы, вода, препятствия) и выводит PNG.
package e2e

import (
	"math"
	"testing"

	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

func protosT(t *testing.T) *knowledge.Prototypes {
	t.Helper()
	pong, err := bridgeT(t).Ping()
	if err != nil {
		t.Fatal(err)
	}
	p, err := knowledge.Fetch(bridgeT(t), pong.GameVersion) // всегда свежий дамп, не кэш
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func worldT(t *testing.T) *world.World {
	t.Helper()
	w := world.New(protosT(t))
	if _, err := w.Sync(bridgeT(t), true); err != nil {
		t.Fatal(err)
	}
	return w
}

// Модель совпадает с игрой: запасы руды по видам, вода, деревья — на всей открытой карте.
func TestBase_WorldMatchesGame(t *testing.T) {
	w := worldT(t)
	type counts struct {
		Ore   map[string]int
		Water int
		Trees int
	}
	var g counts
	lua(t, `(function()
		local water = {}
		for name, pr in pairs(prototypes.tile) do
			local L = pr.collision_mask.layers
			if L.water_tile and L.player and not L.out_of_map then water[#water+1] = name end
		end
		local r = {ore = {}, water = 0, trees = 0}
		for ch in s.get_chunks() do
			if p.force.is_chunk_charted(s, {ch.x, ch.y}) then
				for _, e in pairs(s.find_entities_filtered{area = ch.area, type = "resource"}) do
					if math.floor(e.position.x/32) == ch.x and math.floor(e.position.y/32) == ch.y then
						r.ore[e.name] = (r.ore[e.name] or 0) + e.amount
					end
				end
				r.water = r.water + s.count_tiles_filtered{area = ch.area, name = water}
				for _, e in pairs(s.find_entities_filtered{area = ch.area, type = "tree"}) do
					if math.floor(e.position.x/32) == ch.x and math.floor(e.position.y/32) == ch.y then r.trees = r.trees + 1 end
				end
			end
		end
		return r
	end)()`, &g)

	ore := map[string]int{}
	for _, p := range w.Patches() {
		ore[p.Name] += p.Amount
	}
	// буры точек копают и во время сравнения — допуск 0.1%
	for name, n := range g.Ore {
		if math.Abs(float64(ore[name]-n)) > float64(n)/1000 {
			t.Errorf("%s: в модели %d, в игре %d", name, ore[name], n)
		}
	}
	water, trees := 0, 0
	for _, c := range w.Chunks {
		for i := range c.Tiles {
			if c.Tiles[i] == world.Water {
				water++
			}
		}
		for _, o := range c.Objects {
			if o.Type == "tree" {
				trees++
			}
		}
	}
	if water != g.Water {
		t.Errorf("вода: в модели %d тайлов, в игре %d", water, g.Water)
	}
	if trees != g.Trees {
		t.Errorf("деревья: в модели %d, в игре %d", trees, g.Trees)
	}
	t.Logf("чанков %d, руды %v, воды %d, деревьев %d", len(w.Chunks), g.Ore, water, trees)
}
