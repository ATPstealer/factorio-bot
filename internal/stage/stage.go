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
	"sync/atomic"
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

// crossCache — входы буферов на чужую полосу по плану (planCross): план — ~10 тыс. построек,
// считать один раз на файл плана.
var crossCache struct {
	key string
	set map[layout.Placement]bool
}

// planCross — crossBuffers плана стройки (как сохранён); плана нет — пусто.
func planCross() map[layout.Placement]bool {
	fi, err := os.Stat(runPath(planNowFile()))
	if err != nil {
		return nil
	}
	key := fmt.Sprint(runPath(planNowFile()), fi.ModTime().UnixNano(), fi.Size())
	if crossCache.key != key {
		raw, err := rawPlan()
		if err != nil {
			return nil
		}
		crossCache.key, crossCache.set = key, crossBuffers(raw.Layout)
	}
	return crossCache.set
}

// onceLayout — раскладка стадии один раз на карту: есть файл file — из него, нет — build и в
// файл (стройка, проверка и перезапуск стадии видят одну и ту же раскладку); freshLayouts —
// заново и в файл.
func onceLayout(file string, build func() ([]layout.Placement, error)) ([]layout.Placement, error) {
	if data, err := os.ReadFile(runPath(file)); err == nil && !freshLayouts {
		var list []layout.Placement
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			// посчитанная до правила о буферах на чужую полосу — без их входов (стадия 27 на
			// 260647970: проверка ждала снятый вход буфера зелёного)
			if cross := planCross(); len(cross) > 0 {
				list = slices.DeleteFunc(list, func(pl layout.Placement) bool { return cross[pl] })
			}
			return uniq(list), nil
		}
	}
	list, err := build()
	if err != nil {
		return nil, err
	}
	list = uniq(list)
	return list, writeState(file, list)
}

// stateLayouts — раскладки из состояния (state-*.json, что читается как список построек; зоны,
// точки стадии 10 и т. п. — нет), в порядке имён файлов: имя файла → раскладка.
func stateLayouts() ([]string, map[string][]layout.Placement) {
	files, _ := filepath.Glob(runPath("state-*.json"))
	var names []string
	res := map[string][]layout.Placement{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var list []layout.Placement
		if json.Unmarshal(data, &list) != nil {
			continue // не раскладка
		}
		base := filepath.Base(f)
		names = append(names, base)
		res[base] = list
	}
	return names, res
}

// uniq — без повторов одного места (порядок — первого появления): вторую такую же постройку игра
// не ставит (лента камня стадии 30 — дважды, 859088572).
func uniq(list []layout.Placement) []layout.Placement {
	seen := map[layout.Placement]bool{}
	res := list[:0:0]
	for _, pl := range list {
		if !seen[pl] {
			seen[pl] = true
			res = append(res, pl)
		}
	}
	return res
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

// estimate — оценка времени стройки текущей стадии, нс (prepTime); лимит Do растягивается сразу,
// как она известна, — и посреди подготовки (расчистка стадии 30 — 1400 деревьев, часы).
var estimate atomic.Int64

// Do — выполнить стадию и проверить критерии.
func Do(ctx context.Context, h *act.Hands, n int) error {
	s, err := Get(n)
	if err != nil {
		return err
	}
	start := time.Now()
	estimate.Store(0)
	if Limit > 0 { // лимит — Limit или полторы оценки, что больше (оценка — по ходу подготовки)
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(ctx)
		defer cancel(nil)
		go func() {
			for {
				lim := max(Limit, time.Duration(estimate.Load())*3/2)
				left := time.Until(start.Add(lim))
				if left <= 0 {
					h.Log("лимит стадии вышел (%v)", lim.Round(time.Minute))
					cancel(context.DeadlineExceeded)
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(min(left, time.Minute)):
				}
			}
		}()
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
		// набираемое сейчас (have: gathering) — при себе, иначе круг «взял — выложил — взял»
		// (стадия 29 на 427965634: уголь на бойлеры)
		var mine []string
		if gathering != "" {
			mine = []string{gathering}
		}
		err := s.store(ctx, craftFree+5, mine...)
		if err == nil {
			p, perr := h.B.Player()
			if perr != nil || p.InventorySize-len(p.InventorySlots) >= craftFree {
				return perr
			}
			bulk := map[string]bool{"iron-plate": true, "copper-plate": true, "steel-plate": true, "stone-brick": true, "coal": true}
			keep := append([]string{}, mine...)
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
				err = s.store(ctx, craftFree+5, mine...)
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
		// сеть базы одна (stages.md, общее): острова — подключить
		if err := healPower(ctx, h); err != nil { // не вышло — сказать и дальше (мягко)
			if ctx.Err() != nil {
				return err
			}
			h.Say("сеть: острова не подключить: %v", err)
		}
		// уголь станции не кончается (stages.md, общее): голодает — подвод с другой добычи
		if err := healCoal(ctx, h); err != nil {
			if ctx.Err() != nil {
				return err
			}
			h.Say("уголь станции: подвод не вышел: %v", err)
		}
		// чужая жидкость в сети складов — слить (stages.md, общее)
		if err := healFluids(ctx, h); err != nil {
			if ctx.Err() != nil {
				return err
			}
			h.Say("слив чужой жидкости не вышел: %v", err)
		}
		// чужой предмет на ленте — убрать (stages.md, общее)
		if err := healBelts(ctx, h); err != nil {
			if ctx.Err() != nil {
				return err
			}
			h.Say("чужое с лент не убралось: %v", err)
		}
		// буферов входа у лабораторий нет (cluster.md §6): стоящие — снять
		if err := dropLabBuffers(ctx, h); err != nil {
			if ctx.Err() != nil {
				return err
			}
			h.Say("буферы лабораторий не снялись: %v", err)
		}
	}
	// перед стройкой — проверка и расчистка площади раскладки (stages.md, общее)
	if err := prepare(ctx, h, n); err != nil {
		return fmt.Errorf("стадия %d: %w", n, err)
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
