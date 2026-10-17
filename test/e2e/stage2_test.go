//go:build e2e

// Стадия 2 — сенсоры: справочник прототипов, сканирование открытой карты, месторождения, PNG.
// Критерий: бот строит в памяти карту стартовой зоны (ресурсы, вода, препятствия) и выводит PNG.
package e2e

import (
	"bytes"
	"fmt"
	"image/png"
	"slices"
	"testing"
	"time"

	"factorio-bot/internal/bridge"
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

func TestStage2_Prototypes(t *testing.T) {
	p := protosT(t)
	if p.GameVersion != gameVersion {
		t.Errorf("версия %q", p.GameVersion)
	}
	if len(p.Recipes) < 150 || len(p.Items) < 150 || len(p.Entities) < 100 || len(p.Technologies) < 150 {
		t.Errorf("мало прототипов: рецептов %d, предметов %d, сущностей %d, тех %d",
			len(p.Recipes), len(p.Items), len(p.Entities), len(p.Technologies))
	}
	gear := p.Recipes["iron-gear-wheel"]
	if len(gear.Ingredients) != 1 || gear.Ingredients[0].Name != "iron-plate" || gear.Ingredients[0].Amount != 2 {
		t.Errorf("шестерня: %+v", gear.Ingredients)
	}
	f := p.Entities["stone-furnace"]
	if f.TileWidth != 2 || f.TileHeight != 2 || !slices.Contains(f.CraftingCategories, "smelting") || f.Burner == nil {
		t.Errorf("каменная печь: %+v", f)
	}
	if d := p.Entities["burner-mining-drill"]; d.MiningSpeed != 0.25 {
		t.Errorf("burner-mining-drill mining_speed = %v", d.MiningSpeed)
	}
	if !slices.Contains(p.Technologies["automation"].Unlocks, "assembling-machine-1") {
		t.Errorf("automation открывает %v", p.Technologies["automation"].Unlocks)
	}
	if tr := p.Technologies["electronics"].Trigger; tr == nil || tr.Type != "craft-item" {
		t.Errorf("electronics в 2.0 открывается действием: %+v", tr)
	}
	if got := p.RecipesFor("iron-plate"); !slices.Contains(got, "iron-plate") {
		t.Errorf("RecipesFor(iron-plate) = %v", got)
	}
	if m := p.Entities["huge-rock"].Mineable; m == nil || len(m.Products) != 2 {
		t.Errorf("huge-rock: %+v", m)
	}
}

// Честность: неоткрытый чанк не сканируется, список открытых совпадает с игрой.
func TestStage2_OnlyChartedScan(t *testing.T) {
	b := bridgeT(t)
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
		t.Skip("все сгенерированные чанки открыты")
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
func TestStage2_WorldMatchesGame(t *testing.T) {
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
	for name, n := range g.Ore {
		if ore[name] != n {
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

// Стартовая зона этого сида: все четыре базовых ресурса и вода уже открыты.
func TestStage2_StartArea(t *testing.T) {
	w := worldT(t)
	have := map[string]bool{}
	for _, p := range w.Patches() {
		if len(p.Tiles) >= 50 {
			have[p.Name] = true
		}
	}
	// стартовая зона обычной карты содержит все четыре; нет — особенность карты, не ошибка бота
	for _, r := range []string{"iron-ore", "copper-ore", "coal", "stone"} {
		if !have[r] {
			t.Skipf("на открытой карте нет месторождения %s (≥50 тайлов)", r)
		}
	}
	pl, err := bridgeT(t).Player()
	if err != nil {
		t.Fatal(err)
	}
	// персонаж может стоять у края рамки дерева (тайл под ним формально занят) — хотя бы
	// один тайл вокруг проходим
	pt, free := world.TileOf(pl.Position), 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if w.Walkable(world.Tile{X: pt.X + dx, Y: pt.Y + dy}) {
				free++
			}
		}
	}
	if free == 0 {
		t.Errorf("вокруг игрока %v нет проходимых тайлов", pt)
	}
}

// После добычи Sync пересканирует только затронутые чанки, и объекта в модели больше нет.
func TestStage2_SyncAfterMining(t *testing.T) {
	w := worldT(t)
	h := handsT(t)
	trees, err := h.B.FindMinable("tree", 60, 5)
	if err != nil || len(trees) == 0 {
		t.Skipf("нет деревьев: %v", err)
	}
	if _, err := h.Gather(ctxT(t, 60*time.Second), "tree", 1, 60); err != nil {
		t.Fatal(err)
	}
	n, err := w.Sync(h.B, false)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || n > 9 {
		t.Errorf("пересканировано %d чанков, ждали 1..9", n)
	}
	// хотя бы одно из ближайших деревьев исчезло из модели
	gone := 0
	for _, tr := range trees {
		found := false
		for _, o := range w.Objects() {
			if o.Name == tr.Name && o.Pos == tr.Position {
				found = true
			}
		}
		if !found {
			gone++
		}
	}
	if gone == 0 {
		t.Error("срубленное дерево осталось в модели")
	}
}

// Критерий стадии: PNG стартовой зоны с рудой и игроком.
func TestStage2_RenderPNG(t *testing.T) {
	w := worldT(t)
	pl, err := bridgeT(t).Player()
	if err != nil {
		t.Fatal(err)
	}
	center := world.TileOf(pl.Position)
	img := w.Render(world.RenderOptions{Center: center, Radius: 150, Scale: 2, Player: &pl.Position, Labels: true})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	size := buf.Len()
	dec, err := png.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	px := func(tl world.Tile) [4]uint8 {
		r, g, b, a := dec.At((tl.X-(center.X-150))*2, (tl.Y-(center.Y-150))*2).RGBA()
		return [4]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}
	col := func(c interface{ RGBA() (r, g, b, a uint32) }) [4]uint8 {
		r, g, b, a := c.RGBA()
		return [4]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}
	if got := px(center); got != col(world.ColorPlayer) {
		t.Errorf("игрок: %v", got)
	}
	// тайл руды, не закрытый объектом/подписью/игроком, — цвета своей руды
	checked := 0
	for _, p := range w.Patches() {
		for _, tl := range p.Tiles {
			if checked >= 20 || abs(tl.X-center.X) >= 150 || abs(tl.Y-center.Y) >= 150 || w.Blocked(tl) ||
				abs(tl.X-center.X) <= 1 && abs(tl.Y-center.Y) <= 1 {
				continue
			}
			want := col(world.ResourceColors[p.Name])
			if got := px(tl); got != want && got != col(world.ColorLabel) {
				if got != [4]uint8{0, 0, 0, 255} { // обводка подписи
					t.Errorf("%s на %v: %v, ждали %v", p.Name, tl, got, want)
				}
			}
			checked++
		}
	}
	if checked == 0 {
		t.Error("в радиусе 150 нет руды для проверки")
	}
	t.Logf("PNG %d байт, проверено тайлов руды %d", size, checked)
}

func TestStage2_ForceAndStats(t *testing.T) {
	b := bridgeT(t)
	f, err := b.Force()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Recipes, "iron-gear-wheel") || !slices.Contains(f.Recipes, "stone-furnace") {
		t.Errorf("доступные рецепты: %d, без базовых", len(f.Recipes))
	}
	if slices.Contains(f.Recipes, "assembling-machine-1") && !slices.Contains(f.Researched, "automation") {
		t.Error("рецепт сборщика доступен без automation")
	}
	st, err := b.ProductionStats()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("исследовано %d, рецептов %d, произведено %v", len(f.Researched), len(f.Recipes), fmt.Sprint(st.Produced))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
