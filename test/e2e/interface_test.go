//go:build e2e

// Интерфейс: меню и карта не мешают действиям.
package e2e

import "testing"

// Защиты: меню игры и карта (мод/ввод не доходят до мира) — бот сам их закрывает.
func TestBase_Guards(t *testing.T) {
	t.Run("menu", checkMenuGuard)
	t.Run("map", checkMapGuard)
}
