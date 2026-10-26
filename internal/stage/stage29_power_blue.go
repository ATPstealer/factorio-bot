package stage

import (
	"context"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 29 (stages.md): энергия под синюю науку — первым делом (решение пользователя): всё, что
// стадия 7 дописала в станции, — бойлеры и двигатели дальше по ряду первой станции, новые станции
// у воды (помпа, трубы, бойлеры, двигатели, отвод угля); новые бойлеры при запуске — углём руками;
// столбы — к сети базы.

const blueAddedFile = "state-power-blue.json" // раскладка стадии: один раз на карту

// boilerFuel — угля в каждый новый бойлер при запуске (как на стадии 11).
const boilerFuel = 50

func init() {
	register(Stage{N: 29, Name: "энергия под синюю науку", Run: runPowerBlue, Check: checkPowerBlue})
}

// blueAdded — дописанное стадией 7 (весь план минус план до неё) и весь план.
func blueAdded() ([]layout.Placement, *ClusterPlan, error) {
	plan, err := LoadBluePlan()
	if err != nil {
		return nil, nil, err
	}
	base, err := loadPlanFile(runPath(blueBaseFile))
	if err != nil {
		return nil, nil, fmt.Errorf("нет плана до стадии 7: %w", err)
	}
	was := map[layout.Placement]bool{}
	for _, pl := range base.Layout {
		was[pl] = true
	}
	var res []layout.Placement
	for _, pl := range plan.Layout {
		if !was[pl] {
			res = append(res, pl)
		}
	}
	return res, plan, nil
}

// powerBlueLayout — дописанное в станции и новые станции с их логистикой (вода, отвод угля),
// столбы к сети базы; один раз на карту.
func powerBlueLayout(h *act.Hands) ([]layout.Placement, error) {
	return blueAddedFor(h, blueAddedFile, func(c *cluster.Cluster) bool { return c.Kind == cluster.Power }, false)
}

func runPowerBlue(ctx context.Context, h *act.Hands) error {
	list, err := powerBlueLayout(h)
	if err != nil {
		return err
	}
	h.Say("энергия: %d построек по плану стадии 7", len(list))
	order := []string{"offshore-pump", "pipe-to-ground", "pipe", "boiler", "steam-engine", chest, "burner-inserter",
		"inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	// запуск: каждый новый бойлер — углём руками (дальше — с ленты)
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites, useStore: true}
	for _, pl := range list {
		if pl.Name != "boiler" {
			continue
		}
		st, err := h.B.GetEntity("boiler", pl.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет бойлера в (%.1f, %.1f): %v", pl.Pos.X, pl.Pos.Y, err)
		}
		if fuel := st.Inventories["fuel"]["coal"]; fuel < boilerFuel {
			if err := s.have(ctx, "coal", boilerFuel-fuel); err != nil {
				return err
			}
			h.Say("уголь в новый бойлер (%.0f, %.0f)", pl.Pos.X, pl.Pos.Y)
			if err := h.Insert(ctx, st.Entity, "coal", boilerFuel-fuel); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkPowerBlue — всё из раскладки стоит; у новых бойлеров есть уголь; столбы — в сети базы.
func checkPowerBlue(h *act.Hands) error {
	list, err := powerBlueLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	net := 0
	for _, pl := range list {
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		switch pl.Name {
		case "boiler":
			if st.Inventories["fuel"]["coal"] == 0 {
				return fmt.Errorf("бойлер (%.1f, %.1f) без угля", pl.Pos.X, pl.Pos.Y)
			}
			if st.Fluids["water"] == 0 { // по факту (stages.md): без воды станция бесполезна — жёстко
				return fmt.Errorf("бойлер (%.1f, %.1f) без воды", pl.Pos.X, pl.Pos.Y)
			}
		case "small-electric-pole":
			if st.Network == 0 {
				return fmt.Errorf("столб (%.1f, %.1f) без сети", pl.Pos.X, pl.Pos.Y)
			}
			if net == 0 {
				net = st.Network
			} else if st.Network != net {
				return fmt.Errorf("столбы станций — в разных сетях (%d и %d)", net, st.Network)
			}
		}
	}
	return nil
}
