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
  path X Y              показать маршрут от игрока до (X, Y): точки и деревья, которые рубить по дороге (отладка, без движения)
  walk X Y              дойти клавишами WASD до точки мира (X, Y), точность 0.5 тайла
  craft РЕЦЕПТ [N]      скрафтить N раз (по умолчанию 1) и дождаться: E → поиск → клик по ячейке рецепта
  place ПРЕДМЕТ X Y [DIR]
                        поставить постройку центром в (X, Y); DIR: n|e|s|w (n)
  insert ПРЕДМЕТ N X Y  положить N предметов в постройку в точке (X, Y) (клавиша Z по одному)
  take X Y              забрать результат из постройки в точке (X, Y) (Ctrl+ЛКМ)
  pickup X Y            подобрать свою постройку в точке (X, Y)
  run ЦЕЛЬ… | keep      выполнять цели (планировщик сам добывает, крафтит, плавит, ставит):
                          start — старт игры: 50 дерева, 200 камня с камней, добывающие точки на бурах
                          (уголь парами, железо/медь бур→печь, камень 4 бура→сундук; поддерживается);
                          have ПРЕДМЕТ N — иметь в инвентаре; smelt РУДА N — линия из N печей на руде
                          с углём (поддерживается: бот следит и чинит, выход — Ctrl+C или -t);
                          keep — продолжить сохранённые цели (run/state.json); журнал — run/bot.log
  plan ЦЕЛЬ…            показать план (шаги и почему) без исполнения
  goals                 сохранённые цели, свои постройки, баны
  layout [-o F] [-pairs N] [demo | mine РУДА [N]]
                        раскладка «на бумаге» по открытой карте + PNG: demo — шахты железа, меди и
                        угля, плавильни, блок красной науки, паровая станция, столбы, всё на лентах
  map [-r R] [-s S] [-o FILE]
                        нарисовать открытую карту вокруг игрока в PNG: радиус R тайлов (150),
                        S пикселей на тайл (3), файл FILE (map.png); руды, вода, деревья, камни, обломки
  patches [R]           месторождения на открытой карте в радиусе R (по умолчанию все): ресурс, запас, расстояние
  protos [-refresh]     справочник игры (рецепты, предметы, сущности, техи); кэш data/prototypes-<версия>.json
  force                 исследования: изучено, текущее, прогресс, доступные рецепты
  shot [-o FILE]        снимок окна клиента в PNG (shot.png) — для отладки и калибровки интерфейса
  mine ЦЕЛЬ [N] [R]     добыть руками N раз в радиусе R тайлов (по умолчанию N=1, R=64)
                          ЦЕЛЬ — группа:  tree, rock, wreck (обломки корабля), ore, fish, any
                                 предмет:  coal, stone, wood, iron-ore, copper-ore ...
                                 сущность: huge-rock, crash-site-spaceship-wreck-big-1 ...
                          N — сколько раз добыть, а не сколько ресурса получить:
                            mine wood 3  — срубить 3 дерева (дерева в инвентаре прибавится ~6–12)
                            mine rock 2  — разбить 2 камня (по 20+ камня с каждого)
                            mine coal 10 — 10 раз копнуть уголь = 10 угля (руда даёт 1 за раз)

Расчёты (работают и без игры — по кэшу data/, изученного тогда нет):
  calc [-all] [-json] [-machine кат=машина,…] ПРЕДМЕТ СКОРОСТЬ [ПРЕДМЕТ СКОРОСТЬ …]
                        дерево производства, машины, сырьё, буры, электричество, ленты, нужные
                        исследования; СКОРОСТЬ: 60/min | 1/s | 2/h (без единицы — в минуту);
                        -all — считать всё изученным
  tech ЦЕЛЬ             путь исследований до технологии / рецепта / предмета: порядок, цена, действия
  raw ПРЕДМЕТ [N]       ручной крафт: что нужно иметь и сколько секунд

Примеры:
  bin/bot ping
  bin/bot map -r 200 -o start.png
  bin/bot patches 300
  bin/bot walk 10 40
  bin/bot mine coal 10
  bin/bot mine wreck 3
  bin/bot -t 60s mine stone 20
  bin/bot calc -all rocket-part 1/min
  bin/bot calc automation-science-pack 30 logistic-science-pack 30
  bin/bot tech rocket-silo
  bin/bot plan smelt iron-ore 5
  bin/bot -t 20m run smelt iron-ore 5

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
	// расчётные команды работают и без игры
	switch args[0] {
	case "calc":
		runCalc(args[1:], *addr, *pass)
		return
	case "tech":
		runTech(args[1:], *addr, *pass)
		return
	case "raw":
		runRaw(args[1:], *addr, *pass)
		return
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
		h.Console = true
		h.DebugDir = "run/debug"
		h.World = loadWorld(b)
		h.Protos = h.World.Protos
		check(h.Clean(context.Background()))
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
	case "path":
		if len(args) < 3 {
			log.Fatal("bot path X Y")
		}
		w := loadWorld(b)
		p, err := b.Player()
		check(err)
		target := bridge.Pos{X: atof(args[1]), Y: atof(args[2])}
		r, err := w.Route(p.Position, target, 0.5, 0, 6)
		fmt.Printf("от (%.2f, %.2f), длина %.1f (по прямой %.1f), %v:\n", p.Position.X, p.Position.Y,
			r.Length(), dist(p.Position, target), err)
		for _, pt := range r.Points[min(1, len(r.Points)):] {
			fmt.Printf("  (%.1f, %.1f)\n", pt.X, pt.Y)
		}
		if err != nil {
			fmt.Print(w.DebugCells(p.Position, 6))
		}
		for _, c := range r.Cuts {
			fmt.Printf("  срубить %s (%.1f, %.1f) на %.1f\n", c.Tree.Name, c.Tree.Pos.X, c.Tree.Pos.Y, c.S)
		}
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
		path := knowledge.CachePath(dataDir, knowledge.CacheKey(pong))
		if *refresh {
			os.Remove(path)
		}
		pr, err := knowledge.LoadOrFetch(b, dataDir)
		check(err)
		fmt.Printf("%s: рецептов %d, предметов %d, сущностей %d, технологий %d, жидкостей %d\n",
			path, len(pr.Recipes), len(pr.Items), len(pr.Entities), len(pr.Technologies), len(pr.Fluids))
	case "craft":
		if len(args) < 2 {
			log.Fatal("bot craft РЕЦЕПТ [N]")
		}
		n := 1
		if len(args) > 2 {
			n = atoi(args[2])
		}
		check(hands().CraftWait(ctx, args[1], n))
		log.Printf("скрафчено %s ×%d", args[1], n)
	case "place":
		if len(args) < 4 {
			log.Fatal("bot place ПРЕДМЕТ X Y [n|e|s|w]")
		}
		dir := act.North
		if len(args) > 4 {
			dir = map[string]int{"n": act.North, "e": act.East, "s": act.South, "w": act.West}[args[4]]
		}
		e, err := hands().Place(ctx, args[1], bridge.Pos{X: atof(args[2]), Y: atof(args[3])}, dir)
		check(err)
		log.Printf("поставлено %s в (%.1f, %.1f)", e.Name, e.Position.X, e.Position.Y)
	case "insert", "take", "pickup":
		var pos bridge.Pos
		var item string
		n := 0
		switch {
		case args[0] == "insert" && len(args) >= 5:
			item, n, pos = args[1], atoi(args[2]), bridge.Pos{X: atof(args[3]), Y: atof(args[4])}
		case args[0] != "insert" && len(args) >= 3:
			pos = bridge.Pos{X: atof(args[1]), Y: atof(args[2])}
		default:
			log.Fatalf("bot %s: см. bin/bot", args[0])
		}
		h := hands()
		es, err := b.FindNearest(bridge.FindArgs{Position: &pos, Radius: 1.5, Limit: 5})
		check(err)
		var target *bridge.Entity
		for i := range es {
			if es[i].Type != "character" && es[i].Type != "resource" {
				target = &es[i]
				break
			}
		}
		if target == nil {
			log.Fatalf("в (%.1f, %.1f) нет постройки", pos.X, pos.Y)
		}
		switch args[0] {
		case "insert":
			check(h.Insert(ctx, *target, item, n))
			log.Printf("в %s положено %s ×%d", target.Name, item, n)
		case "take":
			got, err := h.Take(ctx, *target)
			check(err)
			log.Printf("из %s забрано: %v", target.Name, got)
		case "pickup":
			check(h.Pickup(ctx, *target))
			log.Printf("подобрано %s", target.Name)
		}
	case "run":
		runRun(ctx, hands(), args[1:])
	case "plan":
		runPlan(hands(), args[1:])
	case "goals":
		runGoals()
	case "layout":
		runLayout(b, args[1:])
	case "shot":
		fs := flag.NewFlagSet("shot", flag.ExitOnError)
		out := fs.String("o", "shot.png", "файл")
		fs.Parse(args[1:])
		h := hands()
		img, err := h.In.Screenshot(h.Win)
		check(err)
		f, err := os.Create(*out)
		check(err)
		check(png.Encode(f, img))
		check(f.Close())
		log.Printf("%s: %dx%d", *out, img.Bounds().Dx(), img.Bounds().Dy())
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
