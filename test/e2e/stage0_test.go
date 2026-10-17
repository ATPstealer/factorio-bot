//go:build e2e

// Стадия 0 — окружение: карта, сервер, клиент в Xephyr, мод подключён.
package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStage0_MapGenSettingsFile(t *testing.T) {
	var m struct {
		PeacefulMode      bool                                   `json:"peaceful_mode"`
		AutoplaceControls map[string]struct{ Frequency float64 } `json:"autoplace_controls"`
		CliffSettings     struct{ Richness float64 }             `json:"cliff_settings"`
	}
	readJSON(t, path("scripts", "map-gen-settings.json"), &m)
	if f := m.AutoplaceControls["enemy-base"].Frequency; f != 0 {
		t.Errorf("enemy-base frequency = %v, нужно 0", f)
	}
	if m.CliffSettings.Richness != 0 {
		t.Errorf("cliff richness = %v, нужно 0", m.CliffSettings.Richness)
	}
	if !m.PeacefulMode {
		t.Error("peaceful_mode выключен")
	}
}

func TestStage0_ServerSettingsFile(t *testing.T) {
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

func TestStage0_ConfigIni(t *testing.T) {
	srv := ini(t, path("run", "server", "config.ini"))
	cli := ini(t, path("run", "client", "config.ini"))
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
	b, err := os.ReadFile(path("run", "client", "steam_appid.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "427520" {
		t.Errorf("run/client/steam_appid.txt должен содержать 427520 (иначе Steam перезапустит клиент на :0): %q %v", b, err)
	}
}

func TestStage0_ModLinked(t *testing.T) {
	want, _ := filepath.Abs(path("mod", "botbridge"))
	for _, side := range []string{"server", "client"} {
		link := path("run", side, "mods", "botbridge")
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

func TestStage0_ServerRunning(t *testing.T) {
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

func TestStage0_Mods(t *testing.T) {
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

func TestStage0_BotbridgePing(t *testing.T) {
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

func TestStage0_Map(t *testing.T) {
	var m struct {
		Seed    int
		Cliff   float64
		Enemy   float64
		Enemies int
	}
	lua(t, `{seed=s.map_gen_settings.seed, cliff=s.map_gen_settings.cliff_settings.richness,
		enemy=s.map_gen_settings.autoplace_controls["enemy-base"].frequency,
		enemies=s.count_entities_filtered{force="enemy"}}`, &m)
	if m.Seed != seed {
		t.Errorf("seed = %d, нужно %d", m.Seed, seed)
	}
	if m.Cliff != 0 || m.Enemy != 0 {
		t.Errorf("карта сгенерирована с утёсами/кусаками: cliff=%v enemy=%v", m.Cliff, m.Enemy)
	}
	if m.Enemies != 0 {
		t.Errorf("на карте %d вражеских сущностей", m.Enemies)
	}
}

func TestStage0_ClientConnected(t *testing.T) {
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
	if log, err := os.ReadFile(path("run", "client.log")); err != nil || !strings.Contains(string(log), "Steam API could not be initialized") {
		t.Error("клиент запущен со Steam API (нужен HOME=run/client/home, см. scripts/run.sh)")
	}
	// клиент бота изолирован от Steam: не трогает облако пользователя и заходит одним и тем же игроком
	if log, err := os.ReadFile(path("run", "client.log")); err != nil || !strings.Contains(string(log), "Steam API could not be initialized") {
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
