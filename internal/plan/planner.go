package plan

import (
	"fmt"
	"time"

	"factorio-bot/internal/bridge"
)

// Planner — выбор следующего шага и учёт отказов: шаг, упавший MaxFails раз подряд,
// банится на BanFor — разложение берёт другой метод / другое место.
type Planner struct {
	Fails    map[string]int        `json:"fails"`
	Bans     map[string]time.Time  `json:"bans"`
	Spots    map[string]bridge.Pos `json:"spots"` // забаненные места построек: ключ → позиция
	MaxFails int                   `json:"-"`
	BanFor   time.Duration         `json:"-"`
	Now      func() time.Time      `json:"-"`
}

func NewPlanner() *Planner {
	return &Planner{Fails: map[string]int{}, Bans: map[string]time.Time{}, Spots: map[string]bridge.Pos{},
		MaxFails: 3, BanFor: 5 * time.Minute, Now: time.Now}
}

func (p *Planner) init() {
	if p.Fails == nil {
		p.Fails = map[string]int{}
	}
	if p.Bans == nil {
		p.Bans = map[string]time.Time{}
	}
	if p.Spots == nil {
		p.Spots = map[string]bridge.Pos{}
	}
	if p.MaxFails == 0 {
		p.MaxFails = 3
	}
	if p.BanFor == 0 {
		p.BanFor = 5 * time.Minute
	}
	if p.Now == nil {
		p.Now = time.Now
	}
}

func (p *Planner) banned(key string) bool {
	p.init()
	until, ok := p.Bans[key]
	return ok && p.Now().Before(until)
}

func (p *Planner) badSpots() []bridge.Pos {
	p.init()
	var res []bridge.Pos
	for k, pos := range p.Spots {
		if p.banned(k) {
			res = append(res, pos)
		}
	}
	return res
}

// Next — следующий примитив для цели или nil, если цель выполнена.
func (p *Planner) Next(s *State, g Goal) (*Primitive, error) {
	p.init()
	if g.Satisfied(s) {
		return nil, nil
	}
	c, err := (&ctx{stack: map[string]bool{}}).push(g)
	if err != nil {
		return nil, err
	}
	prim, err := g.next(p, s, c)
	if err == nil && prim == nil && !g.Satisfied(s) {
		return nil, fmt.Errorf("plan: %s не выполнена, а шагов нет", g)
	}
	return prim, err
}

// Failed — шаг не удался: после MaxFails подряд — бан (место постройки — тоже).
func (p *Planner) Failed(prim *Primitive) (banned bool) {
	p.init()
	k := prim.Key()
	p.Fails[k]++
	if prim.Kind == "place" {
		// место плохое сразу: другое место найдётся
		p.Bans[k] = p.Now().Add(p.BanFor)
		p.Spots[k] = prim.Pos
		return true
	}
	if p.Fails[k] >= p.MaxFails {
		p.Bans[k] = p.Now().Add(p.BanFor)
		p.Fails[k] = 0
		return true
	}
	return false
}

// Succeeded — шаг удался: счётчик отказов сбрасывается.
func (p *Planner) Succeeded(prim *Primitive) {
	p.init()
	delete(p.Fails, prim.Key())
}
