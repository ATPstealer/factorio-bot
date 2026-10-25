package world

import (
	"compress/gzip"
	"encoding/gob"
	"os"

	"factorio-bot/internal/bridge"
)

// Save — открытая карта (чанки) в файл: разметку базы можно гонять без игры (bin/bot rough).
func (w *World) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	if err := gob.NewEncoder(z).Encode(w.Chunks); err != nil {
		f.Close()
		return err
	}
	if err := z.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load — чанки из файла Save.
func (w *World) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	chunks := map[bridge.ChunkPos]*Chunk{}
	if err := gob.NewDecoder(z).Decode(&chunks); err != nil {
		return err
	}
	w.Chunks = chunks
	return nil
}
