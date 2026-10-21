// Package stage — стадии прохождения игры (stages.md): по файлу на стадию. Стадия делает своё
// на текущей карте и проверяет критерии; выполнил на 3 новых картах — стадия пройдена.
package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"factorio-bot/internal/act"
)

// Stage — стадия: номер, название, выполнить, проверить критерии (nil — выполнены).
type Stage struct {
	N     int
	Name  string
	Run   func(ctx context.Context, h *act.Hands) error
	Check func(h *act.Hands) error
}

var all = map[int]Stage{}

// runPath — файл в папке экземпляра игры (BOT_RUN, по умолчанию run; scripts/inst.sh).
func runPath(name string) string {
	dir := os.Getenv("BOT_RUN")
	if dir == "" {
		dir = "run"
	}
	return filepath.Join(dir, name)
}

func register(s Stage) { all[s.N] = s }

// Get — стадия N.
func Get(n int) (Stage, error) {
	s, ok := all[n]
	if !ok {
		return s, fmt.Errorf("stage: стадии %d нет", n)
	}
	return s, nil
}

// List — номера стадий по порядку.
func List() []int {
	var res []int
	for n := range all {
		res = append(res, n)
	}
	sort.Ints(res)
	return res
}

// Do — выполнить стадию и проверить критерии.
func Do(ctx context.Context, h *act.Hands, n int) error {
	s, err := Get(n)
	if err != nil {
		return err
	}
	h.Say("стадия %d: %s", s.N, s.Name)
	if err := s.Run(ctx, h); err != nil {
		return fmt.Errorf("стадия %d: %w", n, err)
	}
	if err := s.Check(h); err != nil {
		return fmt.Errorf("стадия %d не выполнена: %w", n, err)
	}
	h.Say("стадия %d выполнена", n)
	return nil
}

// seedOf — сид текущей карты (map.json экземпляра).
func seedOf() string {
	var m struct {
		Seed int64 `json:"seed"`
	}
	if data, err := os.ReadFile(runPath("map.json")); err == nil {
		json.Unmarshal(data, &m)
	}
	return fmt.Sprint(m.Seed)
}
