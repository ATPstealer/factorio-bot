package act

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"factorio-bot/internal/bridge"
)

// Поворот руки в игре — один на все предметы (не свой у каждого: инсертер, взятый после ленты на
// юг, встаёт на юг; R с сундуком в руке поворачивает и его; постановка сундука и поворот готовой
// постройки — нет); предмет, взятый кликом из инвентаря, — север (с панели — как была). Живёт,
// пока жив процесс клиента, — и между запусками бота (стадия 23 на
// 794457638; тесты подряд на одном клиенте). Бот помнит так же: в памяти и (HandFile) в файле с
// отметкой процесса клиента; клиент другой (игру перезапустили) — север.

type handMemo struct {
	Client string `json:"client"`
	Rot    int    `json:"rot"`
}

// hand — куда смотрит предмет в руке (при этом клиенте не поворачивали — север). Файл —
// каждый раз заново: руки экземпляра бывают не одни (второй процесс, руки «нового запуска» в
// тестах) — поворот у них общий.
func (h *Hands) hand() int {
	h.loadHand()
	var m handMemo
	if h.HandFile != "" && h.handClient != "" {
		if data, err := os.ReadFile(h.HandFile); err == nil && json.Unmarshal(data, &m) == nil && m.Client == h.handClient {
			h.handRot = m.Rot
		}
	}
	return h.handRot
}

// setHand — запомнить поворот руки.
func (h *Hands) setHand(dir int) {
	h.loadHand()
	h.handRot = dir
	if h.HandFile == "" || h.handClient == "" {
		return
	}
	if data, err := json.Marshal(handMemo{Client: h.handClient, Rot: dir}); err == nil {
		os.WriteFile(h.HandFile, data, 0o644)
	}
}

// handAfter — куда смотрит рука сразу после постановки e; false — не узнать по ней (нет
// направления; симметричные — паровой двигатель, цистерна: d и d+8 у них одно).
// Обычно — куда встала e. Подземку (трубу и ленту) игра после постановки в руке разворачивает —
// следующая встанет её парой (стадия 34 на 427965634: рука считалась смотрящей, как встала
// труба, — каждая следующая вставала не тем концом и переставлялась трижды;
// TestPlacePipeToGroundChain); выход подземной ленты при этом встаёт против руки: рука после
// входа — наоборот от него, после выхода — как он.
func (h *Hands) handAfter(e bridge.Entity) (int, bool) {
	pe := h.Protos.Entities[e.Name]
	if e.Direction == nil || pe.Type == "generator" || pe.Type == "storage-tank" {
		return 0, false
	}
	d := *e.Direction
	switch pe.Type {
	case "pipe-to-ground":
		d += 8
	case "underground-belt":
		st, err := h.B.GetEntity(e.Name, e.Position)
		if err != nil || st == nil {
			return 0, false
		}
		if st.UGType == "input" {
			d += 8
		}
	}
	return d % 16, true
}

func (h *Hands) loadHand() {
	if h.handLoaded {
		return
	}
	h.handLoaded = true
	if h.HandFile != "" {
		h.handClient = clientProc(filepath.Dir(h.HandFile))
	}
}

// clientProc — процесс клиента игры с конфигом экземпляра dir: «pid:время старта»; "" — нет.
func clientProc(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	cfg := []byte(filepath.Join(abs, "client", "config.ini"))
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid := e.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		cmd, err := os.ReadFile("/proc/" + pid + "/cmdline")
		if err != nil || !bytes.HasSuffix(bytes.SplitN(cmd, []byte{0}, 2)[0], []byte("/factorio")) || !bytes.Contains(cmd, cfg) {
			continue
		}
		st, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			continue
		}
		// после «(имя)»: состояние — поле 3, время старта — поле 22
		if f := strings.Fields(string(st[bytes.LastIndexByte(st, ')')+1:])); len(f) > 19 {
			return pid + ":" + f[19]
		}
	}
	return ""
}
