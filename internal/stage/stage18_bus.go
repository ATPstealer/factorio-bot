package stage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
)

// Стадия 18 (stages.md): пластины железа и меди с плавки — в их ленты шины (по плану стадии 3).
// Больше ничего.

const busPlatesFile = "state-bus-plates.json"

func init() {
	register(Stage{N: 18, Name: "пластины на шину", Run: runBusPlates, Check: checkBusPlates})
}

// busPlatesLayout — busPlatesBase; один раз на карту (файл).
func busPlatesLayout(h *act.Hands) ([]layout.Placement, error) {
	return onceLayout(busPlatesFile, func() ([]layout.Placement, error) {
		plan, err := LoadPlan()
		if err != nil {
			return nil, err
		}
		res := busPlatesBase(h, plan)
		if len(res) == 0 {
			return nil, fmt.Errorf("в плане нет лент пластин шины")
		}
		return res, nil
	})
}

func runBusPlates(ctx context.Context, h *act.Hands) error {
	list, err := busPlatesLayout(h)
	if err != nil {
		return err
	}
	if err := buildSorted(ctx, h, list, []string{"transport-belt", "underground-belt", "splitter"}); err != nil {
		return err
	}
	// пластины дошли до концов своих лент
	deadline := time.Now().Add(8 * time.Minute)
	for checkBusPlates(h) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// checkBusPlates — всё стоит; на последней ленте каждой ленты пластин шины — её пластины.
func checkBusPlates(h *act.Hands) error {
	list, err := busPlatesLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	for _, it := range []string{"iron-plate", "copper-plate"} {
		// своя шина пластин — та, куда подводит плавка; конец её ленты
		bus := ""
		for _, pl := range plan.Layout {
			for _, b := range busNames(plan) {
				if strings.HasPrefix(pl.Tag, cluster.LogisticsTag+"/"+b+"/") && clusterOfName(plan, strings.TrimPrefix(pl.Tag, cluster.LogisticsTag+"/"+b+"/")).Product == it {
					bus = b
				}
			}
		}
		// конец ленты — самая дальняя по течению (шина течёт на восток), а не последняя в плане:
		// голову за неровностью план добавляет в конец (859088572: проверялась голова)
		var last *layout.Placement
		for i, pl := range plan.Layout {
			if bus != "" && pl.Tag == cluster.Tag(bus) && pl.Recipe == it && pl.Name == "transport-belt" && (last == nil || pl.Pos.X > last.Pos.X) {
				last = &plan.Layout[i]
			}
		}
		if last == nil {
			return fmt.Errorf("в плане нет ленты %s шины", it)
		}
		st, err := h.B.GetEntity(last.Name, last.Pos)
		if err != nil || st == nil {
			return fmt.Errorf("нет конца ленты %s в (%.1f, %.1f): %v", it, last.Pos.X, last.Pos.Y, err)
		}
		if st.Belt[it] == 0 {
			return fmt.Errorf("%s не дошли до конца ленты шины (%.1f, %.1f)", it, last.Pos.X, last.Pos.Y)
		}
	}
	return nil
}
