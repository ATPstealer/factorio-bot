package stage

import (
	"testing"

	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Столбы трасс нефти в сохранённом плане (стадия 34 на 1098794301): у трубы и над промежутком
// подземной пары — остаются, диагональный вдали от трубы — убран; чужие столбы — как есть.
func TestDropStrayPoles(t *testing.T) {
	tag := cluster.LogisticsTag + "/oil-2"
	pl := func(name, tag string, x, y int) layout.Placement {
		return layout.Placement{Name: name, Pos: world.Tile{X: x, Y: y}.Center(), Tag: tag}
	}
	list := []layout.Placement{
		pl("pipe", tag, 0, 0),
		pl("pipe-to-ground", tag, 1, 0),
		pl("pipe-to-ground", tag, 9, 0),
		pl("small-electric-pole", tag, 0, 1),  // у трубы
		pl("small-electric-pole", tag, 5, -1), // над промежутком подземной пары
		pl("small-electric-pole", tag, 20, 20),
		pl("small-electric-pole", cluster.LogisticsTag, 40, 40),
	}
	got := dropStrayPoles(list)
	if len(got) != len(list)-1 {
		t.Fatalf("осталось %d из %d, ждали без одного: %v", len(got), len(list), got)
	}
	for _, p := range got {
		if world.TileOf(p.Pos) == (world.Tile{X: 20, Y: 20}) {
			t.Errorf("диагональный столб (20, 20) не убран")
		}
	}
}
