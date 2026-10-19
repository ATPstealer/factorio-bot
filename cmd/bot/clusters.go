package main

import (
	"flag"
	"fmt"
	"image/png"
	"log"
	"math"
	"os"
	"strings"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/calc"
	"factorio-bot/internal/cluster"
	"factorio-bot/internal/world"
)

// runClusters — план кластеров под цель на текущей карте и картинка базы.
func runClusters(b *bridge.Bridge, args []string) {
	fs := flag.NewFlagSet("clusters", flag.ExitOnError)
	out := fs.String("o", "clusters.png", "картинка")
	scale := fs.Int("s", 2, "пикселей на тайл")
	at := fs.String("at", "", "центр базы X,Y (по умолчанию — позиция игрока)")
	reserve := fs.Float64("reserve", 5, "запас роста out")
	sec := fs.Float64("labsec", 7, "секунд исследования на пакет (лаборатории)")
	machines := fs.String("machine", "crafting=assembling-machine-1,smelting=stone-furnace", "категория=машина,…")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 || len(rest)%2 != 0 {
		log.Fatal("bot clusters [-o F] [-reserve K] [-at X,Y] ПРЕДМЕТ СКОРОСТЬ [...]")
	}
	targets := map[string]float64{}
	for i := 0; i < len(rest); i += 2 {
		targets[rest[i]] += parseRate(rest[i+1])
	}
	w := loadWorld(b)
	// с -at игрок не нужен (хватает сервера без клиента)
	var p bridge.Player
	if *at != "" {
		var x, y float64
		if _, err := fmt.Sscanf(*at, "%g,%g", &x, &y); err != nil {
			log.Fatalf("-at X,Y: %v", err)
		}
		p.Position = bridge.Pos{X: x, Y: y}
	} else {
		var err error
		p, err = b.Player()
		check(err)
	}
	o := calc.Options{All: true, Machines: map[string]string{}}
	for _, kv := range strings.Split(*machines, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			o.Machines[k] = v
		}
	}
	plan, err := calc.Solve(w.Protos, targets, o)
	check(err)
	cs, err := cluster.Split(w.Protos, plan, *reserve, nil)
	check(err)
	packs := map[string]float64{}
	for it, r := range targets {
		if strings.HasSuffix(it, "-science-pack") {
			packs[it] = r
		}
	}
	if len(packs) > 0 {
		cs = append(cs, cluster.Labs(w.Protos, packs, *sec, *reserve))
	}
	cs = append(cs, cluster.StorageCluster())
	cs, err = cluster.AddPower(w.Protos, cs, *reserve, nil)
	check(err)
	base := cluster.NewBase(w.Protos, w, world.TileOf(p.Position))
	err = base.PlaceAll(cs)
	for _, c := range base.Sorted() {
		fmt.Println(" ", c)
	}
	check(err)
	// внутренности и логистика — «на бумаге», с проверкой раскладки
	l, err := base.Layout()
	check(err)
	check(base.Connect(l, nil, nil))
	fmt.Println("  итого:", l.Summary())
	if os.Getenv("CLUSTERS_DUMP") != "" { // отладка: размещения раскладки
		for _, pl := range l.List {
			fmt.Printf("    %s %s\n", pl.Tag, pl)
		}
	}
	if errs := l.Validate(); len(errs) == 0 {
		fmt.Println("  проверка: без ошибок")
	} else {
		for _, e := range errs {
			fmt.Println("  ✗", e)
		}
	}
	// картинка: все кластеры с коридорами
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, c := range base.Clusters {
		r := c.Area.Grow(cluster.Corridor)
		minX, minY = min(minX, r.Min.X), min(minY, r.Min.Y)
		maxX, maxY = max(maxX, r.Max.X), max(maxY, r.Max.Y)
	}
	ro := world.RenderOptions{Center: world.Tile{X: (minX + maxX) / 2, Y: (minY + maxY) / 2},
		Radius: max(maxX-minX, maxY-minY)/2 + 20, Scale: *scale, Player: &p.Position, Labels: true}
	img := w.Render(ro)
	base.Draw(img, ro)
	l.Draw(img, ro)
	f, err := os.Create(*out)
	check(err)
	check(png.Encode(f, img))
	check(f.Close())
	fmt.Printf("  %s: %dx%d\n", *out, img.Bounds().Dx(), img.Bounds().Dy())
}
