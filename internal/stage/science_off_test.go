package stage

import (
	"strings"
	"testing"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/knowledge"
)

// Отключение науки на ракету — какие ленты снимать (stages.md, общее): у кластера пакета на каждом
// входе с шины — одна, первая после делителя отвода, лента или подземка его подвода. По плану
// прогона 2–61 (снимок last_run; нет — пропуск).
func TestScienceTaps(t *testing.T) {
	plan, err := LoadPlanFile("../../run/snapshots/last_run/2448886157-61/state-plan.json")
	if err != nil {
		t.Skip("нет плана прогона:", err)
	}
	p, err := knowledge.Load("../knowledge/testdata/prototypes.json")
	if err != nil {
		t.Fatal(err)
	}
	h := &act.Hands{Protos: p}
	for _, pack := range sciencePacks {
		inputs := 0
		for _, c := range plan.Clusters {
			if c.Kind == cluster.Make && c.Product == pack {
				inputs += len(c.In)
			}
		}
		taps := scienceTaps(h, plan, pack)
		t.Logf("%s: входов %d, снять %d", pack, inputs, len(taps))
		if len(taps) != inputs {
			t.Errorf("%s: снять %d, а входов с шины %d", pack, len(taps), inputs)
		}
		for _, pl := range taps {
			if !strings.HasSuffix(pl.Name, "transport-belt") && !strings.HasSuffix(pl.Name, "underground-belt") ||
				!strings.HasPrefix(pl.Tag, cluster.LogisticsTag+"/") {
				t.Errorf("%s: снимается %s (%s) — не лента подвода", pack, pl.Name, pl.Tag)
			}
		}
	}
}
