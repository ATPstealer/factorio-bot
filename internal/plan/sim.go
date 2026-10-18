package plan

import "fmt"

// Apply — эффект примитива на состояние (симуляция без игры: для bot plan и тестов).
// Плавка в симуляции мгновенная, топливо не расходуется.
func Apply(s *State, p *Primitive) error {
	if err := apply(s, p); err != nil {
		return err
	}
	if p.Mark != "" {
		if s.Done == nil {
			s.Done = map[string]bool{}
		}
		s.Done[p.Mark] = true
	}
	simTriggers(s)
	return nil
}

// simTriggers — trigger-технологии «скрафтить N», условие которых набрано, — изучены.
func simTriggers(s *State) {
	for changed := true; changed; {
		changed = false
		for name, t := range s.Protos.Technologies {
			if s.Av.Researched[name] || t.Trigger == nil || t.Trigger.Type != "craft-item" ||
				s.Produced[t.Trigger.Item] < float64(max(t.Trigger.Count, 1)) {
				continue
			}
			ok := true
			for _, pre := range t.Prerequisites {
				ok = ok && s.Av.Researched[pre]
			}
			if ok {
				s.Av.Researched[name] = true
				for _, r := range t.Unlocks {
					s.Av.Recipes[r] = true
				}
				changed = true
			}
		}
	}
}

func produced(s *State, item string, n int) {
	if s.Produced == nil {
		s.Produced = map[string]float64{}
	}
	s.Produced[item] += float64(n)
}

func apply(s *State, p *Primitive) error {
	inv := s.Inventory
	switch p.Kind {
	case "gather":
		inv[p.Item] += p.N
		produced(s, p.Item, p.N)
	case "craft":
		return simCraft(s, p.Recipe, p.N, 0)
	case "place":
		if inv[p.Item] < 1 {
			return fmt.Errorf("sim: нет %s", p.Item)
		}
		inv[p.Item]--
		name := s.Protos.Items[p.Item].PlaceResult
		s.Owned = append(s.Owned, &Owned{Name: name, Pos: p.Pos, Role: p.Role, Inv: map[string]map[string]int{}})
	case "build":
		if inv[p.Item] < len(p.Spots) {
			return fmt.Errorf("sim: нет %s ×%d", p.Item, len(p.Spots))
		}
		inv[p.Item] -= len(p.Spots)
		name := s.Protos.Items[p.Item].PlaceResult
		for i, pos := range p.Spots {
			s.Owned = append(s.Owned, &Owned{Name: name, Pos: pos, Role: p.Roles[i], Inv: map[string]map[string]int{}})
		}
	case "insert":
		o := s.Role(p.Role)
		if o == nil || inv[p.Item] < p.N {
			return fmt.Errorf("sim: положить %s ×%d в %s", p.Item, p.N, p.Role)
		}
		inv[p.Item] -= p.N
		slot := "crafter_input"
		if s.Protos.Entities[o.Name].Type == "lab" {
			slot = "lab_input"
		}
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
		slots := []string{"crafter_output", "chest"}
		if _, ore, ok := moduleOf(o.Role); ok && ore == "coal" {
			slots = append(slots, "fuel")
			o.Status = "working" // забрали всё, а горящий уголь догорает — пара работает
		}
		for _, slot := range slots {
			for it, n := range o.Inv[slot] {
				inv[it] += n
			}
			delete(o.Inv, slot)
		}
	case "wait":
		o := s.Role(p.Role)
		if o == nil {
			return fmt.Errorf("sim: нет %s", p.Role)
		}
		if _, ore, ok := moduleOf(o.Role); ok {
			// добывающая точка: накопилось сколько ждали (пластины в печи, камень в
			// сундуке, уголь в топливе пары)
			slot := "crafter_output"
			switch {
			case ore == "coal":
				slot = "fuel"
			case o.Name == Chest:
				slot = "chest"
			}
			if o.Inv[slot] == nil {
				o.Inv[slot] = map[string]int{}
			}
			o.Inv[slot][p.Item] += p.N
			produced(s, p.Item, p.N)
			return nil
		}
		if o.Name == Chest {
			// сундук бура: накопилось сколько ждали
			if o.Inv["chest"] == nil {
				o.Inv["chest"] = map[string]int{}
			}
			o.Inv["chest"][p.Item] += p.N
			return nil
		}
		// печь: переплавить всё, что лежит во входе
		for ore, n := range o.Inv["crafter_input"] {
			for _, r := range s.Protos.RecipesFor(p.Item) {
				rec := s.Protos.Recipes[r]
				if rec.Category == "smelting" && len(rec.Ingredients) == 1 && rec.Ingredients[0].Name == ore {
					if o.Inv["crafter_output"] == nil {
						o.Inv["crafter_output"] = map[string]int{}
					}
					k := n * int(productAmount(rec, p.Item)) / int(rec.Ingredients[0].Amount)
					o.Inv["crafter_output"][p.Item] += k
					produced(s, p.Item, k)
					delete(o.Inv["crafter_input"], ore)
				}
			}
		}
	case "rotate":
		if o := s.Role(p.Role); o != nil {
			d := p.Dir
			o.Dir = &d
		}
	case "research":
		if s.Researching == nil {
			s.Researching = map[string]bool{}
		}
		s.Researching[p.Item] = true
	case "await":
		// исследование идёт, пока в лабораториях есть наука: в симуляции — сразу готово
		t := s.Protos.Technologies[p.Item]
		for _, o := range s.Owned {
			if s.Protos.Entities[o.Name].Type == "lab" {
				delete(o.Inv, "lab_input")
			}
		}
		s.Av.Researched[p.Item] = true
		for _, r := range t.Unlocks {
			s.Av.Recipes[r] = true
		}
		delete(s.Researching, p.Item)
	case "explore":
		if s.Found == nil {
			s.Found = map[string]bool{}
		}
		s.Found[p.Item] = true
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

// simCraft — крафт с промежуточными: недостающие ингредиенты крафтятся из того, что есть
// (как в игре), базовое сырьё должно быть в инвентаре.
func simCraft(s *State, recipe string, n int, depth int) error {
	rec, ok := s.Protos.Recipes[recipe]
	if !ok || depth > 10 {
		return fmt.Errorf("sim: нет рецепта %s", recipe)
	}
	inv := s.Inventory
	for _, in := range rec.Ingredients {
		need := int(float64(n)*in.Mean() + 0.999)
		if lack := need - inv[in.Name]; lack > 0 {
			r, ok := s.craftRecipe(in.Name)
			if !ok {
				return fmt.Errorf("sim: крафт %s: не хватает %s (%d < %d)", recipe, in.Name, inv[in.Name], need)
			}
			if err := simCraft(s, r, ceilDiv(lack, productAmount(s.Protos.Recipes[r], in.Name)), depth+1); err != nil {
				return err
			}
		}
		inv[in.Name] -= need
	}
	for _, pr := range rec.Products {
		inv[pr.Name] += int(float64(n) * pr.Mean())
		produced(s, pr.Name, int(float64(n)*pr.Mean()))
	}
	return nil
}
