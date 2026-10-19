package main

// Команда стадии 6: раскладка «на бумаге» по живой карте + PNG.

import (
	"flag"
	"fmt"
	"image/png"
	"log"
	"os"

	"factorio-bot/internal/bridge"
	"factorio-bot/internal/layout"
	"factorio-bot/internal/world"
)

func runLayout(b *bridge.Bridge, args []string) {
	fs := flag.NewFlagSet("layout", flag.ExitOnError)
	out := fs.String("o", "layout.png", "картинка")
	scale := fs.Int("s", 4, "пикселей на тайл")
	at := fs.String("at", "", "центр X,Y (по умолчанию — позиция игрока)")
	fs.Parse(args)
	what := ""
	if fs.NArg() > 0 {
		what = fs.Arg(0)
	}
	w := loadWorld(b)
	p, err := b.Player()
	check(err)
	if *at != "" {
		var x, y float64
		if _, err := fmt.Sscanf(*at, "%g,%g", &x, &y); err != nil {
			log.Fatalf("-at X,Y: %v", err)
		}
		p.Position = bridge.Pos{X: x, Y: y}
	}
	l := layout.New(w.Protos, w)
	switch what {
	case "mine":
		if fs.NArg() < 2 {
			log.Fatal("bot layout mine РУДА [N]")
		}
		ore := fs.Arg(1)
		n := 0
		if fs.NArg() > 2 {
			n = atoi(fs.Arg(2))
		}
		var tiles []world.Tile
		best := -1.0
		for _, pt := range w.Patches() {
			if pt.Name == ore && (best < 0 || pt.Distance(p.Position) < best) {
				best, tiles = pt.Distance(p.Position), pt.Tiles
			}
		}
		mr, err := l.Mine(tiles, ore, "burner-mining-drill", n, "mine")
		check(err)
		l.AllowEnd(mr.Out)
		fmt.Printf("  шахта %s: буров %d, %.2f руды/с, выход %v\n", ore, mr.Drills, mr.Rate, mr.Out)
	case "oil":
		// как Runner.design("oil"): к стоящей сети (столбы в 1000 тайлах), насосы на ближайшем поле
		poles, err := b.FindNearest(bridge.FindArgs{Type: "electric-pole", Radius: 1000, Limit: 50})
		check(err)
		for _, e := range poles {
			l.Anchors = append(l.Anchors, layout.Placement{Name: e.Name, Pos: e.Position})
		}
		check(l.Oil(p.Position, 2, 1, 1))
	default:
		log.Fatalf("bot layout [mine РУДА [N] | oil]")
	}
	fmt.Println("  итого:", l.Summary())
	errs := l.Validate()
	if len(errs) == 0 {
		fmt.Println("  проверка: без ошибок")
	}
	for _, e := range errs {
		fmt.Println("  ✗", e)
	}
	lt, rb := l.Bounds()
	c := world.Tile{X: (lt.X + rb.X) / 2, Y: (lt.Y + rb.Y) / 2}
	r := max(rb.X-lt.X, rb.Y-lt.Y)/2 + 10
	o := world.RenderOptions{Center: c, Radius: r, Scale: *scale, Player: &p.Position}
	img := w.Render(o)
	l.Draw(img, o)
	f, err := os.Create(*out)
	check(err)
	check(png.Encode(f, img))
	check(f.Close())
	fmt.Printf("  %s: %dx%d\n", *out, img.Bounds().Dx(), img.Bounds().Dy())
}
