// bot — внешний бот Factorio. Справка по командам — usage ниже (bin/bot без аргументов).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"log"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
	"factorio-bot/internal/knowledge"
	"factorio-bot/internal/world"
)

const usage = `bot — бот Factorio: действует только мышью/клавиатурой в окне клиента, смотрит через мод по RCON.

Перед запуском (в отдельном терминале):
  scripts/run.sh                 сервер + Xephyr + клиент; ждать, пока клиент зайдёт в игру
  go build -o bin/bot ./cmd/bot  собрать бота (после изменений кода)

Использование:
  bin/bot [флаги] КОМАНДА [аргументы]

Команды:
  ping                  проверить связь: тик игры и версия мода botbridge
  player                состояние игрока (позиция, зум, инвентарь, курсор, выделение) в JSON
  events [AFTER]        события из буфера мода (добыча, постройка, крафт...) с номером > AFTER
  walk X Y              дойти клавишами WASD до точки мира (X, Y), точность 0.5 тайла
  map [-r R] [-s S] [-o FILE]
                        нарисовать открытую карту вокруг игрока в PNG: радиус R тайлов (150),
                        S пикселей на тайл (3), файл FILE (map.png); руды, вода, деревья, камни, обломки
  patches [R]           месторождения на открытой карте в радиусе R (по умолчанию все): ресурс, запас, расстояние
  protos [-refresh]     справочник игры (рецепты, предметы, сущности, техи); кэш data/prototypes-<версия>.json
  force                 исследования: изучено, текущее, прогресс, доступные рецепты
  mine ЦЕЛЬ [N] [R]     добыть руками N раз в радиусе R тайлов (по умолчанию N=1, R=64)
                          ЦЕЛЬ — группа:  tree, rock, wreck (обломки корабля), ore, fish, any
                                 предмет:  coal, stone, wood, iron-ore, copper-ore ...
                                 сущность: huge-rock, crash-site-spaceship-wreck-big-1 ...
                          N — сколько раз добыть, а не сколько ресурса получить:
                            mine wood 3  — срубить 3 дерева (дерева в инвентаре прибавится ~6–12)
                            mine rock 2  — разбить 2 камня (по 20+ камня с каждого)
                            mine coal 10 — 10 раз копнуть уголь = 10 угля (руда даёт 1 за раз)

Примеры:
  bin/bot ping
  bin/bot map -r 200 -o start.png
  bin/bot patches 300
  bin/bot walk 10 40
  bin/bot mine coal 10
  bin/bot mine wreck 3
  bin/bot -t 60s mine stone 20

Флаги:
`

func main() {
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
		fmt.Fprint(flag.CommandLine.Output(), `
Если персонаж сам продолжает идти или копать (бота убили на середине):
  DISPLAY=:1 xdotool mouseup 3 keyup w keyup a keyup s keyup d
`)
	}
	addr := flag.String("rcon", "127.0.0.1:27015", "адрес RCON")
	pass := flag.String("password", envOr("RCON_PASSWORD", "botpass"), "пароль RCON")
	display := flag.String("display", envOr("BOT_DISPLAY", ":1"), "X-дисплей клиента")
	limit := flag.Duration("t", 5*time.Minute, "общий лимит времени команды")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	b, rc, err := bridge.Dial(*addr, *pass)
	if err != nil {
		log.Fatalf("RCON %s: %v (игра запущена? scripts/run.sh)", *addr, err)
	}
	defer rc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *limit)
	defer cancel()

	hands := func() *act.Hands {
		in, err := input.Open(*display)
		if err != nil {
			log.Fatal(err)
		}
		check(in.Reset())
		atExit = append(atExit, in.Close) // отпустить клавиши/кнопки при любом выходе
		h, err := act.New(b, in)
		if err != nil {
			log.Fatal(err)
		}
		h.Log = log.Printf
		return h
	}

	switch args[0] {
	case "ping":
		p, err := b.Ping()
		check(err)
		fmt.Printf("tick %d, botbridge %s\n", p.Tick, p.Version)
	case "player":
		p, err := b.Player()
		check(err)
		printJSON(p)
	case "events":
		after := 0
		if len(args) > 1 {
			after = atoi(args[1])
		}
		evs, seq, err := b.PollEvents(after)
		check(err)
		printJSON(evs)
		fmt.Println("seq", seq)
	case "walk":
		if len(args) < 3 {
			log.Fatal("bot walk X Y")
		}
		h := hands()
		target := bridge.Pos{X: atof(args[1]), Y: atof(args[2])}
		check(h.WalkTo(ctx, target, 0.5))
		p, err := b.Player()
		check(err)
		log.Printf("стою в (%.2f, %.2f), до цели %.2f", p.Position.X, p.Position.Y,
			dist(p.Position, target))
	case "mine":
		if len(args) < 2 {
			log.Fatal("bot mine ЦЕЛЬ [N] [радиус]")
		}
		target, n, radius := args[1], 1, 64.0
		if len(args) > 2 {
			n = atoi(args[2])
		}
		if len(args) > 3 {
			radius = atof(args[3])
		}
		res, err := hands().Gather(ctx, target, n, radius)
		log.Printf("добыто %d, в инвентаре прибавилось: %v", res.Mined, res.Items)
		check(err)
	case "map":
		fs := flag.NewFlagSet("map", flag.ExitOnError)
		r := fs.Int("r", 150, "радиус, тайлов")
		sc := fs.Int("s", 3, "пикселей на тайл")
		out := fs.String("o", "map.png", "файл")
		fs.Parse(args[1:])
		w := loadWorld(b)
		p, err := b.Player()
		check(err)
		img := w.Render(world.RenderOptions{Center: world.TileOf(p.Position), Radius: *r, Scale: *sc, Player: &p.Position, Labels: true})
		f, err := os.Create(*out)
		check(err)
		check(png.Encode(f, img))
		check(f.Close())
		log.Printf("%s: %dx%d px, открытых чанков %d, месторождений %d", *out, img.Bounds().Dx(), img.Bounds().Dy(), len(w.Chunks), len(w.Patches()))
	case "patches":
		radius := math.Inf(1)
		if len(args) > 1 {
			radius = atof(args[1])
		}
		w := loadWorld(b)
		p, err := b.Player()
		check(err)
		ps := w.Patches()
		sort.Slice(ps, func(i, j int) bool { return ps[i].Distance(p.Position) < ps[j].Distance(p.Position) })
		fmt.Printf("%-12s %10s %6s %8s  %s\n", "ресурс", "запас", "тайлов", "до него", "центр")
		for _, pt := range ps {
			d := pt.Distance(p.Position)
			if d > radius {
				continue
			}
			fmt.Printf("%-12s %10d %6d %8.0f  (%.0f, %.0f)\n", pt.Name, pt.Amount, len(pt.Tiles), d, pt.Center.X, pt.Center.Y)
		}
	case "protos":
		fs := flag.NewFlagSet("protos", flag.ExitOnError)
		refresh := fs.Bool("refresh", false, "перечитать из игры")
		fs.Parse(args[1:])
		pong, err := b.Ping()
		check(err)
		path := knowledge.CachePath(dataDir, pong.GameVersion)
		if *refresh {
			os.Remove(path)
		}
		pr, err := knowledge.LoadOrFetch(b, dataDir)
		check(err)
		fmt.Printf("%s: рецептов %d, предметов %d, сущностей %d, технологий %d, жидкостей %d\n",
			path, len(pr.Recipes), len(pr.Items), len(pr.Entities), len(pr.Technologies), len(pr.Fluids))
	case "force":
		f, err := b.Force()
		check(err)
		printJSON(f)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n", args[0])
		flag.Usage()
		os.Exit(2)
	}
	for _, f := range atExit {
		f()
	}
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

const dataDir = "data"

// loadWorld — справочник (из кэша) и полное сканирование открытой карты.
func loadWorld(b *bridge.Bridge) *world.World {
	pr, err := knowledge.LoadOrFetch(b, dataDir)
	check(err)
	w := world.New(pr)
	n, err := w.Sync(b, true)
	check(err)
	log.Printf("просканировано чанков: %d", n)
	return w
}

// atExit — что сделать перед выходом; log.Fatal пропускает defer, поэтому check вызывает их сам.
var atExit []func()

func check(err error) {
	if err != nil {
		for _, f := range atExit {
			f()
		}
		log.Fatal(err)
	}
}

func printJSON(v any) {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	e.Encode(v)
}

func atof(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	check(err)
	return f
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	check(err)
	return n
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
