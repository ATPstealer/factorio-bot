package stage

import (
	"context"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
)

// Стадия 15 (stages.md): разобрать точки стадии 10 (печи, буры, угольные пары, сундук) и
// сложить в кластер хранения; содержимое точек забрать (подбор отдаёт его в инвентарь),
// не помещается — в хранение (сначала ненужное).

func init() {
	register(Stage{N: 15, Name: "разбор временных точек", Run: runDismantle, Check: checkDismantle})
}

// standing — постройки точек стадии 10, которые ещё стоят.
func (s *worker) standing() ([]bridge.Entity, error) {
	var res []bridge.Entity
	for _, site := range s.sites {
		for _, pl := range site.Items {
			e, err := s.entity(pl)
			if err != nil {
				return nil, err
			}
			if e != nil {
				res = append(res, *e)
			}
		}
	}
	return res, nil
}

// strays — свои буры точек вне раскладки точек и то, во что они выдают (печь, сундук): буры на
// твёрдом топливе после стадии 15 не нужны нигде, разбираются и такие (стадия 10 на 20421397
// продолжалась с пересчитанной раскладкой — после разбора по файлу 46 буров остались, стадия 22:
// «лишнего 73 — чертёж неполный?»).
func (s *worker) strays() ([]bridge.Entity, error) {
	p, err := s.h.B.Player()
	if err != nil {
		return nil, err
	}
	in := map[bridge.Pos]bool{}
	for _, site := range s.sites {
		for _, pl := range site.Items {
			in[pl.Pos] = true
		}
	}
	planned := map[bridge.Pos]bool{} // печи и сундуки плана (колонны плавки) — не трогать
	if plan, err := LoadPlan(); err == nil {
		for _, pl := range plan.Layout {
			planned[pl.Pos] = true
		}
	}
	found, err := s.h.B.FindNearest(bridge.FindArgs{Name: drill, Radius: 3000, Position: &p.Position, Limit: 1000})
	if err != nil {
		return nil, err
	}
	var res []bridge.Entity
	seen := map[bridge.Pos]bool{}
	for _, e := range found {
		if in[e.Position] {
			continue
		}
		st, err := s.h.B.GetEntity(e.Name, e.Position)
		if err != nil || st == nil || !st.Own {
			continue
		}
		res = append(res, st.Entity)
		dir := 0
		if st.Direction != nil {
			dir = *st.Direction
		}
		out := layout.DrillOutput(s.h.Protos, layout.Placement{Name: e.Name, Pos: e.Position, Dir: dir}).Center()
		for _, name := range []string{furnace, chest} {
			near, err := s.h.B.FindNearest(bridge.FindArgs{Name: name, Radius: 1.5, Position: &out, Limit: 1})
			if err != nil || len(near) == 0 || in[near[0].Position] || planned[near[0].Position] || seen[near[0].Position] {
				continue
			}
			if t, err := s.h.B.GetEntity(name, near[0].Position); err == nil && t != nil && t.Own && dist2(t.Position, out) < 1.1 {
				seen[t.Position] = true
				res = append(res, t.Entity)
			}
		}
	}
	return res, nil
}

func runDismantle(ctx context.Context, h *act.Hands) error {
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites}
	// сначала — буры точек вне раскладки (и их печи, сундуки), по десятку: подбор отдаёт и
	// содержимое — места должно хватать
	if es, err := s.strays(); err == nil && len(es) > 0 {
		h.Say("буры точек вне раскладки: %d построек — разбираю", len(es))
		for i := 0; i < len(es); i += 10 {
			if err := s.store(ctx, 12); err != nil {
				return err
			}
			if err := h.PickupAll(ctx, es[i:min(len(es), i+10)]); err != nil {
				h.Say("разбор точек вне раскладки: %v", err)
			}
		}
	}
	for tries := 0; ; tries++ {
		es, err := s.standing()
		if err != nil {
			return err
		}
		if len(es) == 0 {
			break
		}
		if tries > 5 {
			return fmt.Errorf("не разобрать точки: стоит ещё %d", len(es))
		}
		// по одной точке: подбор отдаёт и содержимое — места в инвентаре должно хватать
		for _, site := range sites {
			var part []bridge.Entity
			for _, pl := range site.Items {
				if e, err := s.entity(pl); err == nil && e != nil {
					part = append(part, *e)
				}
			}
			if len(part) == 0 {
				continue
			}
			if err := s.store(ctx, 12); err != nil {
				return err
			}
			h.Say("разбираю точку %s: %d построек", site.Ore, len(part))
			if err := h.PickupAll(ctx, part); err != nil {
				h.Say("разбор точки %s: %v", site.Ore, err)
			}
		}
	}
	// разобранное — в хранение; печи — кроме нужных кластерам плавки (стадия 17)
	need, _, err := materialsNeed(h)
	if err != nil {
		return err
	}
	return s.stash(ctx, map[string]int{drill: s.inv(drill), chest: max(0, s.inv(chest)-need[chest]),
		furnace: max(0, s.inv(furnace)-need[furnace])})
}

// stash — из инвентаря в сундуки хранения: предмет → сколько.
func (s *worker) stash(ctx context.Context, items map[string]int) error {
	list, err := storageLayout()
	if err != nil {
		return err
	}
	k := 0
	for it, n := range items {
		for left := s.inv(it) - n; s.inv(it) > left; {
			if k >= len(list) {
				return fmt.Errorf("хранение полно: не положить %s ×%d", it, s.inv(it)-left)
			}
			e, err := s.entity(list[k])
			if err != nil {
				return err
			}
			if e == nil {
				k++
				continue
			}
			before := s.inv(it)
			if s.inv(it)-left >= before { // всё — Ctrl+клик, часть — по штуке
				_, err = s.h.InsertAll(ctx, *e, it)
			} else {
				err = s.h.Insert(ctx, *e, it, s.inv(it)-left)
			}
			if err != nil {
				return err
			}
			if s.inv(it) == before { // сундук полон — следующий
				k++
			}
		}
	}
	return nil
}

func checkDismantle(h *act.Hands) error {
	sites, err := outpostsLayout(h)
	if err != nil {
		return err
	}
	s := &worker{h: h, sites: sites}
	es, err := s.standing()
	if err != nil {
		return err
	}
	if len(es) > 0 {
		return fmt.Errorf("точки стадии 10 не разобраны: %d построек (%s в (%.1f, %.1f) …)", len(es), es[0].Name, es[0].Position.X, es[0].Position.Y)
	}
	if es, err := s.strays(); err == nil && len(es) > 0 {
		return fmt.Errorf("буры точек вне раскладки не разобраны: %d построек (%s в (%.1f, %.1f) …)", len(es), es[0].Name, es[0].Position.X, es[0].Position.Y)
	}
	stock, err := materialsStock(h)
	if err != nil {
		return err
	}
	p, err := h.B.Player()
	if err != nil {
		return err
	}
	if n := p.Inventory[drill]; n > 0 {
		return fmt.Errorf("твердотопливные буры не в хранении: в инвентаре %d", n)
	}
	drills := 0
	for _, site := range sites {
		for _, pl := range site.Items {
			if pl.Name == drill {
				drills++
			}
		}
	}
	if stock[drill] < drills {
		return fmt.Errorf("в хранении буров %d из %d", stock[drill], drills)
	}
	return nil
}
