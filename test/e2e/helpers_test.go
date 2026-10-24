//go:build e2e

// Общие помощники e2e: связь с игрой, руки, карта, справочник, набор предметов.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/input"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/rcon"
	"factorio-bot/internal/world"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	brOnce sync.Once
	br     *bridge.Bridge

	handsOnce sync.Once
	hands     *act.Hands
	handsErr  error
)

func bridgeT(t *testing.T) *bridge.Bridge {
	t.Helper()
	g := game(t)
	brOnce.Do(func() { br = bridge.New(g) })
	return br
}

func handsT(t *testing.T) *act.Hands {
	t.Helper()
	b := bridgeT(t)
	handsOnce.Do(func() {
		in, err := input.Open(botDisplay)
		if err != nil {
			handsErr = err
			return
		}
		if handsErr = in.Reset(); handsErr != nil {
			return
		}
		hands, handsErr = act.New(b, in)
		if handsErr != nil {
			return
		}
		hands.DebugDir = runFile("debug")
		hands.Console = true
		pong, err := b.Ping()
		if err != nil {
			handsErr = err
			return
		}
		if hands.Protos, handsErr = knowledge.Fetch(b, pong.GameVersion); handsErr != nil {
			return
		}
		hands.World = world.New(hands.Protos)
		if _, handsErr = hands.World.Sync(b, true); handsErr != nil {
			return
		}
		handsErr = hands.Clean(context.Background())
	})
	if handsErr != nil {
		t.Fatalf("ввод: %v", handsErr)
	}
	hands.Log = t.Logf
	return hands
}

func ctxT(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

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

// freeArea — центр свободной (суша без объектов) площадки size×size рядом с игроком, не под ним.
func freeArea(t *testing.T, h *act.Hands, size int) bridge.Pos {
	t.Helper()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	c, ok := spot(h, p.Position, size, size, 60)
	if !ok {
		t.Skipf("нет свободной площадки %d×%d", size, size)
	}
	return c
}

// spot — World.FreeSpot у near (обходя персонажа), но только достижимая от него по суше
// площадка (заливка по суше и мелководью, деревья и свои постройки — не помеха: их убирают):
// на водной карте площадка бывала за водой — «пути нет» (PickupUndergroundKeepsNeighbors).
func spot(h *act.Hands, near bridge.Pos, w, hh, r int) (bridge.Pos, bool) {
	p, _ := h.B.Player()
	from := world.TileOf(p.Position)
	lim := r + w + hh + int(dist(near, p.Position))
	reach := map[world.Tile]bool{from: true}
	queue := []world.Tile{from}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			n := world.Tile{X: c.X + d[0], Y: c.Y + d[1]}
			if reach[n] || abs(n.X-from.X) > lim || abs(n.Y-from.Y) > lim {
				continue
			}
			if tt := h.World.TileAt(n); tt != world.Land && tt != world.Shallow {
				continue
			}
			reach[n] = true
			queue = append(queue, n)
		}
	}
	return h.World.FreeSpotWhere(near, w, hh, r, &p.Position, func(t world.Tile) bool { return reach[t] })
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

const gameVersion = "2.1.23"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// экземпляр игры (scripts/inst.sh): дисплей, RCON, папка
var (
	botDisplay = env("BOT_DISPLAY", ":1")
	rconAddr   = env("BOT_RCON", "127.0.0.1:27015")
)

// root — корень репозитория.
func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func path(parts ...string) string { return filepath.Join(append([]string{root()}, parts...)...) }

// runFile — файл в папке экземпляра игры (BOT_RUN, по умолчанию run/).
func runFile(parts ...string) string {
	return filepath.Join(append([]string{env("BOT_RUN", path("run"))}, parts...)...)
}

var (
	connOnce sync.Once
	conn     *rcon.Client
	connErr  error
)

// game — общее RCON-соединение; без запущенного сервера тест падает с подсказкой.
func game(t *testing.T) *rcon.Client {
	t.Helper()
	connOnce.Do(func() {
		conn, connErr = rcon.Dial(rconAddr, env("RCON_PASSWORD", "botpass"), 3*time.Second)
	})
	if connErr != nil {
		t.Fatalf("RCON %s: %v (запусти scripts/run.sh)", rconAddr, connErr)
	}
	return conn
}

// lua вычисляет Lua-выражение на сервере и декодирует его JSON в out.
// В выражении доступны s = game.surfaces[1], p = первый подключённый игрок (или nil).
func lua(t *testing.T, expr string, out any) {
	t.Helper()
	cmd := "/sc local s=game.surfaces[1] local p=game.connected_players[1] " +
		"rcon.print(helpers.table_to_json({v=" + expr + "}))"
	res, err := game(t).Exec(cmd)
	if err != nil {
		t.Fatalf("lua %q: %v", expr, err)
	}
	var wrap struct{ V json.RawMessage }
	if err := json.Unmarshal([]byte(res), &wrap); err != nil {
		t.Fatalf("lua %q: ответ %q: %v", expr, res, err)
	}
	if out != nil && len(wrap.V) > 0 {
		if err := json.Unmarshal(wrap.V, out); err != nil {
			t.Fatalf("lua %q: %s: %v", expr, wrap.V, err)
		}
	}
}

// bot вызывает метод мода botbridge и декодирует result в out.
func bot(t *testing.T, method string, args any, out any) {
	t.Helper()
	req, _ := json.Marshal(map[string]any{"method": method, "args": args})
	res, err := game(t).Exec(fmt.Sprintf(`/sc remote.call("bot", "call", %q)`, string(req)))
	if err != nil {
		t.Fatalf("bot.%s: %v", method, err)
	}
	var resp struct {
		OK     bool
		Error  string
		Result json.RawMessage
	}
	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		t.Fatalf("bot.%s: ответ %q: %v", method, res, err)
	}
	if !resp.OK {
		t.Fatalf("bot.%s: %s", method, resp.Error)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			t.Fatalf("bot.%s: %s: %v", method, resp.Result, err)
		}
	}
}

// xdo запускает xdotool в дисплее клиента.
func xdo(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("xdotool", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+botDisplay)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("xdotool %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readJSON(t *testing.T, file string, out any) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

// ini — плоский разбор config.ini Factorio: "секция.ключ" → значение.
func ini(t *testing.T, file string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	section := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == ';' || line[0] == '#':
		case line[0] == '[':
			section = strings.Trim(line, "[]")
		default:
			if k, v, ok := strings.Cut(line, "="); ok {
				m[section+"."+k] = v
			}
		}
	}
	return m
}

// have — довести предмет в инвентаре до n руками: сырьё (дерево, камень, уголь, руда) —
// добыть, остальное — скрафтить из него (ручной крафт). Нужна плавка (пластины) и их нет —
// тест пропускается: плавить без планировщика тесты не умеют.
func have(t *testing.T, h *act.Hands, item string, n int) {
	t.Helper()
	p, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	if p.Inventory[item] >= n {
		return
	}
	ctx := ctxT(t, 15*time.Minute)
	f, err := h.B.Force()
	if err != nil {
		t.Fatal(err)
	}
	av := h.Protos.NewAvailability(f.Researched, false)
	hc := calc.Handcraft(h.Protos, av, item, float64(n-p.Inventory[item]))
	raw := map[string]bool{"wood": true, "stone": true, "coal": true, "iron-ore": true, "copper-ore": true}
	for base, k := range hc.Base {
		if base == item {
			continue
		}
		need := int(math.Ceil(k))
		if !raw[base] {
			if p.Inventory[base] < need {
				t.Skipf("для %s нужно %s ×%d — плавить тесты не умеют", item, base, need)
			}
			continue
		}
		for tries := 0; tries < 50; tries++ {
			q, err := h.B.Player()
			if err != nil {
				t.Fatal(err)
			}
			if q.Inventory[base] >= need {
				break
			}
			if _, err := h.Gather(ctx, base, min(need-q.Inventory[base], 10), 64); err != nil {
				if errors.Is(err, act.ErrNoTargets) { // нет на карте рядом — рельеф (CLAUDE.md)
					t.Skipf("нет %s рядом: %v", base, err)
				}
				t.Fatalf("добыть %s: %v", base, err)
			}
		}
	}
	if raw[item] {
		for tries := 0; tries < 50; tries++ {
			if q, _ := h.B.Player(); q.Inventory[item] >= n {
				return
			}
			if _, err := h.Gather(ctx, item, n, 64); err != nil {
				if errors.Is(err, act.ErrNoTargets) { // нет на карте рядом — рельеф (CLAUDE.md)
					t.Skipf("нет %s рядом: %v", item, err)
				}
				t.Fatalf("добыть %s: %v", item, err)
			}
		}
		return
	}
	recipes := h.Protos.RecipesFor(item)
	if len(recipes) == 0 {
		t.Fatalf("нет рецепта %s", item)
	}
	rec := recipes[0]
	per := 1.0
	for _, pr := range h.Protos.Recipes[rec].Products {
		if pr.Name == item && pr.Amount > 0 {
			per = pr.Amount
		}
	}
	if err := h.CraftWait(ctx, rec, int(math.Ceil(float64(n-p.Inventory[item])/per))); err != nil {
		t.Fatalf("скрафтить %s: %v", item, err)
	}
}

func checkMapGenFile(t *testing.T) {
	var m struct {
		PeacefulMode      bool                                   `json:"peaceful_mode"`
		AutoplaceControls map[string]struct{ Frequency float64 } `json:"autoplace_controls"`
		CliffSettings     struct{ Richness float64 }             `json:"cliff_settings"`
	}
	readJSON(t, path("scripts", "map-gen-settings.json"), &m)
	if f := m.AutoplaceControls["enemy-base"].Frequency; f != 0 {
		t.Errorf("enemy-base frequency = %v, нужно 0", f)
	}
	if m.CliffSettings.Richness <= 0 {
		t.Errorf("cliff richness = %v: утёсы должны быть, как в обычной игре", m.CliffSettings.Richness)
	}
	if !m.PeacefulMode {
		t.Error("peaceful_mode выключен")
	}
}

func checkServerSettings(t *testing.T) {
	var s struct {
		AutoPause               bool `json:"auto_pause"`
		RequireUserVerification bool `json:"require_user_verification"`
		Visibility              struct{ Public, Lan bool }
	}
	readJSON(t, path("scripts", "server-settings.json"), &s)
	if s.AutoPause {
		t.Error("auto_pause = true: без игроков сервер встанет на паузу")
	}
	if s.RequireUserVerification {
		t.Error("require_user_verification = true: клиент не подключится без аккаунта")
	}
	if s.Visibility.Public {
		t.Error("сервер публичный")
	}
}

func checkConfigIni(t *testing.T) {
	srv := ini(t, runFile("server", "config.ini"))
	cli := ini(t, runFile("client", "config.ini"))
	if srv["path.write-data"] == "" || srv["path.write-data"] == cli["path.write-data"] {
		t.Errorf("write-data сервера и клиента должны быть разными: %q / %q", srv["path.write-data"], cli["path.write-data"])
	}
	want := map[string]string{
		"interface.ui-scale-mode":   "manual",
		"interface.custom-ui-scale": "1",
		"graphics.full-screen":      "false",
		"sound.master-muted":        "true",
	}
	for k, v := range want {
		if cli[k] != v {
			t.Errorf("client config.ini %s = %q, нужно %q", k, cli[k], v)
		}
	}
	b, err := os.ReadFile(runFile("client", "steam_appid.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "427520" {
		t.Errorf("run/client/steam_appid.txt должен содержать 427520 (иначе Steam перезапустит клиент на :0): %q %v", b, err)
	}
}

func checkModLinked(t *testing.T) {
	want, _ := filepath.Abs(path("mod", "botbridge"))
	for _, side := range []string{"server", "client"} {
		link := runFile(side, "mods", "botbridge")
		got, err := filepath.EvalSymlinks(link)
		if err != nil {
			t.Errorf("%s: %v", link, err)
			continue
		}
		if got != want {
			t.Errorf("%s → %s, нужно %s", link, got, want)
		}
	}
}

func checkServerRunning(t *testing.T) {
	var t1, t2 int
	lua(t, "game.tick", &t1)
	time.Sleep(500 * time.Millisecond)
	lua(t, "game.tick", &t2)
	if t2 <= t1 {
		t.Errorf("игра стоит: tick %d → %d", t1, t2)
	}
	var speed float64
	lua(t, "game.speed", &speed)
	if speed != 1 {
		t.Errorf("game.speed = %v, по плану 1", speed)
	}
}

func checkMods(t *testing.T) {
	var mods map[string]string
	lua(t, "script.active_mods", &mods)
	if mods["base"] != gameVersion {
		t.Errorf("base = %q, нужно %s", mods["base"], gameVersion)
	}
	if mods["botbridge"] == "" {
		t.Error("мод botbridge не загружен на сервере")
	}
	for name := range mods {
		if name != "base" && name != "botbridge" {
			t.Errorf("лишний мод: %s", name)
		}
	}
}

func checkBotbridgePing(t *testing.T) {
	var info struct{ Version string }
	readJSON(t, path("mod", "botbridge", "info.json"), &info)
	var pong struct {
		Tick    int
		Version string
	}
	bot(t, "ping", nil, &pong)
	if pong.Version != info.Version {
		t.Errorf("версия мода в игре %q, в info.json %q — перезапусти сервер", pong.Version, info.Version)
	}
	if pong.Tick <= 0 {
		t.Errorf("tick = %d", pong.Tick)
	}
}

func checkMap(t *testing.T) {
	var m struct {
		Seed    int
		Cliff   float64
		Enemy   float64
		Enemies int
	}
	lua(t, `{seed=s.map_gen_settings.seed, cliff=s.map_gen_settings.cliff_settings.richness,
		enemy=s.map_gen_settings.autoplace_controls["enemy-base"].frequency,
		enemies=s.count_entities_filtered{force="enemy", type={"unit","unit-spawner","turret"}}}`, &m)
	// карта — та, что создал scripts/run.sh (сид в run/map.json), с настройками из файла
	var mj struct{ Seed int }
	readJSON(t, runFile("map.json"), &mj)
	if m.Seed != mj.Seed {
		t.Errorf("seed = %d, в run/map.json %d", m.Seed, mj.Seed)
	}
	// утёсы — как в одном из файлов настроек генерации (обычная карта, лес, вода)
	var cliffs []float64
	files, _ := filepath.Glob(path("scripts", "map-gen-*.json"))
	for _, f := range files {
		var file struct {
			CliffSettings struct{ Richness float64 } `json:"cliff_settings"`
		}
		readJSON(t, f, &file)
		cliffs = append(cliffs, file.CliffSettings.Richness)
	}
	if !slices.Contains(cliffs, m.Cliff) || m.Enemy != 0 {
		t.Errorf("настройки карты: cliff=%v (в файлах %v), enemy=%v", m.Cliff, cliffs, m.Enemy)
	}
	t.Logf("карта: сид %d", m.Seed)
	if m.Enemies != 0 {
		t.Errorf("на карте %d вражеских сущностей", m.Enemies)
	}
}

func checkClient(t *testing.T) {
	var c struct {
		Players   int
		Character bool
		Scale     float64
		W, H      int
	}
	lua(t, `{players=#game.connected_players, character=p and p.character~=nil,
		scale=p and p.display_scale, w=p and p.display_resolution.width, h=p and p.display_resolution.height}`, &c)
	if c.Players != 1 {
		t.Fatalf("подключено игроков: %d, нужен 1 (клиент бота)", c.Players)
	}
	if !c.Character {
		t.Error("у игрока нет персонажа")
	}
	// клиент бота изолирован от Steam: не трогает облако пользователя и заходит одним и тем же игроком
	if log, err := os.ReadFile(runFile("client.log")); err != nil || !strings.Contains(string(log), "Steam API could not be initialized") {
		t.Error("клиент запущен со Steam API (нужен HOME=run/client/home, см. scripts/run.sh)")
	}
	// клиент бота изолирован от Steam: не трогает облако пользователя и заходит одним и тем же игроком
	if log, err := os.ReadFile(runFile("client.log")); err != nil || !strings.Contains(string(log), "Steam API could not be initialized") {
		t.Error("клиент запущен со Steam API (нужен HOME=run/client/home, см. scripts/run.sh)")
	}
	if c.Scale != 1 {
		t.Errorf("display_scale = %v, нужно 1", c.Scale)
	}

	// клиент живёт в Xephyr, а не на основном дисплее
	win := strings.Fields(xdo(t, "search", "--name", "^Factorio"))
	if len(win) == 0 {
		t.Fatalf("окно Factorio не найдено в дисплее %s", botDisplay)
	}
	geom := xdo(t, "getwindowgeometry", "--shell", win[0])
	var w, h int
	for _, line := range strings.Split(geom, "\n") {
		k, v, _ := strings.Cut(line, "=")
		n, _ := strconv.Atoi(v)
		switch k {
		case "WIDTH":
			w = n
		case "HEIGHT":
			h = n
		}
	}
	if w != c.W || h != c.H {
		t.Errorf("окно %dx%d, а игра рендерит %dx%d", w, h, c.W, c.H)
	}
}

// clean — после действия бот ничего не держит: нет окон, рука пуста, не идёт и не копает.
func clean(t *testing.T, h *act.Hands) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	p, err := h.B.Player()
	if err != nil {
		t.Fatal(err)
	}
	if p.OpenedGUI != "none" || p.RenderMode != "game" || p.Cursor != nil || p.Character.Walking || p.Character.Mining {
		t.Errorf("после действия: окно %q, вид %q, рука %+v, walking=%v, mining=%v", p.OpenedGUI, p.RenderMode, p.Cursor, p.Character.Walking, p.Character.Mining)
	}
	if open, err := h.MenuOpen(); err != nil || open {
		t.Errorf("открыто меню игры (%v)", err)
	}
}

// Меню игры (Esc без окна) мод не видит: бот распознаёт его по снимку и закрывает.
// Регрессия: тесты висели под открытым меню.
func checkMenuGuard(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 30*time.Second)
	if err := h.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	if open, err := h.MenuOpen(); err != nil || open {
		t.Fatalf("меню открыто до теста (%v)", err)
	}
	if err := h.Focus(); err != nil {
		t.Fatal(err)
	}
	if err := h.In.Tap("escape"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if open, err := h.MenuOpen(); err != nil || !open {
		t.Fatalf("меню не распознано после Esc (%v)", err)
	}
	// действие под меню: WalkTo должен сам закрыть меню и дойти
	target := walkTarget(t, h)
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	clean(t, h)
}

// Регрессия: бот оказался на карте (Ctrl+F без окна открывает поиск по карте) и не вышел.
// Карту мод видит (render_mode) — guard закрывает её перед действием.
func checkMapGuard(t *testing.T) {
	h := handsT(t)
	ctx := ctxT(t, 30*time.Second)
	if err := h.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Focus(); err != nil {
		t.Fatal(err)
	}
	if err := h.In.Tap("tab"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.B.Player(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if p, _ := h.B.Player(); p.RenderMode == "game" {
		t.Fatalf("Tab не открыл карту: %q", p.RenderMode)
	}
	target := walkTarget(t, h)
	if err := h.WalkTo(ctx, target, 0.5); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.B.Player(); p.RenderMode != "game" {
		t.Errorf("карта не закрыта: %q", p.RenderMode)
	}
	clean(t, h)
}

// walkTarget — проходимая точка в нескольких тайлах от игрока (центр свободного тайла).
func walkTarget(t *testing.T, h *act.Hands) bridge.Pos {
	t.Helper()
	if _, err := h.World.Sync(h.B, false); err != nil {
		t.Fatal(err)
	}
	p, _ := h.B.Player()
	for r := 3; r <= 8; r++ {
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}} {
			c := world.TileOf(bridge.Pos{X: p.Position.X + float64(d[0]*r), Y: p.Position.Y + float64(d[1]*r)})
			if pts, err := h.World.Path(p.Position, c.Center(), 0.5, 20000); err == nil && len(pts) > 0 {
				return c.Center()
			}
		}
	}
	t.Skip("рядом нет достижимой точки")
	return bridge.Pos{}
}
