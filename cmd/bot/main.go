// bot — внешний бот Factorio. Пока: отладочные команды стадии 1.
//
//	bot ping                 — проверка моста
//	bot player               — состояние игрока (JSON)
//	bot events [after]       — события из буфера мода
//	bot walk X Y             — дойти до точки клавишами
//	bot mine ЦЕЛЬ [N] [R]    — добыть руками N раз (руда — N единиц) в радиусе R (64)
//	                           ЦЕЛЬ: tree|rock|wreck|ore|fish|any, имя сущности или предмета (coal, stone, wood)
//	bot chop                 — то же, что mine tree 1
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
)

func main() {
	addr := flag.String("rcon", "127.0.0.1:27015", "адрес RCON")
	pass := flag.String("password", envOr("RCON_PASSWORD", "botpass"), "пароль RCON")
	display := flag.String("display", envOr("BOT_DISPLAY", ":1"), "X-дисплей клиента")
	limit := flag.Duration("t", 5*time.Minute, "общий лимит времени команды")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	b, rc, err := bridge.Dial(*addr, *pass)
	if err != nil {
		log.Fatal(err)
	}
	defer rc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *limit)
	defer cancel()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
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
	case "mine", "chop":
		target, n, radius := "tree", 1, 64.0
		if args[0] == "mine" {
			if len(args) < 2 {
				log.Fatal("bot mine ЦЕЛЬ [N] [радиус]")
			}
			target = args[1]
			if len(args) > 2 {
				n = atoi(args[2])
			}
			if len(args) > 3 {
				radius = atof(args[3])
			}
		}
		res, err := hands().Gather(ctx, target, n, radius)
		log.Printf("добыто %d, в инвентаре прибавилось: %v", res.Mined, res.Items)
		check(err)
	default:
		log.Fatalf("неизвестная команда %q", args[0])
	}
	for _, f := range atExit {
		f()
	}
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

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
