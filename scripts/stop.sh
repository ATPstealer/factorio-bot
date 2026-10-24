#!/usr/bin/env bash
# Остановить экземпляр INST (scripts/inst.sh): сервер (SIGTERM — сохраняет карту в bot.zip),
# клиент и Xephyr. Другие экземпляры не трогает.
source "$(dirname "$0")/inst.sh"
if [[ -f $R/run.pid ]]; then
  kill "$(cat "$R/run.pid")" 2>/dev/null
  rm -f "$R/run.pid"
fi
# сервер сохраняет карту и выходит; завис (сохранение висело 40 мин) — через 120 с убить,
# код 1: карта не сохранилась
code=0
if pkill -TERM -f "^$F --config $R/server/config.ini .*--start-server"; then
  for ((i = 0; i < 400; i++)); do
    pgrep -f "^$F --config $R/server/config.ini .*--start-server" >/dev/null || break
    sleep 0.3
  done
  if pgrep -f "^$F --config $R/server/config.ini .*--start-server" >/dev/null; then
    echo "сервер $R не вышел за 120 с — убит, карта не сохранена" >&2
    pkill -KILL -f "^$F --config $R/server/config.ini .*--start-server"
    code=1
  fi
fi
pkill -f "^$F --config $R/client/config.ini"
pkill -f "^Xephyr $DISPLAY_NUM "
exit $code
