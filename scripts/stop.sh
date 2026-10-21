#!/usr/bin/env bash
# Остановить экземпляр INST (scripts/inst.sh): сервер (SIGTERM — сохраняет карту в bot.zip),
# клиент и Xephyr. Другие экземпляры не трогает.
source "$(dirname "$0")/inst.sh"
if [[ -f $R/run.pid ]]; then
  kill "$(cat "$R/run.pid")" 2>/dev/null
  rm -f "$R/run.pid"
fi
pkill -TERM -f "^$F --config $R/server/config.ini .*--start-server" &&
  while pgrep -f "^$F --config $R/server/config.ini .*--start-server" >/dev/null; do sleep 0.3; done
pkill -f "^$F --config $R/client/config.ini"
pkill -f "^Xephyr $DISPLAY_NUM "
exit 0
