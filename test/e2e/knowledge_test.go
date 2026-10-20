//go:build e2e

// Знания: справочник прототипов и калькулятор.
package e2e

import (
	"factorio-bot/internal/calc"
	"factorio-bot/internal/knowledge"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Снимок internal/knowledge/testdata/prototypes.json — тот же, что в игре: юнит-тесты
// калькулятора проверяют настоящие данные.
func TestBase_SnapshotMatchesGame(t *testing.T) {
	live := protosT(t)
	snap, err := knowledge.Load(path("internal", "knowledge", "testdata", "prototypes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if snap.GameVersion != live.GameVersion {
		t.Fatalf("снимок для %s, игра %s — обнови снимок", snap.GameVersion, live.GameVersion)
	}
	for name, r := range live.Recipes {
		s, ok := snap.Recipes[name]
		if !ok || s.Energy != r.Energy || s.Category != r.Category ||
			!reflect.DeepEqual(s.Ingredients, r.Ingredients) || !reflect.DeepEqual(s.Products, r.Products) {
			t.Errorf("рецепт %s в снимке отличается от игры", name)
		}
	}
	for name, tc := range live.Technologies {
		s := snap.Technologies[name]
		if !reflect.DeepEqual(s.Prerequisites, tc.Prerequisites) || !reflect.DeepEqual(s.Unlocks, tc.Unlocks) || s.UnitCount != tc.UnitCount {
			t.Errorf("технология %s в снимке отличается от игры", name)
		}
	}
	if len(snap.Recipes) != len(live.Recipes) || len(snap.Technologies) != len(live.Technologies) {
		t.Errorf("размеры: снимок %d/%d, игра %d/%d", len(snap.Recipes), len(snap.Technologies), len(live.Recipes), len(live.Technologies))
	}
}

// Доступные рецепты, посчитанные по изученным техам, совпадают с открытыми в игре.
func TestBase_AvailabilityMatchesForce(t *testing.T) {
	p := protosT(t)
	f, err := bridgeT(t).Force()
	if err != nil {
		t.Fatal(err)
	}
	av := p.NewAvailability(f.Researched, false)
	var mine []string
	for r := range av.Recipes {
		if !p.Recipes[r].Hidden {
			mine = append(mine, r)
		}
	}
	var game []string
	for _, r := range f.Recipes {
		if !p.Recipes[r].Hidden {
			game = append(game, r)
		}
	}
	sort.Strings(mine)
	sort.Strings(game)
	if !slices.Equal(mine, game) {
		t.Errorf("доступность: посчитано %v, в игре %v", mine, game)
	}
}

// Проверка: ракетная часть 1/мин на живом справочнике и через CLI.
func TestBase_RocketPartPlan(t *testing.T) {
	p := protosT(t)
	f, err := bridgeT(t).Force()
	if err != nil {
		t.Fatal(err)
	}
	pl, err := calc.Solve(p, map[string]float64{"rocket-part": 1.0 / 60}, calc.Options{Researched: f.Researched, All: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pl.Techs, "rocket-silo") || pl.Raw["crude-oil"] <= 0 || pl.PowerW <= 0 {
		t.Errorf("план неполный: техи %d, сырьё %v, МВт %.1f", len(pl.Techs), pl.Raw, pl.PowerW/1e6)
	}

	bin := path("bin", "bot")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("нет bin/bot (go build -o bin/bot ./cmd/bot)")
	}
	cmd := exec.Command(bin, "calc", "-all", "rocket-part", "1/min")
	cmd.Dir = root()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bot calc: %v\n%s", err, out)
	}
	for _, want := range []string{"rocket-part 1.00 ← rocket-part", "сырьё с карты", "crude-oil", "МВт", "rocket-silo"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("в выводе bot calc нет %q", want)
		}
	}
}
