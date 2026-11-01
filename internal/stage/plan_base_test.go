package stage

import (
	"slices"
	"testing"

	"factorio-bot/internal/cluster"
	"factorio-bot/internal/knowledge"
)

// Порядок кластеров плана: поставщик каждого входа — раньше потребителя (PlaceZoned ставит слева
// направо), смазка — раньше крекинга тяжёлого, газ всех поставщиков — раньше пластика и серы.
func TestPlanClustersOrder(t *testing.T) {
	pr, err := knowledge.Load("../knowledge/testdata/prototypes.json")
	if err != nil {
		t.Fatal(err)
	}
	cs := planClusters(pr)
	at := map[*cluster.Cluster]int{}
	for i, c := range cs {
		at[c] = i
	}
	for _, c := range cs {
		for _, in := range c.In {
			for _, o := range cs {
				if o != c && slices.ContainsFunc(o.Out, func(f cluster.Flow) bool { return f.Item == in.Item }) && at[o] > at[c] {
					t.Errorf("%s (вход %s) — раньше своего поставщика %s", c.Product, in.Item, o.Product)
				}
			}
		}
	}
	idx := func(product string) int {
		return slices.IndexFunc(cs, func(c *cluster.Cluster) bool { return c.Product == product })
	}
	if idx("lubricant") > idx("light-oil") {
		t.Error("крекинг тяжёлого — раньше смазки (смазка — выше по трубе тяжёлого)")
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Product)
	}
	t.Logf("порядок: %v", names)
}
