// bot — внешний бот Factorio. Пока: отладочные команды стадии 1.
//
//	bot ping                 — проверка моста
//	bot player               — состояние игрока (JSON)
//	bot events [after]       — события из буфера мода
//	bot walk X Y             — дойти до точки клавишами
//	bot chop [радиус]        — найти ближайшее дерево, дойти и срубить
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"time"

	"factorio-bot/internal/act"
	"factorio-bot/internal/bridge"
	"factorio-bot/internal/input"
)

func main() {
	addr := flag.String("rcon", "127.0.0.1:27015", "адрес RCON")
	pass := flag.String("password", envOr("RCON_PASSWORD", "botpass"), "пароль RCON")
	display := flag.String("display", envOr("BOT_DISPLAY", ":1"), "X-дисплей клиента")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	b, rc, err := bridge.Dial(*addr, *pass)
	if err != nil {
		log.Fatal(err)
	}
	defer rc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

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
	case "chop":
		radius := 50.0
		if len(args) > 1 {
			radius = atof(args[1])
		}
		check(chop(ctx, hands(), radius))
	default:
		log.Fatalf("неизвестная команда %q", args[0])
	}
}

// chop рубит ближайшее дерево, на которое удаётся навести курсор.
func chop(ctx context.Context, h *act.Hands, radius float64) error {
	trees, err := h.B.FindNearest(bridge.FindArgs{Type: "tree", Radius: radius, Limit: 5})
	if err != nil {
		return err
	}
	if len(trees) == 0 {
		return fmt.Errorf("деревьев в радиусе %.0f нет", radius)
	}
	before, err := h.B.Player()
	if err != nil {
		return err
	}
	for _, t := range trees {
		log.Printf("дерево %s (%.1f, %.1f), %.1f тайлов; wood: %d", t.Name, t.Position.X, t.Position.Y, t.Distance, before.Inventory["wood"])
		if err := h.WalkTo(ctx, t.Position, 2.5); err != nil {
			return err
		}
		err := h.Mine(ctx, t, 10*time.Second)
		if errors.Is(err, act.ErrHover) {
			log.Print(err, " — беру следующее")
			continue
		}
		if err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
		after, err := h.B.Player()
		if err != nil {
			return err
		}
		log.Printf("срублено; wood: %d → %d", before.Inventory["wood"], after.Inventory["wood"])
		return nil
	}
	return errors.New("ни на одно из ближайших деревьев не навести курсор")
}

func dist(a, b bridge.Pos) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

func check(err error) {
	if err != nil {
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
