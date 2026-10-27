// Package stage — стадии прохождения игры (stages.md): по файлу на стадию. Стадия делает своё
// на текущей карте и проверяет критерии; выполнил на 3 новых картах — стадия пройдена.
package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/layout"
)

// Stage — стадия: номер, название, выполнить, проверить критерии (nil — выполнены).
type Stage struct {
	N     int
	Name  string
	Run   func(ctx context.Context, h *act.Hands) error
	Check func(h *act.Hands) error
	Plan  bool // стадия планирования (только «на бумаге»: зоны, план кластеров, проекты)
	Empty bool // пустая (резерв): прогон только копирует снимок прошлой стадии (scripts/stage.sh)
}

var all = map[int]Stage{}

// PlanOnly — прогон одних стадий планирования подряд (bin/bot plans): стройки между ними нет,
// а план прошлых стадий считается построенным — свои постройки на карте (точки стадии 10 и т.п.)
// планам не мешают.
var PlanOnly bool

// Plans — номера стадий планирования в from…to по порядку.
func Plans(from, to int) []int {
	var res []int
	for _, n := range List() {
		if n >= from && n <= to && all[n].Plan {
			res = append(res, n)
		}
	}
	return res
}

// runPath — файл в папке экземпляра игры (BOT_RUN, по умолчанию run; scripts/inst.sh).
func runPath(name string) string {
	dir := os.Getenv("BOT_RUN")
	if dir == "" {
		dir = "run"
	}
	return filepath.Join(dir, name)
}

// writeState — v в файл состояния стадий file (runPath).
func writeState(file string, v any) error {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(runPath(file), data, 0o644)
}

// freshLayouts — раскладки стадий считать заново, не из файла (обновление чертежа на
// обслуживании — refreshLayouts).
var freshLayouts bool

// onceLayout — раскладка стадии один раз на карту: есть файл file — из него, нет — build и в
// файл (стройка, проверка и перезапуск стадии видят одну и ту же раскладку); freshLayouts —
// заново и в файл.
func onceLayout(file string, build func() ([]layout.Placement, error)) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(file)); err == nil && !freshLayouts {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list, nil
		}
	}
	list, err := build()
	if err != nil {
		return nil, err
	}
	return list, writeState(file, list)
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

// Limit — лимит стадии (bin/bot -t); 0 — без лимита. Жирной стадии Do сразу даёт полторы оценки
// из проверки перед стройкой, если это больше (stages.md, общее: лимит с запасом).
var Limit time.Duration

// estimate — оценка времени стройки текущей стадии (prepTime).
var estimate time.Duration

// Do — выполнить стадию и проверить критерии.
func Do(ctx context.Context, h *act.Hands, n int) error {
	s, err := Get(n)
	if err != nil {
		return err
	}
	start, base := time.Now(), ctx
	estimate = 0
	if Limit > 0 { // до оценки — обычный лимит
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(base, start.Add(Limit))
		defer cancel()
	}
	h.Say("стадия %d: %s", s.N, s.Name)
	if n >= 22 { // с первого обслуживания — только железные сундуки (и в планах следующих стадий)
		layout.Chest, chest = ironChest, ironChest
	}
	// нужное текущей стадии — по её плану (inventory.go)
	curStage, needCache.items = n, nil
	// добыча упёрлась в полный инвентарь (стадия 23 на 859088572) — в хранение ненужное; не
	// хватило — и запас (сырьё, пластины, уголь); постройки — при себе (их ставить)
	h.OnFull = func(ctx context.Context) error {
		s := &worker{h: h, useStore: true}
		err := s.store(ctx, craftFree+5)
		if err == nil {
			p, perr := h.B.Player()
			if perr != nil || p.InventorySize-len(p.InventorySlots) >= craftFree {
				return perr
			}
			bulk := map[string]bool{"iron-plate": true, "copper-plate": true, "steel-plate": true, "stone-brick": true, "coal": true}
			var keep []string
			for it := range p.Inventory {
				if !bulk[it] && !slices.Contains(rawItems, it) {
					keep = append(keep, it)
				}
			}
			s.any = true
			err = s.store(ctx, craftFree+5, keep...)
			// всё ещё полон (одни постройки стадии — 260647970, стадия 16: бур с пути не
			// поднять) — и постройки: стройка доберёт их из хранения пачками (have)
			if p, perr := h.B.Player(); err == nil && perr == nil && p.InventorySize-len(p.InventorySlots) < craftFree {
				err = s.store(ctx, craftFree+5)
			}
		}
		if errors.Is(err, errNoStorage) {
			return nil // хранения ещё нет — как есть
		}
		return err
	}
	defer func() { h.OnFull = nil }()
	// в начале — очередь исследований (stages.md, общее): лаборатории учат, пока идут подготовка
	// и стройка; лаборатории стоят со стадии 12
	if n > 12 && !s.Plan && !s.Empty {
		if err := queueResearch(ctx, h); err != nil {
			return fmt.Errorf("стадия %d: очередь исследований: %w", n, err)
		}
	}
	// перед стройкой — проверка и расчистка площади раскладки (stages.md, общее)
	if err := prepare(ctx, h, n); err != nil {
		return fmt.Errorf("стадия %d: %w", n, err)
	}
	if lim := estimate * 3 / 2; Limit > 0 && lim > Limit { // жирная стадия — время с запасом
		h.Say("лимит стадии — %v (полторы оценки %v)", lim.Round(time.Minute), estimate.Round(time.Minute))
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(base, start.Add(lim))
		defer cancel()
	}
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
