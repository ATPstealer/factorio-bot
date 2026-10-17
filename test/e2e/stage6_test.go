//go:build e2e

// Стадия 6 — layout «на бумаге»: шахты, плавильни, блок красной науки, станция, столбы,
// всё связано лентами, без коллизий с картой (живой скан случайной карты), PNG.
package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"factorio-bot/internal/layout"
)

func TestStage6_DemoLayout(t *testing.T) {
	w := worldT(t)
	p, err := bridgeT(t).Player()
	if err != nil {
		t.Fatal(err)
	}
	l := layout.New(w.Protos, w)
	res, err := l.Demo(p.Position, 2)
	if err != nil {
		if strings.Contains(err.Error(), "не видно") {
			t.Skipf("на открытой карте нет нужного: %v", err)
		}
		t.Fatalf("%v; %v", err, res.Notes)
	}
	if errs := l.Validate(); len(errs) > 0 {
		t.Errorf("раскладка с ошибками (%d):\n%s", len(errs), strings.Join(errs, "\n"))
	}
	c := l.Count()
	for _, want := range []string{"burner-mining-drill", "stone-furnace", "assembling-machine-1", "transport-belt", "small-electric-pole"} {
		if c[want] == 0 {
			t.Errorf("нет %s: %v", want, c)
		}
	}
	t.Logf("%s; %v", l.Summary(), res.Notes)
}

func TestStage6_CLI(t *testing.T) {
	bin := path("bin", "bot")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("нет bin/bot")
	}
	png := path("run", "debug", "layout-test.png")
	os.MkdirAll(path("run", "debug"), 0o755)
	os.Remove(png)
	cmd := exec.Command(bin, "layout", "-o", png, "demo")
	cmd.Dir = root()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "не видно") {
			t.Skipf("на открытой карте нет нужного: %s", out)
		}
		t.Fatalf("bot layout: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "проверка: без ошибок") {
		t.Errorf("вывод:\n%s", out)
	}
	if st, err := os.Stat(png); err != nil || st.Size() < 1000 {
		t.Errorf("PNG: %v", err)
	}
}
