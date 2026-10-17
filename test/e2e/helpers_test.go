//go:build e2e

// Регрессионные e2e-тесты по стадиям плана. Требуют запущенного scripts/run.sh.
// Запуск: scripts/test.sh (все стадии) или go test -tags e2e ./test/e2e/ -run Stage0
// Каждая стадия — свой файл stageN_test.go с тестами TestStageN_*.
// Запросы к игре — только чтение; действия — только ввод в дисплей клиента.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"factorio-bot/internal/rcon"
)

const (
	rconAddr    = "127.0.0.1:27015"
	gameVersion = "2.0.77"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var botDisplay = env("BOT_DISPLAY", ":1")

// root — корень репозитория.
func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func path(parts ...string) string { return filepath.Join(append([]string{root()}, parts...)...) }

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
