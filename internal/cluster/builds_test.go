package cluster

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"factorio-bot/internal/calc"
)

// sciencePacks — пакеты до ракеты (stages.md: наука 0.1/с каждого).
var sciencePacks = []string{"automation-science-pack", "logistic-science-pack", "chemical-science-pack",
	"production-science-pack", "utility-science-pack"}

// buildRate — на стройку стадий до 61, шт/с: прикидка по планам до рефакторинга (постройки и всё,
// из чего они крафтятся, за 10 ч игры; стадии — строки «Ставит» stages.md).
var buildRate = map[string]float64{
	"iron-plate": 16518.0 / 3600, "copper-plate": 889.0 / 3600, "electronic-circuit": 457.0 / 3600,
	"inserter": 113.0 / 3600, "transport-belt": 1864.0 / 3600, "steel-plate": 178.0 / 3600,
	"fast-transport-belt": 595.0 / 3600,
}

// demand — сколько предметов в секунду тратит наука packs по 0.1/с машинами machines (с самими
// пакетами), без того, что кластеры таблицы делают внутри себя: промежуточное пар и железо стали.
func demand(t *testing.T, packs []string, machines map[string]string) (map[string]float64, *calc.Plan) {
	t.Helper()
	targets := map[string]float64{}
	for _, s := range packs {
		targets[s] = 0.1
	}
	plan, err := calc.Solve(pt(t), targets, calc.Options{All: true, Machines: machines})
	if err != nil {
		t.Fatal(err)
	}
	d := map[string]float64{}
	for it, v := range targets {
		d[it] += v
	}
	for _, n := range plan.Nodes {
		for it, v := range n.In {
			if n.Recipe == "steel-plate" && it == "iron-plate" {
				continue // сталь — из своей руды (cluster.md §2а «Сталь»)
			}
			d[it] += v
		}
	}
	main := map[string]bool{}
	for _, b := range Builds {
		main[b.Product] = true
	}
	for _, b := range Builds {
		for _, s := range b.Subs {
			if it := product(pt(t).Recipes[s]); !main[it] {
				delete(d, it)
			}
		}
	}
	return d, plan
}

// Числа таблицы — те же, что в строках «Ставит» stages.md.
func TestBuildsMatchStages(t *testing.T) {
	data, err := os.ReadFile("../../stages.md")
	if err != nil {
		t.Fatal(err)
	}
	put := map[int]string{} // стадия → её абзац «Ставит»
	stage := 0
	head := regexp.MustCompile(`^## (\d+)\.`)
	for _, line := range strings.Split(string(data), "\n") {
		if m := head.FindStringSubmatch(line); m != nil {
			stage, _ = strconv.Atoi(m[1])
			continue
		}
		switch {
		case strings.HasPrefix(line, "- Ставит:"):
			put[stage] = line
		case put[stage] != "" && strings.HasPrefix(line, "  ") && !strings.HasSuffix(put[stage], "\x00"):
			put[stage] += " " + strings.TrimSpace(line)
		case put[stage] != "":
			put[stage] += "\x00" // абзац кончился
		}
	}
	for _, b := range Builds {
		text := put[b.Stage]
		if text == "" {
			t.Errorf("стадия %d (%s): нет строки «Ставит»", b.Stage, b.Product)
			continue
		}
		var num string
		switch {
		case b.Ore > 0:
			num = spaced(b.Ore)
		default:
			num = strconv.Itoa(b.N)
		}
		if !hasNumber(text, num) {
			t.Errorf("стадия %d (%s): в «Ставит» нет числа %s: %s", b.Stage, b.Product, num, strings.TrimSuffix(text, "\x00"))
		}
	}
}

// hasNumber — в тексте есть число num целиком: не часть другого (ни цифр, ни групп «цифры через
// пробел» вплотную — 500 000 не находится в 2 500 000).
func hasNumber(text, num string) bool {
	digit := func(i int) bool { return i >= 0 && i < len(text) && text[i] >= '0' && text[i] <= '9' }
	for i := 0; ; {
		j := strings.Index(text[i:], num)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(num)
		before := digit(a-1) || a >= 2 && text[a-1] == ' ' && digit(a-2)
		after := digit(b) || b+1 < len(text) && text[b] == ' ' && digit(b+1)
		if !before && !after {
			return true
		}
		i = a + 1
	}
}

// spaced — число с пробелами между тысячами (1 000 000).
func spaced(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	return s
}

// Таблицы хватает: выход кластеров на втором уровне — не меньше, чем тратят пять наук по 0.1/с и
// стройка.
func TestBuildsEnoughTier2(t *testing.T) {
	p := pt(t)
	d, plan := demand(t, sciencePacks, map[string]string{"smelting": "steel-furnace", "crafting": "assembling-machine-2"})
	supply := map[string]float64{}
	for _, b := range Builds {
		for it, v := range b.Capacity(p, true) {
			supply[it] += v
		}
	}
	for _, it := range sortedKeys(d) {
		if _, made := supply[it]; !made {
			continue // сырьё, вода, промежуточное внутри пар
		}
		need := d[it] + buildRate[it]
		t.Logf("%-26s нужно %7.2f/с (наука %.2f + стройка %.2f), выход %7.2f/с", it, need, d[it], buildRate[it], supply[it])
		if supply[it] < need {
			t.Errorf("%s: выход %.2f/с меньше нужного %.2f/с", it, supply[it], need)
		}
	}
	var wells, boilers int
	for _, b := range Builds {
		switch {
		case b.Kind == Mine && b.Product == "crude-oil":
			wells += b.N
		case b.Kind == Power:
			boilers += b.N
		}
	}
	// скважина на минимуме — 2 нефти/с (cluster.md §2а «Нефть»)
	if oil := float64(wells) * 2; oil < plan.Raw["crude-oil"] {
		t.Errorf("нефть: %d скважин на минимуме — %.0f/с, нужно %.0f/с", wells, oil, plan.Raw["crude-oil"])
	}
	if w := float64(boilers) * p.Entities["boiler"].MaxEnergyUsage * 60; w < plan.PowerW {
		t.Errorf("станция: %d бойлеров — %.1f МВт, нужно %.1f МВт", boilers, w/1e6, plan.PowerW/1e6)
	}
}

// До замены на второй уровень (стадия Tier2From) кластеры стоят на первом: науке до фиолетовой —
// хватает.
func TestBuildsEnoughTier1(t *testing.T) {
	p := pt(t)
	d, _ := demand(t, sciencePacks[:4], map[string]string{"smelting": "stone-furnace", "crafting": "assembling-machine-1"})
	supply := map[string]float64{}
	for _, b := range Builds {
		if b.Stage >= Tier2From {
			continue
		}
		for it, v := range b.Capacity(p, false) {
			supply[it] += v
		}
	}
	for _, it := range sortedKeys(d) {
		if _, made := supply[it]; !made {
			continue
		}
		t.Logf("%-26s нужно %7.2f/с (наука), выход %7.2f/с", it, d[it], supply[it])
		if supply[it] < d[it] {
			t.Errorf("%s: выход %.2f/с меньше нужного науке %.2f/с", it, supply[it], d[it])
		}
	}
}

func sortedKeys(m map[string]float64) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// Разбивки на два кластера (cluster.md «Сборка»): от MaxN + остаток, уменьшая больший, до ровной;
// не больше MaxN — одна.
func TestSplits(t *testing.T) {
	sizes := func(b Build) [][]int {
		var res [][]int
		for _, s := range b.Splits() {
			var n []int
			for _, part := range s {
				n = append(n, part.N)
			}
			res = append(res, n)
		}
		return res
	}
	for _, c := range []struct {
		n, max int
		want   [][]int
	}{
		{13, 10, [][]int{{10, 3}, {9, 4}, {8, 5}, {7, 6}}},
		{12, 10, [][]int{{10, 2}, {9, 3}, {8, 4}, {7, 5}, {6, 6}}},
		{5, 10, [][]int{{5}}},
		{5, 0, [][]int{{5}}},
	} {
		if got := sizes(Build{N: c.n, MaxN: c.max}); !slices.EqualFunc(got, c.want, slices.Equal) {
			t.Errorf("N %d, MaxN %d: %v, ждал %v", c.n, c.max, got, c.want)
		}
	}
}
