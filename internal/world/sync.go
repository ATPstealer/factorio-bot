package world

import (
	"sort"

	"factorio-bot/internal/bridge"
)

const scanBatch = 16 // чанков за один RCON-запрос (плотный чанк руды ≈ 50 КБ)

// Sync догоняет состояние игры: сканирует новые открытые чанки и чанки, где по событиям
// мода что-то изменилось (добыто, построено, разрушено, открыто). Возвращает число
// пересканированных чанков. full — пересканировать всё (остатки руды меняются без событий).
func (w *World) Sync(b *bridge.Bridge, full bool) (int, error) {
	if b == nil { // без игры (bin/bot rough): карта загружена из файла
		return 0, nil
	}
	dirty := map[bridge.ChunkPos]bool{}

	events, seq, err := b.PollEvents(w.EventSeq)
	if err != nil {
		return 0, err
	}
	for _, e := range events {
		switch {
		case e.Chunk != nil:
			dirty[*e.Chunk] = true
		case e.Entity != nil:
			dirty[ChunkOf(TileOf(e.Entity.Position))] = true
		}
	}

	charted, err := b.ListCharted()
	if err != nil {
		return 0, err
	}
	open := map[bridge.ChunkPos]bool{}
	for _, c := range charted {
		open[c] = true
		if full || w.Chunks[c] == nil {
			dirty[c] = true
		}
	}
	for c := range w.Chunks {
		if !open[c] {
			delete(w.Chunks, c)
		}
	}

	list := make([]bridge.ChunkPos, 0, len(dirty))
	for c := range dirty {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Y != list[j].Y {
			return list[i].Y < list[j].Y
		}
		return list[i].X < list[j].X
	})
	for i := 0; i < len(list); i += scanBatch {
		scans, err := b.ScanChunks(list[i:min(i+scanBatch, len(list))])
		if err != nil {
			return 0, err
		}
		w.Apply(scans)
	}
	w.EventSeq = seq
	return len(list), nil
}
