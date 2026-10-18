#!/usr/bin/env bash
# Снимки игры (экономия времени на тестах): сейв сервера + состояния бота + сид.
#   scripts/snapshot.sh save ИМЯ  — остановить игру (сервер сохраняет bot.zip), скопировать
#                                   в run/snapshots/ИМЯ/, поднять игру на той же карте
#   scripts/snapshot.sh load ИМЯ  — остановить игру, вернуть снимок, поднять
#   scripts/snapshot.sh list
# По RCON ничего не пишется: сохраняет сам сервер при штатной остановке.
set -euo pipefail
cd "$(dirname "$0")/.."
R=run
S=$R/snapshots/${2:-}
case "${1:-}" in
save)
  [[ -n "${2:-}" ]] || { echo "нужно имя" >&2; exit 1; }
  scripts/stop.sh
  rm -rf "$S" && mkdir -p "$S"
  cp $R/server/saves/bot.zip $R/map.json "$S"/
  cp $R/state*.json "$S"/ 2>/dev/null || true
  NEW_MAP=0 scripts/newmap.sh >/dev/null
  echo "снимок $2: $(cat "$S/map.json")"
  ;;
load)
  [[ -f "$S/bot.zip" ]] || { echo "нет снимка $S" >&2; exit 1; }
  scripts/stop.sh
  rm -f $R/state*.json
  cp "$S"/bot.zip $R/server/saves/bot.zip
  cp "$S"/map.json $R/
  cp "$S"/state*.json $R/ 2>/dev/null || true
  NEW_MAP=0 scripts/newmap.sh >/dev/null
  echo "загружен снимок $2: $(cat $R/map.json)"
  ;;
list)
  for d in $R/snapshots/*/; do [[ -d "$d" ]] && echo "$(basename "$d"): $(cat "$d/map.json")"; done
  ;;
*)
  echo "scripts/snapshot.sh save|load ИМЯ | list" >&2; exit 1
  ;;
esac
