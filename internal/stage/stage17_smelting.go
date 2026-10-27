package stage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

// Стадия 17 (stages.md): кластеры добычи и плавки железа и меди по плану стадии 3 с буферами
// входа; руда — в ленты шины, плавка берёт руду и уголь с шины, пластины пока не на шину
// (стадия 18). Буферы выхода (cluster.md 6а) — инсертер с фильтром на продукт берёт с выходной
// ленты плавки в сундук: пластины для крафта и стройки бота.

const smeltingFile = "state-smelting.json"

func init() {
	register(Stage{N: 17, Name: "добыча и плавка железа и меди", Run: runSmelting, Check: checkSmelting})
}

// smeltingLayout — кластеры и подводы к плавке из плана, буферы выхода плавки (один раз на карту).
func smeltingLayout(h *act.Hands) ([]layout.Placement, error) {
	return onceLayout(smeltingFile, func() ([]layout.Placement, error) {
		plan, err := LoadPlan()
		if err != nil {
			return nil, err
		}
		res := smeltingBase(h, plan)
		var smelt []*cluster.Cluster
		var allow []string
		for _, c := range plan.Clusters {
			switch {
			case c.Kind == cluster.Smelt && (c.Product == "iron-plate" || c.Product == "copper-plate"):
				smelt = append(smelt, c)
				allow = append(allow, c.Name)
			case c.Kind == cluster.Mine && (c.Product == "iron-ore" || c.Product == "copper-ore" || c.Product == "coal"):
				allow = append(allow, c.Name)
			}
		}
		all := layout.New(h.Protos, h.World)
		for _, pl := range plan.Layout {
			all.Add(pl)
		}
		for _, c := range smelt {
			buf, err := outBuffer(h, all, c, 1)
			if err != nil {
				return nil, err
			}
			res = append(res, buf...)
		}
		poles, err := connectPower(h, plan, res, allow)
		return append(res, poles...), err
	})
}

// outBuffer — буфер выхода кластера c: сбоку от выходной ленты (от порта назад) pairs пар
// «инсертер с фильтром на продукт — сундук»; места — свободные от плана. Инсертер берёт с
// ленты всё, что успевает (~0.8 шт/с): у сборщика с выходом ~1 шт/с два инсертера забирали
// всё — на шину не шло ничего (стадия 19).
func outBuffer(h *act.Hands, all *layout.Layout, c *cluster.Cluster, pairs int) ([]layout.Placement, error) {
	var out *cluster.Port
	for i := range c.Ports {
		if !c.Ports[i].In {
			out = &c.Ports[i]
		}
	}
	if out == nil {
		return nil, fmt.Errorf("у %s нет порта выхода", c.Name)
	}
	tag := "outbuf/" + c.Name
	future := planTaken(h)
	var res []layout.Placement
	t := layout.Step(out.T, (out.Dir+8)%16)
	for k := 0; k < 12 && len(res) < 2*pairs; k++ {
		b := all.At(t)
		if b == nil || b.Name != "transport-belt" {
			break
		}
		for _, side := range []int{(b.Dir + 4) % 16, (b.Dir + 12) % 16} {
			ins, box := layout.Step(t, side), layout.Step(layout.Step(t, side), side)
			if len(res) >= 2*pairs || !bufferFree(all, future, ins) || !bufferFree(all, future, box) {
				continue
			}
			pi := layout.Placement{Name: "inserter", Pos: ins.Center(), Dir: layout.InserterDir(h.Protos, "inserter", ins, t), Recipe: out.Item, Tag: tag}
			pc := layout.Placement{Name: chest, Pos: box.Center(), Tag: tag}
			all.Add(pi)
			all.Add(pc)
			res = append(res, pi, pc)
		}
		// назад по ленте: тайл, чья лента смотрит в t
		next := world.Tile{}
		found := false
		for _, d := range []int{layout.N, layout.E, layout.S, layout.W} {
			q := layout.Step(t, (d+8)%16)
			if p := all.At(q); p != nil && p.Name == "transport-belt" && p.Dir == d && p.Tag == cluster.Tag(c.Name) {
				next, found = q, true
				break
			}
		}
		if !found {
			break
		}
		t = next
	}
	if len(res) == 0 {
		// почему: что у первой ленты от порта по бокам (разбор)
		t := layout.Step(out.T, (out.Dir+8)%16)
		var why []string
		for _, side := range []int{(out.Dir + 4) % 16, (out.Dir + 12) % 16} {
			for _, q := range []world.Tile{layout.Step(t, side), layout.Step(layout.Step(t, side), side)} {
				switch ok, _ := h.World.Buildable(q); {
				case all.At(q) != nil:
					why = append(why, fmt.Sprintf("%v: %s [%s]", q, all.At(q).Name, all.At(q).Tag))
				case all.Reserved[q]:
					why = append(why, fmt.Sprintf("%v: резерв", q))
				case !ok:
					why = append(why, fmt.Sprintf("%v: не суша/утёс", q))
				}
			}
		}
		return nil, fmt.Errorf("у выходной ленты %s нет места под буфер выхода (порт %v %s, у %v — %v: %s)", c.Name, out.T, layout.DirName(out.Dir), t, all.At(t), strings.Join(why, "; "))
	}
	return res, nil
}

func runSmelting(ctx context.Context, h *act.Hands) error {
	list, err := smeltingLayout(h)
	if err != nil {
		return err
	}
	// добыча и машины, сундуки и инсертеры (фильтры — сразу), потом ленты (подземки — парами по
	// порядку плана), столбы
	order := []string{"electric-mining-drill", furnace, "assembling-machine-1", chest, "inserter", "long-handed-inserter",
		"burner-inserter", "transport-belt", "underground-belt", "splitter", "small-electric-pole"}
	if err := buildSorted(ctx, h, list, order); err != nil {
		return err
	}
	if err := configure(ctx, h, list); err != nil { // фильтры инсертеров буферов
		return err
	}
	// пластины дошли до буферов выхода
	deadline := time.Now().Add(6 * time.Minute)
	for checkSmelting(h) != nil && time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func checkSmelting(h *act.Hands) error {
	list, err := smeltingLayout(h)
	if err != nil {
		return err
	}
	if err := checkBuilt(h, list); err != nil {
		return err
	}
	got := map[string]int{}
	for _, pl := range list {
		if pl.Name != chest || !strings.HasPrefix(pl.Tag, "outbuf/") {
			continue
		}
		st, err := h.B.GetEntity(pl.Name, pl.Pos)
		if err != nil || st == nil {
			continue
		}
		for it, n := range st.Inventories["chest"] {
			got[it] += n
		}
	}
	for _, it := range []string{"iron-plate", "copper-plate"} {
		if got[it] == 0 {
			return fmt.Errorf("в буферах выхода нет %s", it)
		}
	}
	return nil
}
