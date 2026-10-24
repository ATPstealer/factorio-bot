# Экземпляр игры (подключается через source из других скриптов). INST — номер:
#   0 (по умолчанию) — основной: run/, дисплей :1, RCON 27015, порт игры 34197;
#   1–4 — параллельные: run/iN, дисплей :N+1, RCON 27015+N, порт 34197+N.
# Снимки (run/snapshots), логи стадий и бенча — общие.
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
F=/mnt/games/steam/steamapps/common/Factorio/bin/x64/factorio
# с 2.1 bin/x64/factorio — скрипт-обёртка (exec bin/x64_/factorio): запускать сам файл игры,
# иначе процесс не находится по «^$F --config …» (stop.sh не останавливал сервер)
[[ -x ${F%/x64/factorio}/x64_/factorio ]] && F=${F%/x64/factorio}/x64_/factorio
INST=${INST:-0}
if [[ $INST == 0 ]]; then R=$ROOT/run; else R=$ROOT/run/i$INST; fi
DISPLAY_NUM=${DISPLAY_NUM:-:$((INST + 1))}
RCON_PORT=$((27015 + INST))
GAME_PORT=$((34197 + INST))
# бот, тесты и rcon.py берут экземпляр отсюда
export INST R RCON_PORT
export BOT_RUN=$R BOT_DISPLAY=$DISPLAY_NUM BOT_RCON=127.0.0.1:$RCON_PORT

# inst_setup — папки экземпляра 1–4 по образцу основного: config.ini (свой write-data),
# моды (симлинки мода), player-data.json, steam_appid.txt.
inst_setup() {
  [[ $INST == 0 ]] && return 0
  local side
  for side in server client; do
    mkdir -p "$R/$side/mods" "$R/$side/saves"
    sed "s|^write-data=.*|write-data=$R/$side|" "$ROOT/run/$side/config.ini" > "$R/$side/config.ini"
    cp -P "$ROOT/run/$side/mods/"* "$R/$side/mods/" 2>/dev/null || true
    [[ -f $ROOT/run/$side/player-data.json ]] && cp --update=none "$ROOT/run/$side/player-data.json" "$R/$side/"
  done
  cp "$ROOT/run/client/steam_appid.txt" "$R/client/"
  mkdir -p "$R/client/home"
}
