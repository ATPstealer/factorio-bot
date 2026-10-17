package plan

import "fmt"

// Apply — эффект примитива на состояние (симуляция без игры: для bot plan и тестов).
// Плавка в симуляции мгновенная, топливо не расходуется.
func Apply(s *State, p *Primitive) error {
	inv := s.Inventory
	switch p.Kind {
	case "gather":
		inv[p.Item] += p.N
	case "craft":
		rec, ok := s.Protos.Recipes[p.Recipe]
		if !ok {
			return fmt.Errorf("sim: нет рецепта %s", p.Recipe)
		}
		for _, in := range rec.Ingredients {
			need := int(float64(p.N)*in.Mean() + 0.999)
			if inv[in.Name] < need {
				return fmt.Errorf("sim: крафт %s: не хватает %s (%d < %d)", p.Recipe, in.Name, inv[in.Name], need)
			}
			inv[in.Name] -= need
		}
		for _, pr := range rec.Products {
			inv[pr.Name] += int(float64(p.N) * pr.Mean())
		}
	case "place":
		if inv[p.Item] < 1 {
			return fmt.Errorf("sim: нет %s", p.Item)
		}
		inv[p.Item]--
		name := s.Protos.Items[p.Item].PlaceResult
		s.Owned = append(s.Owned, &Owned{Name: name, Pos: p.Pos, Role: p.Role, Inv: map[string]map[string]int{}})
	case "insert":
		o := s.Role(p.Role)
		if o == nil || inv[p.Item] < p.N {
			return fmt.Errorf("sim: положить %s ×%d в %s", p.Item, p.N, p.Role)
		}
		inv[p.Item] -= p.N
		slot := "crafter_input"
		if it := s.Protos.Items[p.Item]; it.FuelValue > 0 {
			if e := s.Protos.Entities[o.Name]; e.Burner != nil {
				slot = "fuel"
			}
		}
		if o.Inv[slot] == nil {
			o.Inv[slot] = map[string]int{}
		}
		o.Inv[slot][p.Item] += p.N
	case "take":
		o := s.Role(p.Role)
		if o == nil {
			return fmt.Errorf("sim: нет %s", p.Role)
		}
		for _, slot := range []string{"crafter_output", "chest"} {
			for it, n := range o.Inv[slot] {
				inv[it] += n
			}
			delete(o.Inv, slot)
		}
	case "wait":
		// переплавить всё, что лежит во входе печи
		o := s.Role(p.Role)
		if o == nil {
			return fmt.Errorf("sim: нет %s", p.Role)
		}
		for ore, n := range o.Inv["crafter_input"] {
			for _, r := range s.Protos.RecipesFor(p.Item) {
				rec := s.Protos.Recipes[r]
				if rec.Category == "smelting" && len(rec.Ingredients) == 1 && rec.Ingredients[0].Name == ore {
					if o.Inv["crafter_output"] == nil {
						o.Inv["crafter_output"] = map[string]int{}
					}
					o.Inv["crafter_output"][p.Item] += n * int(productAmount(rec, p.Item)) / int(rec.Ingredients[0].Amount)
					delete(o.Inv["crafter_input"], ore)
				}
			}
		}
	default:
		return fmt.Errorf("sim: неизвестный шаг %s", p.Kind)
	}
	return nil
}

// Simulate — шаги до выполнения целей (по порядку приоритета) на копии состояния.
func Simulate(pl *Planner, s *State, goals []Goal, maxSteps int) ([]*Primitive, error) {
	s = s.Clone()
	var steps []*Primitive
	for len(steps) < maxSteps {
		var prim *Primitive
		for _, g := range goals {
			p, err := pl.Next(s, g)
			if err != nil {
				return steps, err
			}
			if p != nil {
				prim = p
				break
			}
		}
		if prim == nil {
			return steps, nil
		}
		if err := Apply(s, prim); err != nil {
			return steps, err
		}
		steps = append(steps, prim)
	}
	return steps, fmt.Errorf("plan: больше %d шагов — похоже на зацикливание", maxSteps)
}
