//go:build e2e

// Раскладка «на бумаге» кластерами (красная наука от добычи до лабораторий, станция,
// логистика по коридорам) на живой карте — без ошибок проверки, PNG.
package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBase_Layout(t *testing.T) {
	bin := path("bin", "bot")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("нет bin/bot")
	}
	png := path("run", "debug", "layout-test.png")
	os.MkdirAll(path("run", "debug"), 0o755)
	os.Remove(png)
	// от стартовой зоны: там ресурсы есть на любой карте (у игрока может быть озеро и т.п.)
	cmd := exec.Command(bin, "clusters", "-o", png, "-at", "0,0", "automation-science-pack", "0.1/s")
	cmd.Dir = root()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "не видно") {
			t.Skipf("на открытой карте нет нужного: %s", out)
		}
		t.Fatalf("bot clusters: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "проверка: без ошибок") {
		t.Errorf("вывод:\n%s", out)
	}
	if st, err := os.Stat(png); err != nil || st.Size() < 1000 {
		t.Errorf("PNG: %v", err)
	}
}
