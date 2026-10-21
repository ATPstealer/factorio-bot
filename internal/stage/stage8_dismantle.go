package stage

import (
	"context"
	"fmt"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
)

// Стадия 8 (stages.md): разобрать точки стадии 2 (печи, буры, угольные пары, сундук) и
// сложить в кластер хранения; содержимое точек забрать (подбор отдаёт его в инвентарь),
// не помещается — в хранение (сначала ненужное).

func init() {
	register(Stage{N: 8, Name: "разбор временных точек", Run: runDismantle, Check: checkDismantle})
}

// s8Standing — постройки точек стадии 2, которые ещё стоят.
func (s *s2) standing() ([]bridge.Entity, error) {
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

func runDismantle(ctx context.Context, h *act.Hands) error {
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites}
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
	// разобранное — в хранение; печи — кроме нужных кластерам плавки (стадия 10)
	need, err := s7Need(h)
	if err != nil {
		return err
	}
	return s.stash(ctx, map[string]int{drill: s.inv(drill), chest: max(0, s.inv(chest)-need[chest]),
		furnace: max(0, s.inv(furnace)-need[furnace])})
}

// stash — из инвентаря в сундуки хранения: предмет → сколько.
func (s *s2) stash(ctx context.Context, items map[string]int) error {
	list, err := s6Layout()
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
	sites, err := s2Layout(h)
	if err != nil {
		return err
	}
	s := &s2{h: h, sites: sites}
	es, err := s.standing()
	if err != nil {
		return err
	}
	if len(es) > 0 {
		return fmt.Errorf("точки стадии 2 не разобраны: %d построек (%s в (%.1f, %.1f) …)", len(es), es[0].Name, es[0].Position.X, es[0].Position.Y)
	}
	stock, err := s7Stock(h)
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

// fromStorage — item лежит в сундуке хранения: забрать содержимое этого сундука (Ctrl+клик
// берёт всё, что влезет). false — в хранении нет.
func (s *s2) fromStorage(ctx context.Context, item string) (bool, error) {
	list, err := s6Layout()
	if err != nil {
		return false, err
	}
	for _, pl := range list {
		st, err := s.h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil || st.Inventories["chest"][item] == 0 {
			continue
		}
		before := s.inv(item)
		if _, err := s.h.Take(ctx, st.Entity); err != nil {
			return false, err
		}
		return s.inv(item) > before, nil
	}
	return false, nil
}
