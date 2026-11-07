package stage

import (
	"slices"
	"testing"

	"factorio-bot/internal/cluster"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
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

// Месторождения мимо (planBase): у стоящего блока — его зона; не встал — зоны его сырья с местами
// под запретом, таких нет — все зоны его сырья; не добыча — ничего.
func TestMineSkip(t *testing.T) {
	r := func(x, y, w, h int) cluster.Rect {
		return cluster.Rect{Min: world.Tile{X: x, Y: y}, Max: world.Tile{X: x + w, Y: y + h}}
	}
	zones := []cluster.Zone{
		{Kind: cluster.ZoneMine, Rect: r(0, 0, 10, 10), Resources: map[string]float64{"iron-ore": 1e6}},
		{Kind: cluster.ZoneMine, Rect: r(100, 0, 10, 10), Resources: map[string]float64{"iron-ore": 1e6}},
		{Kind: cluster.ZoneMine, Rect: r(200, 0, 10, 10), Resources: map[string]float64{"coal": 1e6}},
		{Kind: cluster.ZoneBus, Rect: r(300, 0, 10, 10)},
	}
	iron := &cluster.Cluster{Kind: cluster.Mine, Product: "iron-ore"}
	ns := &cluster.NoSiteError{Cluster: iron}
	if got := mineSkip(zones, ns, map[world.Tile]bool{{X: 105, Y: 5}: true, {X: 205, Y: 5}: true}); !slices.Equal(got, []cluster.Rect{r(100, 0, 10, 10)}) {
		t.Errorf("с запретом: %v", got)
	}
	if got := mineSkip(zones, ns, nil); !slices.Equal(got, []cluster.Rect{r(0, 0, 10, 10), r(100, 0, 10, 10)}) {
		t.Errorf("без запрета: %v", got)
	}
	placed := r(1, 1, 5, 5)
	iron.Zone = &placed
	if got := mineSkip(zones, ns, nil); !slices.Equal(got, []cluster.Rect{placed}) {
		t.Errorf("стоящий: %v", got)
	}
	if got := mineSkip(zones, &cluster.NoSiteError{Cluster: &cluster.Cluster{Kind: cluster.Make}}, nil); got != nil {
		t.Errorf("не добыча: %v", got)
	}
	if got := mineSkip(zones, nil, nil); got != nil {
		t.Errorf("нет ошибки: %v", got)
	}
}
