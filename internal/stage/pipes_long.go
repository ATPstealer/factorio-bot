package stage

import (
	"context"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// fixLongPipes — подземные пары труб плана длиннее дальности (игра их не соединяет; план
// посчитан до правки pipeAlong) — обходом трубой (cluster.FixLongPipes): в плане, а у построенных
// (стадия трубы до n) — и на карте: концы снять, обход поставить (стадия 59 на 1114315936: кислота
// не доходила до процессоров).
func fixLongPipes(ctx context.Context, h *act.Hands, n int) error {
	plan, err := LoadPlan()
	if err != nil {
		return err
	}
	if _, err := h.World.Sync(h.B, false); err != nil {
		return err
	}
	l := layout.New(h.Protos, h.World)
	for _, pl := range plan.Layout {
		l.Add(pl)
	}
	pairs := cluster.LongPipes(l)
	if len(pairs) == 0 {
		return nil
	}
	st := planStages(plan)
	// стоящие концы пар — не помеха обходу (их снимут): без них в модели карты («начало закрыто»
	// на 1114315936 — конец стоял, а без игры обход находился)
	var hide []world.Object
	for _, pr := range pairs {
		for _, t := range pr {
			for _, o := range h.World.ObjectsNear(t.Center(), 0.5) {
				if o.Own && o.Name == "pipe-to-ground" && world.TileOf(o.Pos) == t {
					hide = append(hide, o)
				}
			}
		}
	}
	var removed, added []layout.Placement
	var run func(i int)
	run = func(i int) {
		if i == len(hide) {
			removed, added, err = cluster.FixLongPipes(l)
			return
		}
		h.World.Without(hide[i], func() { run(i + 1) })
	}
	run(0)
	if len(removed) > 0 {
		h.Say("план: %d подземных пар труб длиннее дальности — обход (%d построек)", len(removed)/2, len(added))
		plan.Layout, plan.Placed = l.List, len(l.List)
		if werr := writeState(planFile, plan); werr != nil {
			return werr
		}
	}
	if err != nil {
		return err
	}
	var lift []bridge.Entity
	built := false
	for _, pl := range removed {
		if st[pl] > 0 && st[pl] < n {
			built = true
			if e, gerr := h.B.EntityAt(pl.Name, pl.Pos); gerr == nil && e != nil {
				lift = append(lift, *e)
			}
		}
	}
	if !built {
		return nil
	}
	if err := h.PickupAll(ctx, lift); err != nil {
		return err
	}
	return buildSorted(ctx, h, added, []string{"pipe-to-ground", "pipe"})
}
