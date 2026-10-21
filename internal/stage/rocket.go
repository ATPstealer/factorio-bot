package stage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"factorio-bot/internal/act"
)

// RocketDesign — проект базы до ракеты за hours часов: все исследования до rocket-silo (по
// их цене — темп пакетов: исследования идут ~80% времени) и ракетные части (100 штук за
// последние ~20% времени).
func RocketDesign(h *act.Hands, hours float64) (Design, error) {
	techs, err := h.Protos.TechPath([]string{"rocket-silo"}, nil)
	if err != nil {
		return Design{}, err
	}
	cost := h.Protos.Cost(techs)
	research := hours * 3600 * 0.8
	targets := map[string]float64{"rocket-part": 100 / (hours * 3600 * 0.2)}
	units := 0.0
	for pack, n := range cost.Packs {
		targets[pack] = n / research
		units = math.Max(units, n)
	}
	if units == 0 {
		return Design{}, fmt.Errorf("цена исследований до rocket-silo — ноль")
	}
	return Design{Targets: targets,
		Machines: map[string]string{"smelting": "steel-furnace", "crafting": "assembling-machine-2"},
		LabSec:   cost.Seconds / units, File: runPath("design-rocket.json"),
		Image: fmt.Sprintf("run/stages/rocket-%s.png", seedOf())}, nil
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
