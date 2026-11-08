#!/usr/bin/env bash
# Прогон с начала до конца: одна карта, стадии 1–68 подряд на одном экземпляре, игра между
# стадиями не перезапускается (запись видео, OBS: захват окна factorio-bot-N), окно игры не
# свёрнуто — мышью над ним не водить (курсор в игре сбивает клики бота). Снимки <сид>-N — без
# остановки игры: после стадии ждать первого автосейва сервера и взять его (snapshot.sh grab).
#   scripts/fullrun.sh [СИД]              — новая карта (СИД или случайная), стадии 1–68
#   FROM=N scripts/fullrun.sh СИД         — со стадии N: снимок <сид>-(N-1) (игра поднимается раз)
#   CONT=1 FROM=N scripts/fullrun.sh СИД  — со стадии N на текущей игре экземпляра (не со снимка)
#   TO=M                                  — до стадии M (68)
#   INST=N                                — экземпляр (1)
#   AUTOSAVE=мин                          — автосейв сервера на этот прогон (5; обычно 10): после
#                                           стадии бот стоит до автосейва — из него снимок
#   LIMIT=мин, RETRIES=2                  — как у stage.sh: лимит стадии (150, разведка — 1080);
#                                           стройка (с 10) упала — повтор: первый — на той же игре,
#                                           второй — игра поднимается заново на сохранении
# Пустые стадии (3–9, резерв) — без игры: снимок прошлой копируется. Ракета улетела — дальше
# игра не трогается (экран победы), снимки — копии. В конце игра не останавливается (и при
# неудаче: видно, где встало); остановить — INST=N scripts/stop.sh.
# Журналы — run/stages/sN-<сид>.log, итог — run/stages/summary.txt.
set -uo pipefail
cd "$(dirname "$0")/.."
source scripts/inst.sh
seed=${1:-}
from=${FROM:-1} to=${TO:-68}
autosave=${AUTOSAVE:-5}
retries=${RETRIES:-2}
mkdir -p run/stages
export SHOW=${SHOW:-1} # окно игры не сворачивать — его снимает OBS
jq ".autosave_interval = $autosave" scripts/server-settings.json > "$R/server-settings.json" || exit 1
export SERVER_SETTINGS=$R/server-settings.json

say() { echo "$*" | tee -a run/stages/summary.txt; }

# rocket_up — ракета уже улетела (RCON, только чтение)
rocket_up() { timeout 60 bin/bot force 2> /dev/null | grep -q '"rockets_launched"'; }

# alive — игра отвечает и клиент в ней
alive() { timeout 60 bin/bot player > /dev/null 2>&1; }

# snap N — снимок <сид>-N без остановки игры: первый автосейв после конца стадии («Saving
# finished» в журнале сервера), затем grab — он и состояния бота сейчас (бот стоит)
snap() {
  local lines i
  lines=$(wc -l < "$R/server.log")
  for ((i = 0; i < (autosave * 60 + 300) / 5; i++)); do
    if tail -n +$((lines + 1)) "$R/server.log" | grep -A5 "Saving to _autosave" | grep -q "Saving finished"; then
      scripts/snapshot.sh grab "$seed-$1" "сид $seed: после стадии $1 (fullrun, без остановки игры)"
      return
    fi
    sleep 5
  done
  echo "снимок $seed-$1: автосейва нет за $((autosave + 5)) мин" >&2
  return 1
}

# copy_snap N — снимок <сид>-N — копия <сид>-(N-1) (стадия без игры)
copy_snap() {
  local prev
  prev=$(snapdir "$seed-$(($1 - 1))")
  [[ -d $prev ]] || return 1
  rm -rf "run/snapshots/$seed-$1"
  cp -r "$prev" "run/snapshots/$seed-$1"
  echo "сид $seed: после стадии $1 ($2 — копия $seed-$(($1 - 1)))" > "run/snapshots/$seed-$1/NOTE.txt"
}

# начало: новая карта, снимок или текущая игра
go build -o bin/bot ./cmd/bot || exit 1
if [[ ${CONT:-0} == 1 ]]; then
  [[ -n $seed ]] || seed=$(jq -r .seed "$R/map.json")
  alive || NEW_MAP=0 timeout 600 scripts/newmap.sh > /dev/null || { say "fullrun: игра экземпляра $INST не поднялась"; exit 1; }
elif ((from == 1)); then
  rm -f "$R"/state*.json
  SEED=$seed NEW_MAP=1 timeout 600 scripts/newmap.sh > /dev/null || { say "fullrun: карта не создалась"; exit 1; }
  seed=$(jq -r .seed "$R/map.json")
else
  [[ -n $seed ]] || { echo "FROM=$from: нужен сид" >&2; exit 1; }
  timeout 600 scripts/snapshot.sh load "$seed-$((from - 1))" > /dev/null || { say "fullrun: нет снимка $seed-$((from - 1))"; exit 1; }
fi
say "== fullrun: сид $seed, стадии $from–$to, экземпляр $INST (окно factorio-bot-$INST), $(date '+%F %T')"
t_all=$(date +%s)

for ((n = from; n <= to; n++)); do
  log=run/stages/s$n-$seed.log
  start=$(date +%s)
  say "== стадия $n, $seed $(date +%T)"
  if bin/bot empty "$n" 2> /dev/null; then
    copy_snap "$n" "пустая" || { say "   нет снимка $seed-$((n - 1))"; exit 1; }
    echo "пустая стадия: снимок $seed-$((n - 1)) скопирован" > "$log"
    say "   выполнена (пустая), снимок $seed-$n"
    continue
  fi
  if ((n >= 67)) && rocket_up; then
    copy_snap "$n" "ракета уже улетела" || { say "   нет снимка $seed-$((n - 1))"; exit 1; }
    echo "ракета уже улетела: игра не тронута, снимок $seed-$((n - 1)) скопирован" > "$log"
    say "   выполнена: ракета уже улетела (снимок — копия $seed-$((n - 1)))"
    continue
  fi
  limit=${LIMIT:-$([[ $n == 1 ]] && echo 1080 || echo 150)}
  [[ ${CONT:-0} == 1 && $n == "$from" ]] && echo "== продолжение $(date +%T)" >> "$log" || : > "$log"
  ok=0
  for ((try = 0; try <= retries; try++)); do
    if ((try > 0)); then
      ((n >= 10)) || break # разведка и план — без повторов (как stage.sh)
      say "   повтор $try (причина — в журнале)"
      echo "== повтор $try $(date +%T)" >> "$log"
      if ((try > 1)) || ! alive; then
        # второй повтор или игра не отвечает — поднять заново на сохранении экземпляра
        scripts/stop.sh > /dev/null 2>&1
        NEW_MAP=0 timeout 600 scripts/newmap.sh > /dev/null 2>> "$log" || break
      fi
    fi
    go build -o bin/bot ./cmd/bot >> "$log" 2>&1 || break
    if timeout $(((limit > 1440 ? limit : 1440) * 60 + 300)) bin/bot -t "${limit}m" stage "$n" >> "$log" 2>&1; then
      ok=1
      break
    fi
  done
  if ((!ok)); then
    say "   НЕ выполнена: $(tail -1 "$log" | cut -c17-200)"
    say "== fullrun: стоп на стадии $n; продолжить — CONT=1 FROM=$n scripts/fullrun.sh $seed"
    exit 1
  fi
  if ! snap "$n" > /dev/null 2>> "$log"; then
    say "   выполнена, но снимок $seed-$n не сделан (автосейва нет) — дальше без него"
  else
    say "   выполнена за $(( ($(date +%s) - start) / 60 )) мин, снимок $seed-$n"
  fi
done
say "== fullrun: сид $seed, стадии $from–$to выполнены за $(( ($(date +%s) - t_all) / 3600 )) ч $(( ($(date +%s) - t_all) / 60 % 60 )) мин; игра не остановлена"
