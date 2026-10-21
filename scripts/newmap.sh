#!/usr/bin/env bash
# Новая карта и запуск игры в фоне: остановить текущую, создать карту (SEED=… или случайную),
# поднять сервер + Xephyr + клиент и дождаться, пока клиент зайдёт.
#   scripts/newmap.sh            — случайная карта
#   SEED=123 scripts/newmap.sh   — конкретная
#   NEW_MAP=0 scripts/newmap.sh  — перезапуск на той же карте (например, после правки мода)
#   INST=N …                     — экземпляр N (scripts/inst.sh)
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/inst.sh
inst_setup
scripts/stop.sh
NEW_MAP=${NEW_MAP:-1} setsid nohup scripts/run.sh > "$R/run.log" 2>&1 < /dev/null &
for i in $(seq 240); do
  # клиент зашёл и у игрока есть персонаж (на новой карте сначала идёт заставка крушения)
  st=$(scripts/rcon.py '/sc local p=game.connected_players[1] rcon.print(not p and "none" or p.character and "ok" or p.controller_type == defines.controllers.cutscene and "cutscene" or "wait")' 2>/dev/null || true)
  if [[ "$st" == "cutscene" ]]; then
    # заставка крушения: пропустить, как игрок, — Tab в окне клиента
    DISPLAY=$DISPLAY_NUM xdotool mousemove 92 332 key Tab || true
  fi
  if [[ "$st" == "ok" ]]; then
    sleep 2
    find mod/botbridge -type f | sort | xargs cat | md5sum > "$R/mod.hash"
    echo "карта готова: $(cat "$R/map.json")"
    exit 0
  fi
  sleep 1
done
echo "клиент не зашёл за 4 минуты, см. $R/run.log, $R/server.log, $R/client.log" >&2
exit 1
