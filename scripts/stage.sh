#!/usr/bin/env bash
# Стадия N на картах: со снимка <сид>-(N-1) выполнить стадию, при успехе — снимок <сид>-N.
#   scripts/stage.sh N СИД…     — лог: run/stages/sN-<сид>.log, итог — run/stages/summary.txt
#   несколько карт — параллельно, по экземпляру на карту (1, 2, …; scripts/inst.sh), каждый в
#   своём окне, запуск через 8 с; после — экземпляры остановлены
#   PAR=0 scripts/stage.sh …   — по картам подряд на экземпляре INST (по умолчанию 0)
set -uo pipefail
cd "$(dirname "$0")/.."
n=$1; shift
mkdir -p run/stages
if [[ ${PAR:-1} != 0 && $# -gt 1 ]]; then
  go build -o bin/bot ./cmd/bot || exit 1
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
  exit 0
fi
source scripts/inst.sh
for seed in "$@"; do
  log=run/stages/s$n-$seed.log
  echo "== стадия $n, $seed $(date +%T)" | tee -a run/stages/summary.txt
  timeout 600 scripts/snapshot.sh load "$seed-$((n - 1))" > /dev/null 2>&1 || { echo "   нет снимка $seed-$((n - 1))" | tee -a run/stages/summary.txt; continue; }
  start=$(date +%s)
  if timeout 3600 bin/bot -t 60m stage "$n" > "$log" 2>&1; then
    timeout 300 scripts/snapshot.sh save "$seed-$n" "сид $seed: после стадии $n" > /dev/null 2>&1
    echo "   выполнена за $(( ($(date +%s) - start) / 60 )) мин, снимок $seed-$n" | tee -a run/stages/summary.txt
  else
    echo "   НЕ выполнена: $(tail -1 "$log" | cut -c17-200)" | tee -a run/stages/summary.txt
  fi
done
