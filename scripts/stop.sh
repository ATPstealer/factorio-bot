#!/usr/bin/env bash
# Остановить сервер (SIGTERM — сохраняет карту в bot.zip), клиент и Xephyr.
F='^/mnt/games/steam/steamapps/common/Factorio/bin/x64/factorio'
pkill -f "^bash .*scripts/run.sh" 2>/dev/null
pkill -TERM -f "$F .*--start-server" && while pgrep -f "$F .*--start-server" >/dev/null; do sleep 0.3; done
pkill -f "$F .*run/client/config.ini"
pkill -x Xephyr
exit 0
