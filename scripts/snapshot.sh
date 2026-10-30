#!/usr/bin/env bash
# Снимки игры (экономия времени на тестах): сейв сервера + состояния бота + сид.
#   scripts/snapshot.sh save ИМЯ ["описание"] — остановить игру (сервер сохраняет bot.zip),
#                                   скопировать в run/snapshots/ИМЯ/ (+ NOTE.txt), поднять игру
#   scripts/snapshot.sh grab ИМЯ ["описание"] — без остановки игры: последний автосейв
#                                   сервера (_autosave*, раз в 10 мин) + состояния бота сейчас
#   scripts/snapshot.sh load ИМЯ  — остановить игру, вернуть снимок, поднять
#   scripts/snapshot.sh list
# По RCON ничего не пишется: сохраняет сам сервер при штатной остановке.
#   INST=N …                         — экземпляр N (scripts/inst.sh); снимки общие
# Папки (решение пользователя 30.10): run/snapshots/tests/ — карты -0 для e2e и бенча,
# run/snapshots/last_run/ — все стадии карт, прошедших 1–41, корень — снимки идущих прогонов;
# снимок ищется во всех (snapdir из inst.sh).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/inst.sh
S=$(snapdir "${2:-}")
# save/grab: снимок в tests/ или last_run/ не перезаписывать — новый ложится в корень (снимки
# идущих прогонов); перекладывать в last_run — руками. Замер стадии 18 на 427965634 затёр
# last_run/427965634-18 (30.10)
case "${1:-}" in save | grab) [[ $S == */tests/* || $S == */last_run/* ]] && S="run/snapshots/${2:-}" ;; esac
case "${1:-}" in
save)
  [[ -n "${2:-}" ]] || { echo "нужно имя" >&2; exit 1; }
  t0=$(date +%s)
  # карта не сохранилась (сервер завис и убит) — снимок не делать: старый bot.zip выдал бы
  # себя за новый
  scripts/stop.sh || { echo "снимок $2 не сохранён: сервер не сохранил карту" >&2; exit 1; }
  (( $(stat -c %Y $R/server/saves/bot.zip) >= t0 )) || { echo "снимок $2 не сохранён: bot.zip старый" >&2; exit 1; }
  rm -rf "$S" && mkdir -p "$S"
  cp $R/server/saves/bot.zip $R/map.json "$S"/
  cp $R/state*.json "$S"/ 2>/dev/null || true
  printf '%s\nсохранён: %s\n' "${3:-}" "$(date -Is)" > "$S/NOTE.txt"
  # снимок уже сохранён — игра не поднялась (мод не грузится после обновления игры) — не беда
  # для снимка: сказать, но код 0
  NEW_MAP=0 scripts/newmap.sh >/dev/null || echo "снимок $2 сохранён, но игра не поднялась" >&2
  echo "снимок $2: $(cat "$S/map.json")"
  ;;
grab)
  [[ -n "${2:-}" ]] || { echo "нужно имя" >&2; exit 1; }
  auto=$(ls -t $R/server/saves/_autosave*.zip 2>/dev/null | head -1)
  [[ -n "$auto" ]] || { echo "нет автосейва" >&2; exit 1; }
  rm -rf "$S" && mkdir -p "$S"
  cp "$auto" "$S"/bot.zip
  cp $R/map.json "$S"/
  cp $R/state*.json "$S"/ 2>/dev/null || true
  printf '%s\nсохранён: %s (автосейв %s)\n' "${3:-}" "$(date -Is)" "$(date -r "$auto" +%T)" > "$S/NOTE.txt"
  echo "снимок $2 из $(basename "$auto"): $(cat "$S/map.json")"
  ;;
load)
  [[ -f "$S/bot.zip" ]] || { echo "нет снимка $S" >&2; exit 1; }
  scripts/stop.sh
  rm -f $R/state*.json
  inst_setup
  cp "$S"/bot.zip $R/server/saves/bot.zip
  cp "$S"/map.json $R/
  cp "$S"/state*.json $R/ 2>/dev/null || true
  NEW_MAP=0 scripts/newmap.sh >/dev/null
  echo "загружен снимок $2: $(cat "$R/map.json")"
  ;;
list)
  for d in run/snapshots/*/ run/snapshots/*/*/; do
    [[ -f "$d/map.json" ]] || continue
    d=${d%/}
    echo "${d#run/snapshots/}: $(cat "$d/map.json")"
    if [[ -f "$d/NOTE.txt" ]]; then sed "s/^/    /" "$d/NOTE.txt"; fi
  done
  ;;
*)
  echo "scripts/snapshot.sh save|grab|load ИМЯ | list" >&2; exit 1
  ;;
esac
