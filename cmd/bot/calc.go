package main

// Команды стадии 4: калькулятор, путь по техам, ручной крафт. Работают и без игры —
// по кэшу справочника в data/ (изученное тогда считается пустым).

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/knowledge"
)

// knowledgeFor — справочник и изученные техи: из игры, если она доступна, иначе из
// последнего кэша без изученного.
func knowledgeFor(addr, pass string) (*knowledge.Prototypes, []string) {
	if b, rc, err := bridge.Dial(addr, pass); err == nil {
		defer rc.Close()
		p, err := knowledge.LoadOrFetch(b, dataDir)
		check(err)
		f, err := b.Force()
		check(err)
		return p, f.Researched
	}
	files, _ := filepath.Glob(filepath.Join(dataDir, "prototypes-*.json"))
	if len(files) == 0 {
		log.Fatal("игра не запущена и кэша справочника нет (data/prototypes-*.json): запусти scripts/run.sh и bin/bot protos")
	}
	sort.Slice(files, func(i, j int) bool {
		a, _ := os.Stat(files[i])
		b, _ := os.Stat(files[j])
		return a.ModTime().After(b.ModTime())
	})
	p, err := knowledge.Load(files[0])
	check(err)
	log.Printf("игра не запущена — справочник %s, изученного нет", files[0])
	return p, nil
}

// parseRate — "60/min", "60/мин", "1/s", "1/с", "60" (= в минуту) → в секунду.
func parseRate(s string) float64 {
	num, unit, _ := strings.Cut(s, "/")
	v, err := strconv.ParseFloat(num, 64)
	check(err)
	switch unit {
	case "", "min", "мин", "m":
		return v / 60
	case "s", "с", "sec":
		return v
	case "h", "ч":
		return v / 3600
	}
	log.Fatalf("единица %q: min | s | h", unit)
	return 0
}

func perMin(v float64) string {
	v *= 60
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func runCalc(args []string, addr, pass string) {
	fs := flag.NewFlagSet("calc", flag.ExitOnError)
	allF := fs.Bool("all", false, "считать всё изученным (план на будущее)")
	jsonF := fs.Bool("json", false, "вывод в JSON")
	machine := fs.String("machine", "", "принудительные машины: категория=сущность,… (crafting=assembling-machine-2)")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 || len(rest)%2 != 0 {
		log.Fatal("bot calc [-all] [-json] [-machine кат=машина] ПРЕДМЕТ СКОРОСТЬ [ПРЕДМЕТ СКОРОСТЬ ...]")
	}
	p, researched := knowledgeFor(addr, pass)
	targets := map[string]float64{}
	for i := 0; i < len(rest); i += 2 {
		if _, ok := p.Items[rest[i]]; !ok && !p.IsFluid(rest[i]) {
			log.Fatalf("нет предмета %q", rest[i])
		}
		targets[rest[i]] += parseRate(rest[i+1])
	}
	machines := map[string]string{}
	if *machine != "" {
		for _, kv := range strings.Split(*machine, ",") {
			k, v, _ := strings.Cut(kv, "=")
			machines[k] = v
		}
	}
	pl, err := calc.Solve(p, targets, calc.Options{Researched: researched, All: *allF, Machines: machines})
	check(err)
	if *jsonF {
		printJSON(pl)
		return
	}
	printPlan(p, pl)
}

func printPlan(p *knowledge.Prototypes, pl *calc.Plan) {
	tg := make([]string, 0, len(pl.Targets))
	for it := range pl.Targets {
		tg = append(tg, it)
	}
	sort.Strings(tg)

	fmt.Println("Дерево производства (в минуту; [машина ×штук]):")
	shown := map[*calc.Node]bool{}
	var tree func(item string, rate float64, indent string, depth int)
	tree = func(item string, rate float64, indent string, depth int) {
		prods := pl.Producers(item)
		if len(prods) == 0 {
			fmt.Printf("%s%s %s\n", indent, item, perMin(rate))
			return
		}
		total := 0.0
		for _, n := range prods {
			total += n.Out[item]
		}
		for _, n := range prods {
			// несколько производителей — у каждого своя доля потока
			line := fmt.Sprintf("%s%s %s ← %s", indent, item, perMin(rate*n.Out[item]/total), n.Recipe)
			if n.Machine != "" {
				line += fmt.Sprintf(" [%s ×%.2f]", n.Machine, n.Machines)
			}
			if shown[n] || depth > 25 {
				fmt.Println(line + " ↑ см. выше")
				continue
			}
			shown[n] = true
			fmt.Println(line)
			ins := make([]string, 0, len(n.In))
			for it := range n.In {
				ins = append(ins, it)
			}
			sort.Strings(ins)
			for _, it := range ins {
				tree(it, n.In[it], indent+"  ", depth+1)
			}
		}
	}
	for _, it := range tg {
		tree(it, pl.Targets[it], "", 0)
	}

	fmt.Println("\nИтого:")
	fmt.Print("  сырьё с карты:")
	for _, it := range sortedKeys(pl.Raw) {
		fmt.Printf(" %s %s/мин", it, perMin(pl.Raw[it]))
		for _, n := range pl.Nodes {
			if n.Out[it] > 0 && n.Machine != "" && (strings.HasPrefix(n.Recipe, "mine:") || strings.HasPrefix(n.Recipe, "pump:")) {
				fmt.Printf(" (%s ×%.1f)", n.Machine, n.Machines)
			}
		}
		fmt.Print(";")
	}
	fmt.Println()
	if len(pl.Sources) > 0 {
		fmt.Print("  без рецепта (добывать руками):")
		for _, it := range sortedKeys(pl.Sources) {
			fmt.Printf(" %s %s/мин;", it, perMin(pl.Sources[it]))
		}
		fmt.Println()
	}
	machines := map[string]float64{}
	for _, n := range pl.Nodes {
		if n.Machine != "" {
			machines[n.Machine] += n.Machines
		}
	}
	fmt.Print("  машины:")
	for _, m := range sortedKeys(machines) {
		fmt.Printf(" %s %d (%.2f);", m, int(math.Ceil(machines[m]-1e-9)), machines[m])
	}
	fmt.Println()
	if pl.PowerW > 0 {
		fmt.Printf("  электричество: %.2f МВт → паровых двигателей %.1f, бойлеров %.1f, помп %.2f\n",
			pl.PowerW/1e6, pl.Steam.Engines, pl.Steam.Boilers, pl.Steam.Pumps)
	}
	if pl.Fuel > 0 {
		fmt.Printf("  уголь на топливо (машины и бойлеры): %s/мин\n", perMin(pl.Fuel))
	}
	if pl.Belt != "" {
		fmt.Printf("  ленты (%s, %.0f/с):", pl.Belt, pl.BeltCap)
		for _, it := range sortedKeys(pl.Belts) {
			if pl.Belts[it] >= 0.05 {
				fmt.Printf(" %s %.2f;", it, pl.Belts[it])
			}
		}
		fmt.Println()
	}
	if len(pl.Surplus) > 0 {
		fmt.Print("  излишки:")
		for _, it := range sortedKeys(pl.Surplus) {
			fmt.Printf(" %s %s/мин;", it, perMin(pl.Surplus[it]))
		}
		fmt.Println()
	}
	printTechs(pl.Techs, pl.Cost)
}

func printTechs(techs []string, c knowledge.TechCost) {
	if len(techs) == 0 {
		fmt.Println("\nИсследования: ничего не нужно")
		return
	}
	fmt.Printf("\nИсследования (%d, по порядку): %s\n", len(techs), strings.Join(techs, ", "))
	if len(c.Packs) > 0 {
		fmt.Print("  цена:")
		for _, k := range sortedKeys(c.Packs) {
			fmt.Printf(" %s ×%.0f;", k, c.Packs[k])
		}
		fmt.Printf("\n  время при 1 лаборатории (скорость 1): %.1f ч\n", c.Seconds/3600)
	}
	for _, tr := range c.Triggers {
		fmt.Println("  сделать:", tr)
	}
}

func runTech(args []string, addr, pass string) {
	if len(args) < 1 {
		log.Fatal("bot tech ЦЕЛЬ — технология, рецепт, предмет или постройка")
	}
	p, researched := knowledgeFor(addr, pass)
	av := p.NewAvailability(researched, false)
	goal := args[0]
	var goals []string
	switch {
	case p.Technologies[goal].UnitCount > 0 || p.Technologies[goal].Trigger != nil:
		goals = []string{goal}
	default:
		recs := []string{goal}
		if _, ok := p.Recipes[goal]; !ok {
			recs = p.RecipesFor(goal)
		}
		if len(recs) == 0 {
			log.Fatalf("%q — не технология, не рецепт и не предмет", goal)
		}
		goals = p.TechsFor(recs, av)
		if len(goals) == 0 {
			fmt.Printf("%s уже доступен\n", goal)
			return
		}
	}
	techs, err := p.TechPath(goals, av.Researched)
	check(err)
	printTechs(techs, p.Cost(techs))
}

func runRaw(args []string, addr, pass string) {
	if len(args) < 1 {
		log.Fatal("bot raw ПРЕДМЕТ [N]")
	}
	n := 1.0
	if len(args) > 1 {
		v, err := strconv.ParseFloat(args[1], 64)
		check(err)
		n = v
	}
	p, researched := knowledgeFor(addr, pass)
	h := calc.Handcraft(p, p.NewAvailability(researched, false), args[0], n)
	fmt.Printf("%s ×%g руками: %.1f с крафта\n  нужно:", args[0], n, h.Seconds)
	for _, k := range sortedKeys(h.Base) {
		fmt.Printf(" %s %g;", k, h.Base[k])
	}
	fmt.Print("\n  крафты:")
	for _, k := range sortedKeys(h.Crafts) {
		fmt.Printf(" %s ×%g;", k, h.Crafts[k])
	}
	fmt.Println()
}

func sortedKeys[V any](m map[string]V) []string {
	res := make([]string, 0, len(m))
	for k := range m {
		res = append(res, k)
	}
	sort.Strings(res)
	return res
}
