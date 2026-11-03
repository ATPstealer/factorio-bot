//go:build worlds

// Планы на сохранённых картах без игры (run/worlds/<сид>.gob, bin/bot estimate и rough):
// прикидка стадии 1 и стадия 2 с прогоном стадий на бумаге до ракеты — на разведанных картах
// (run/snapshots/explored/<сид>-1). Долго (минуты на карту), потому тегом:
//
//	go test -tags worlds ./test/worlds -timeout 0 [-run Estimate|Plan] [-parallel N]
//
// Состояние, журнал и картинки каждой карты — в run/worlds-test/<проверка>/<сид>/.
package worlds

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// root — корень репозитория; bot — собранный бот (TestMain).
var root, bot string

// perMap — лимит одной карты: стадия 2 и бумага до 68 на трудной карте — до часа.
const perMap = 90 * time.Minute

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	root = filepath.Join(wd, "..", "..")
	dir, err := os.MkdirTemp("", "worlds-bot")
	if err != nil {
		panic(err)
	}
	bot = filepath.Join(dir, "bot")
	build := exec.Command("go", "build", "-o", bot, "./cmd/bot")
	build.Dir, build.Stdout, build.Stderr = root, os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// explored — сиды разведанных карт (снимки <сид>-1), у которых есть сохранённая карта.
func explored(t *testing.T) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, "run", "snapshots", "explored", "*-1"))
	if err != nil {
		t.Fatal(err)
	}
	var res []string
	for _, d := range dirs {
		seed := strings.TrimSuffix(filepath.Base(d), "-1")
		if _, err := os.Stat(filepath.Join(root, "run", "worlds", seed+".gob")); err == nil {
			res = append(res, seed)
		}
	}
	if len(res) == 0 {
		t.Skip("нет разведанных карт с run/worlds/<сид>.gob")
	}
	return res
}

// runBot — бот без игры по карте сида: состояние — копия снимка <сид>-1 (map.json, круги
// разведки) в run/worlds-test/<name>/<сид>/; журнал — out.log там же.
func runBot(t *testing.T, name, seed string, args ...string) {
	t.Helper()
	dir := filepath.Join(root, "run", "worlds-test", name, seed)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(root, "run", "snapshots", "explored", seed+"-1")
	for _, f := range []string{"map.json", "state-squares.json"} {
		data, err := os.ReadFile(filepath.Join(snap, f))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log, err := os.Create(filepath.Join(dir, "out.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), perMap)
	defer cancel()
	cmd := exec.CommandContext(ctx, bot, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, log, log
	cmd.Env = append(os.Environ(), "BOT_RUN="+dir)
	t0 := time.Now()
	err = cmd.Run()
	t.Logf("%s: %.0f с, журнал — %s", seed, time.Since(t0).Seconds(), log.Name())
	if err != nil {
		t.Fatalf("%s: %v; конец журнала:\n%s", seed, err, tail(log.Name(), 15))
	}
}

// tail — последние n строк файла.
func tail(file string, n int) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// Прикидка стадии 1 от точки старта (как в фоне при разведке): на разведанной карте база встаёт.
func TestEstimate(t *testing.T) {
	for _, seed := range explored(t) {
		t.Run(seed, func(t *testing.T) {
			t.Parallel()
			runBot(t, "estimate", seed, "estimate", filepath.Join("run", "worlds", seed+".gob"))
		})
	}
}

// Стадия 2 (план всей базы до ракеты) и стадии стройки 10–68 на бумаге: на разведанной карте
// план встаёт, на бумаге нарушений нет, весь план — в раскладках стадий.
func TestPlan(t *testing.T) {
	for _, seed := range explored(t) {
		t.Run(seed, func(t *testing.T) {
			t.Parallel()
			runBot(t, "plan", seed, "rough", filepath.Join("run", "worlds", seed+".gob"), "68")
		})
	}
}
