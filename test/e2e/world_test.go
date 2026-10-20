//go:build e2e

// Карта и честность: бот видит только открытое, модель совпадает с игрой.
package e2e

import (
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/world"
	"fmt"
	"math"
	"testing"
)

// Честность: поиск сущностей не видит неоткрытые (не charted) чанки.
func TestBase_OnlyCharted(t *testing.T) {
	b := bridgeT(t)
	for _, find := range []func() ([]bridge.Entity, error){
		func() ([]bridge.Entity, error) {
			return b.FindNearest(bridge.FindArgs{Type: "tree", Radius: 2000, Limit: 100000})
		},
		func() ([]bridge.Entity, error) { return b.FindMinable("any", 2000, 100000) },
	} {
		es, err := find()
		if err != nil {
			t.Fatal(err)
		}
		hidden := 0
		// результаты отсортированы по расстоянию — проверяем 300 самых дальних, они у края открытой зоны
		far := es[max(0, len(es)-300):]
		for _, e := range far {
			var charted bool
			lua(t, fmt.Sprintf("p.force.is_chunk_charted(s, {%d, %d})",
				int(math.Floor(e.Position.X/32)), int(math.Floor(e.Position.Y/32))), &charted)
			if !charted {
				hidden++
			}
		}
		if hidden > 0 {
			t.Errorf("найдено %d сущностей в неоткрытых чанках", hidden)
		}
		t.Logf("найдено %d, в неоткрытых чанках: %d", len(es), hidden)
	}
	// контроль: в сгенерированных, но не открытых чанках деревья есть — значит фильтр реально отсекает
	var hiddenTrees int
	lua(t, `(function() local n=0 for ch in s.get_chunks() do if not p.force.is_chunk_charted(s,{ch.x,ch.y}) then
		n=n+s.count_entities_filtered{area=ch.area,type="tree"} end end return n end)()`, &hiddenTrees)
	if hiddenTrees == 0 {
		t.Log("в неоткрытых чанках деревьев нет — проверка фильтра неинформативна")
	}

	// скан карты: список открытых чанков совпадает с игрой, неоткрытый чанк — пустой
	charted, err := b.ListCharted()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	lua(t, `(function() local n=0 for ch in s.get_chunks() do if p.force.is_chunk_charted(s,{ch.x,ch.y}) then n=n+1 end end return n end)()`, &n)
	if len(charted) != n || n == 0 {
		t.Errorf("открытых чанков: мод %d, игра %d", len(charted), n)
	}
	var hidden *bridge.ChunkPos
	lua(t, `(function() for ch in s.get_chunks() do if not p.force.is_chunk_charted(s,{ch.x,ch.y}) then return {x=ch.x,y=ch.y} end end end)()`, &hidden)
	if hidden == nil {
		t.Log("все сгенерированные чанки открыты")
		return
	}
	scans, err := b.ScanChunks([]bridge.ChunkPos{*hidden})
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 1 || scans[0].Charted || scans[0].Tiles != "" || len(scans[0].Entities)+len(scans[0].Resources) != 0 {
		t.Errorf("неоткрытый чанк %v отдал данные: %+v", *hidden, scans)
	}
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
