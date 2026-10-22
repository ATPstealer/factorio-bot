#!/usr/bin/env bash
# Стадии на картах: со снимка <сид>-(N-1) выполнить стадию N, при успехе — снимок <сид>-N.
#   scripts/stage.sh N СИД…    — стадия N; карт несколько — параллельно, по экземпляру на карту
#                                (1, 2, …; scripts/inst.sh), каждый в своём окне, запуск через
#                                8 с; после — экземпляры остановлены
#   scripts/stage.sh N-M СИД…  — стадии N…M по очереди: следующая — когда все карты закончили
#                                прошлую; не выполнена хоть на одной — стоп (чинить, потом
#                                продолжить с неё же: scripts/stage.sh K-M …)
#   стадия 1 без снимка <сид>-0 — с новой карты SEED=<сид> (scripts/newmap.sh)
#   PAR=0 scripts/stage.sh …   — по картам подряд на экземпляре INST (по умолчанию 0); после — он остановлен
#   LIMIT=мин                  — лимит стадии (по умолчанию 120, разведка — 180)
# Лог: run/stages/sN-<сид>.log, итог — run/stages/summary.txt; код 0 — выполнена на всех картах.
set -uo pipefail
cd "$(dirname "$0")/.."
range=$1; shift
from=${range%-*} to=${range#*-}
mkdir -p run/stages
if [[ $from != "$to" ]]; then
  for ((n = from; n <= to; n++)); do
    if ! "$0" "$n" "$@"; then
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
limit=${LIMIT:-$([[ $n == 3 ]] && echo 180 || echo 120)}
code=0
for seed in "$@"; do
  log=run/stages/s$n-$seed.log
  rm -f "run/stages/ok-$n-$seed"
  echo "== стадия $n, $seed $(date +%T)" | tee -a run/stages/summary.txt
  if [[ $n == 1 && ! -f run/snapshots/$seed-0/bot.zip ]]; then
    rm -f "$R"/state*.json
    SEED=$seed timeout 600 scripts/newmap.sh > /dev/null 2>&1 || { echo "   карта $seed не создалась" | tee -a run/stages/summary.txt; code=1; continue; }
  else
    timeout 600 scripts/snapshot.sh load "$seed-$((n - 1))" > /dev/null 2>&1 || { echo "   нет снимка $seed-$((n - 1))" | tee -a run/stages/summary.txt; code=1; continue; }
  fi
  start=$(date +%s)
  if timeout $((limit * 60 + 300)) bin/bot -t "${limit}m" stage "$n" > "$log" 2>&1; then
    timeout 300 scripts/snapshot.sh save "$seed-$n" "сид $seed: после стадии $n" > /dev/null 2>&1
    touch "run/stages/ok-$n-$seed"
    echo "   выполнена за $(( ($(date +%s) - start) / 60 )) мин, снимок $seed-$n" | tee -a run/stages/summary.txt
  else
    code=1
    echo "   НЕ выполнена: $(tail -1 "$log" | cut -c17-200)" | tee -a run/stages/summary.txt
  fi
done
scripts/stop.sh > /dev/null 2>&1 # экземпляр после прогона не оставлять
exit $code
