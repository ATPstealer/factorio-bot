package stage

import (
	"testing"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Уровни построек плана по стадиям (stages.md, стадии 22 и 42): в новом плане — по стадии
// постройки (инсертер стадии 30 — быстрый, сборщик стадии 30 — первого уровня, сборщик стадии 44 —
// второго); после стадии 42 — всё прошлое второго уровня; длинный инсертер — как есть.
func TestPlanTiers(t *testing.T) {
	pl := func(name, tag string, x float64) layout.Placement {
		return layout.Placement{Name: name, Pos: bridge.Pos{X: x, Y: 0.5}, Tag: tag}
	}
	plan := &ClusterPlan{
		Clusters: []*cluster.Cluster{
			{Name: "red", Kind: cluster.Make, Stage: 20},
			{Name: "belt", Kind: cluster.Make, Stage: 30},
			{Name: "rail", Kind: cluster.Make, Stage: 44},
		},
		Layout: []layout.Placement{
			pl("inserter", "c/red", 0.5),
			pl("inserter", "c/belt", 1.5),
			pl("assembling-machine-1", "c/belt", 3.5),
			pl("assembling-machine-1", "c/rail", 7.5),
			pl("long-handed-inserter", "c/belt", 9.5),
		},
	}
	names := func() []string {
		var res []string
		for _, p := range plan.Layout {
			res = append(res, p.Name)
		}
		return res
	}
	want := func(step string, w ...string) {
		t.Helper()
		got := names()
		for i := range w {
			if got[i] != w[i] {
				t.Errorf("%s: %v, ждал %v", step, got, w)
				return
			}
		}
	}
	planTiers(plan, 0)
	want("новый план", "inserter", "fast-inserter", "assembling-machine-1", "assembling-machine-2", "long-handed-inserter")
	planTiers(plan, cluster.FastFrom)
	want("после 22", "fast-inserter", "fast-inserter", "assembling-machine-1", "assembling-machine-2", "long-handed-inserter")
	planTiers(plan, cluster.Tier2From)
	want("после 42", "fast-inserter", "fast-inserter", "assembling-machine-2", "assembling-machine-2", "long-handed-inserter")
}
