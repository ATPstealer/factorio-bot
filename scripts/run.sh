#!/usr/bin/env bash
# Запуск: headless-сервер + Xephyr :1 + клиент. Ctrl+C — остановить всё.
set -euo pipefail

F=/mnt/games/steam/steamapps/common/Factorio/bin/x64/factorio
ROOT=$(cd "$(dirname "$0")/.." && pwd)
R=$ROOT/run
SEED=${SEED:-12345}
SAVE=$R/server/saves/bot.zip
DISPLAY_NUM=${DISPLAY_NUM:-:1}
RES=${RES:-1920x1080}


if [[ ! -f $SAVE || ${NEW_MAP:-0} == 1 ]]; then
  "$F" --config "$R/server/config.ini" --mod-directory "$R/server/mods" \
    --create "$SAVE" --map-gen-settings "$ROOT/scripts/map-gen-settings.json" --map-gen-seed "$SEED"
fi

"$F" --config "$R/server/config.ini" --mod-directory "$R/server/mods" \
  --start-server "$SAVE" --server-settings "$ROOT/scripts/server-settings.json" \
  --rcon-port 27015 --rcon-password "${RCON_PASSWORD:-botpass}" > "$R/server.log" 2>&1 &
SERVER=$!
# SIGINT — сервер сохраняет карту в $SAVE перед выходом
trap 'kill $(jobs -p | grep -vx "$SERVER") 2>/dev/null; kill -INT $SERVER 2>/dev/null; wait $SERVER' EXIT

until grep -q "to(InGame)" "$R/server.log" 2>/dev/null; do sleep 0.5; done

Xephyr "$DISPLAY_NUM" -screen "$RES" -glamor -title "factorio-bot" -ac > "$R/xephyr.log" 2>&1 &
sleep 1

# steam_appid.txt + SteamAppId: иначе Steam перезапускает игру у себя на основном дисплее
(cd "$R/client" && DISPLAY=$DISPLAY_NUM SteamAppId=427520 SteamGameId=427520 exec "$F" --config "$R/client/config.ini" --mod-directory "$R/client/mods" \
  --mp-connect 127.0.0.1:34197 > "$R/client.log" 2>&1) &

wait
