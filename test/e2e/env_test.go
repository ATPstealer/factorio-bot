//go:build e2e

// Окружение: игра, мод, клиент, настройки.
package e2e

import "testing"

// Окружение: файлы настроек и связи (без игры).
func TestConfig(t *testing.T) {
	t.Run("map-gen", checkMapGenFile)
	t.Run("server-settings", checkServerSettings)
	t.Run("config.ini", checkConfigIni)
	t.Run("mod-links", checkModLinked)
}

// Игра: сервер идёт, моды, мост отвечает, карта та, клиент бота в Xephyr.
func TestGame(t *testing.T) {
	t.Run("server", checkServerRunning)
	t.Run("mods", checkMods)
	t.Run("botbridge", checkBotbridgePing)
	t.Run("map", checkMap)
	t.Run("client", checkClient)
}
