package plan

import (
	"fmt"

	"factorio-bot/internal/layout"
)

// Мощности — заранее (решение пользователя): перед стройкой потребителя сверить спрос всех
// запланированных электрических машин с мощностью станций; не хватает — сначала станция,
// и расти геометрически (блок power-N — 2^(N-1) цепочек «помпа → бойлер → 2 двигателя»),
// а не по чуть-чуть.

// PowerReserve — запас мощности над спросом.
const PowerReserve = 1.2

// PowerChains — сколько цепочек «помпа → бойлер → 2 двигателя» в блоке power-n.
func PowerChains(n int) int { return 1 << (n - 1) }

// demandW — спрос размещений на электричество (Вт, по максимуму — с запасом).
func demandW(s *State, list []layout.Placement) float64 {
	w := 0.0
	for _, p := range list {
		e := s.Protos.Entities[p.Name]
		if !e.Electric || e.MaxEnergyProduction > 0 {
			continue
		}
		w += (e.MaxEnergyUsage + e.Drain) * 60
	}
	return w
}

// supplyW — мощность генераторов размещений (Вт).
func supplyW(s *State, list []layout.Placement) float64 {
	w := 0.0
	for _, p := range list {
		w += s.Protos.Entities[p.Name].MaxEnergyProduction * 60
	}
	return w
}

// powerShort — какой блок станции строить, чтобы хватило на все раскладки и ещё extra:
// "" — хватает. Считаются все спроектированные задачи (Design) — их список знает State.
func powerShort(s *State, extra []layout.Placement) string {
	if s.Design == nil || s.Designs == nil {
		return ""
	}
	demand, supply := demandW(s, extra), 0.0
	for _, l := range s.Designs() {
		demand += demandW(s, l)
		supply += supplyW(s, l)
	}
	if supply >= demand*PowerReserve {
		return ""
	}
	n := powerBlocks(s)
	if n >= MaxPowerBlocks {
		return ""
	}
	return fmt.Sprintf("power-%d", n+1)
}
