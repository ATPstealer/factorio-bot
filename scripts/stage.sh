#!/usr/bin/env bash
# Стадии на картах: со снимка <сид>-(N-1) выполнить стадию N, при успехе — снимок <сид>-N.
#   scripts/stage.sh N СИД…    — стадия N; карт несколько — параллельно, по экземпляру на карту
#                                (1, 2, …; scripts/inst.sh), каждый в своём окне, запуск через
#                                8 с; после — экземпляры остановлены
#   scripts/stage.sh N-M СИД…  — стадии N…M по очереди: следующая — когда все карты закончили
#                                прошлую; не выполнена хоть на одной — стоп (чинить, потом
#                                продолжить с неё же: scripts/stage.sh K-M …)
#   стадия 1 без снимка <сид>-0 — с новой карты SEED=<сид> (scripts/newmap.sh)
#   пустая стадия (резерв, bin/bot empty N) — без игры: снимок <сид>-(N-1) копируется в <сид>-N
#   PAR=0 scripts/stage.sh …   — по картам подряд на экземпляре INST (по умолчанию 1); после — он остановлен
#   LIMIT=мин                  — лимит стадии (по умолчанию 150 — стадия 17 шла дольше 2 ч; разведка — 1080)
#   CONT=1                     — первая стадия — продолжение на текущем сохранении экземпляра (не
#                                со снимка): стадия, упавшая по лимиту, достраивает недостающее,
#                                а не начинает заново; журнал дописывается
#   PLAN=1 scripts/stage.sh N-M СИД… — только стадии планирования N…M подряд (bin/bot plans), со
#                                снимка <сид>-(N-1), без стройки между ними: план прошлых
#                                считается построенным; снимки не сохраняются (новые картинки —
#                                run/stages/zones-, plan-, plan-green-<сид>.png); карты — параллельно
# Лог: run/stages/sN-<сид>.log, итог — run/stages/summary.txt; код 0 — выполнена на всех картах.
set -uo pipefail
cd "$(dirname "$0")/.."
range=$1; shift
from=${range%-*} to=${range#*-}
mkdir -p run/stages
if [[ ${PLAN:-0} == 1 ]]; then
  go build -o bin/bot ./cmd/bot || exit 1
  k=0
  for seed in "$@"; do
    k=$((k + 1))
    (
      sleep $((8 * (k - 1)))
      log=run/stages/plans-$seed.log
      rm -f "run/stages/ok-plans-$seed"
      INST=$k bash -c "source scripts/inst.sh
        timeout 600 scripts/snapshot.sh load $seed-$((from - 1)) > /dev/null 2>&1 || { echo нет снимка $seed-$((from - 1)) > $log; exit 1; }
        timeout $(( ${LIMIT:-60} * 60 + 300 )) bin/bot -t ${LIMIT:-60}m plans $from $to > $log 2>&1" && touch "run/stages/ok-plans-$seed"
      INST=$k scripts/stop.sh > /dev/null 2>&1
      if [[ -f run/stages/ok-plans-$seed ]]; then
        echo "== планы $from–$to, $seed: выполнены" | tee -a run/stages/summary.txt
      else
        echo "== планы $from–$to, $seed: НЕ выполнены: $(tail -1 "$log" | cut -c17-200)" | tee -a run/stages/summary.txt
      fi
    ) &
  done
  wait
  for seed in "$@"; do [[ -f run/stages/ok-plans-$seed ]] || exit 1; done
  exit 0
fi
if [[ $from != "$to" ]]; then
  for ((n = from; n <= to; n++)); do
    if ! CONT=$([[ $n == "$from" ]] && echo "${CONT:-0}" || echo 0) "$0" "$n" "$@"; then
      echo "== стоп: стадия $n выполнена не на всех картах" | tee -a run/stages/summary.txt
      exit 1
    fi
  done
  echo "== стадии $from–$to выполнены на всех картах" | tee -a run/stages/summary.txt
  exit 0
fi
n=$from
go build -o bin/bot ./cmd/bot || exit 1
if [[ ${PAR:-1} != 0 && $# -gt 1 ]]; then
  k=0
  for seed in "$@"; do
    k=$((k + 1))
    (
      sleep $((8 * (k - 1))) # экземпляры — не разом: загрузка игры тяжёлая
      INST=$k PAR=0 "$0" "$n" "$seed" > /dev/null
      INST=$k scripts/stop.sh > /dev/null 2>&1
    ) &
  done
  wait
  tail -$(($# * 2)) run/stages/summary.txt
  for seed in "$@"; do [[ -f run/stages/ok-$n-$seed ]] || exit 1; done
  exit 0
fi
source scripts/inst.sh
limit=${LIMIT:-$([[ $n == 1 ]] && echo 1080 || echo 150)}
code=0
for seed in "$@"; do
  log=run/stages/s$n-$seed.log
  rm -f "run/stages/ok-$n-$seed"
  echo "== стадия $n, $seed $(date +%T)" | tee -a run/stages/summary.txt
  # пустая стадия (резерв) — игру не запускать: снимок прошлой стадии под её номером
  if bin/bot empty "$n" 2> /dev/null; then
    if [[ -d $(snapdir "$seed-$((n - 1))") ]]; then
      rm -rf "run/snapshots/$seed-$n"
      cp -r "$(snapdir "$seed-$((n - 1))")" "run/snapshots/$seed-$n"
      echo "сид $seed: после стадии $n (пустая — копия $seed-$((n - 1)))" > "run/snapshots/$seed-$n/NOTE.txt"
      echo "пустая стадия: снимок $seed-$((n - 1)) скопирован" > "$log"
      touch "run/stages/ok-$n-$seed"
      echo "   выполнена за 0 мин (пустая), снимок $seed-$n" | tee -a run/stages/summary.txt
    else
      code=1
      echo "   нет снимка $seed-$((n - 1))" | tee -a run/stages/summary.txt
    fi
    continue
  fi
  if [[ ${CONT:-0} == 1 ]]; then
    # продолжение: игра — на сохранении экземпляра, состояние стадий — его же
    NEW_MAP=0 timeout 600 scripts/newmap.sh > /dev/null 2>> "run/stages/load-$seed.log" || { echo "   нет сохранения экземпляра $INST" | tee -a run/stages/summary.txt; code=1; continue; }
  elif [[ $n == 1 && ! -f $(snapdir "$seed-0")/bot.zip ]]; then
    rm -f "$R"/state*.json
    SEED=$seed timeout 600 scripts/newmap.sh > /dev/null 2>&1 || { echo "   карта $seed не создалась" | tee -a run/stages/summary.txt; code=1; continue; }
  else
    # загрузка — до 3 раз: игра сразу после остановки прошлой стадии однажды не поднялась
    # (1028292478, стадия 10 — «нет снимка», а снимок был); ошибки — в run/stages/load-<сид>.log
    loaded=0
    for try in 1 2 3; do
      if timeout 600 scripts/snapshot.sh load "$seed-$((n - 1))" > /dev/null 2>> "run/stages/load-$seed.log"; then
        loaded=1
        break
      fi
      echo "стадия $n: загрузка $seed-$((n - 1)) не удалась ($try)" >> "run/stages/load-$seed.log"
      sleep 15
    done
    [[ $loaded == 1 ]] || { echo "   нет снимка $seed-$((n - 1))" | tee -a run/stages/summary.txt; code=1; continue; }
  fi
  start=$(date +%s)
  [[ ${CONT:-0} == 1 ]] && echo "== продолжение $(date +%T)" >> "$log" || : > "$log"
  # лимит стадии — у бота (жирной стадии он сам даёт полторы оценки); timeout — страховка от
  # зависания: сутки, но не меньше лимита
  if timeout $(( (limit > 1440 ? limit : 1440) * 60 + 300 )) bin/bot -t "${limit}m" stage "$n" >> "$log" 2>&1; then
    if timeout 300 scripts/snapshot.sh save "$seed-$n" "сид $seed: после стадии $n" > /dev/null 2>> "$log"; then
      touch "run/stages/ok-$n-$seed"
      echo "   выполнена за $(( ($(date +%s) - start) / 60 )) мин, снимок $seed-$n" | tee -a run/stages/summary.txt
    else
      # стадия прошла, но снимок не сохранился (сервер завис на сохранении) — следующей
      # стадии не с чего начинать: не выполнена
      code=1
      rm -rf "run/snapshots/$seed-$n" # только в корне: снимок в last_run/ с тем же номером не трогать
      echo "   НЕ выполнена: стадия $n прошла, но снимок $seed-$n не сохранился" | tee -a run/stages/summary.txt
    fi
  else
    code=1
    echo "   НЕ выполнена: $(tail -1 "$log" | cut -c17-200)" | tee -a run/stages/summary.txt
  fi
done
scripts/stop.sh > /dev/null 2>&1 # экземпляр после прогона не оставлять
exit $code
