//go:build e2e

// Стадия 6 — layout «на бумаге»: шахты, плавильни, блок красной науки, станция, столбы,
// всё связано лентами, без коллизий с картой (живой скан случайной карты), PNG.
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
