package main

// Команды стадии 5: цели, план, исполнение.

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"factorio-bot/internal/act"
	"factorio-bot/internal/exec"
	"factorio-bot/internal/plan"
)

const statePath, botLog = "run/state.json", "run/bot.log"

// parseGoals — "have ITEM N", "smelt ORE N" (линия плавки, поддерживается) подряд.
func parseGoals(args []string) []exec.GoalSpec {
	var gs []exec.GoalSpec
	for i := 0; i < len(args); {
		if i+2 >= len(args) {
			log.Fatalf("цель %q: нужно «have ПРЕДМЕТ N» или «smelt РУДА N»", args[i:])
		}
		switch args[i] {
		case "have", "smelt":
			n, err := strconv.Atoi(args[i+2])
			check(err)
			gs = append(gs, exec.GoalSpec{Kind: args[i], Item: args[i+1], N: n, Maintain: args[i] == "smelt"})
			i += 3
		default:
			log.Fatalf("неизвестная цель %q: have ПРЕДМЕТ N | smelt РУДА N | keep", args[i])
		}
	}
	return gs
}

// runner — исполнитель с сохранённым состоянием; новые цели заменяют сохранённые,
// "keep" — продолжить сохранённые.
func runner(h *act.Hands, args []string) *exec.Runner {
	saved, err := exec.Load(statePath)
	check(err)
	if len(args) > 0 && args[0] != "keep" {
		saved.Goals = parseGoals(args)
	}
	if len(saved.Goals) == 0 {
		log.Fatal("нет целей: bot run have ПРЕДМЕТ N | smelt РУДА N | keep")
	}
	return &exec.Runner{H: h, B: h.B, Saved: saved, StatePath: statePath, LogPath: botLog}
}

func runRun(ctx context.Context, h *act.Hands, args []string) {
	r := runner(h, args)
	defer r.Close()
	log.Printf("цели: %s", exec.GoalsString(r.Saved.Goals))
	err := r.Run(ctx)
	if err != nil && ctx.Err() == nil {
		check(err)
	}
	check(r.Save())
}

func runPlan(h *act.Hands, args []string) {
	r := runner(h, args)
	s, err := r.Snapshot()
	check(err)
	var goals []plan.Goal
	for _, spec := range r.Saved.Goals {
		g, err := spec.Goal()
		check(err)
		goals = append(goals, g)
	}
	steps, err := plan.Simulate(plan.NewPlanner(), s, goals, 300)
	for i, p := range steps {
		fmt.Printf("%2d. %s\n     почему: %s\n", i+1, p, p.Reason())
	}
	if err != nil {
		fmt.Println("ошибка:", err)
	}
	if len(steps) == 0 && err == nil {
		fmt.Println("цели уже выполнены")
	}
}

func runGoals() {
	saved, err := exec.Load(statePath)
	check(err)
	fmt.Println("цели:", exec.GoalsString(saved.Goals))
	fmt.Println("свои постройки:")
	for _, o := range saved.Owned {
		fmt.Printf("  %s %s в (%.1f, %.1f)\n", o.Role, o.Name, o.Pos.X, o.Pos.Y)
	}
	for k, until := range saved.Planner.Bans {
		fmt.Printf("  бан: %s до %s\n", k, until.Format("15:04:05"))
	}
}
