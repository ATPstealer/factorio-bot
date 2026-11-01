package stage

import (
	"factorio-bot/internal/layout"
)

// boilerFuel — угля в каждый новый бойлер при запуске (как на стадии 11).
const boilerFuel = 50

// blueAdded — постройки плана стадий синей науки (30–41) и весь план.
func blueAdded() ([]layout.Placement, *ClusterPlan, error) {
	plan, err := LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	return stageAdded(plan, 30, 41), plan, nil
}
