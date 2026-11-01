package cluster

import (
	"sync"
	"testing"

	"factorio-bot/internal/knowledge"
)

var (
	protos *knowledge.Prototypes
	once   sync.Once
)

func pt(t *testing.T) *knowledge.Prototypes {
	t.Helper()
	once.Do(func() {
		var err error
		protos, err = knowledge.Load("../knowledge/testdata/prototypes.json")
		if err != nil {
			t.Fatal(err)
		}
	})
	return protos
}
