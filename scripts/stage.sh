#!/usr/bin/env bash
# Стадия N на картах: со снимка <сид>-(N-1) выполнить стадию, при успехе — снимок <сид>-N.
#   scripts/stage.sh N СИД…     — лог: run/stages/sN-<сид>.log, итог — run/stages/summary.txt
#   INST=N scripts/stage.sh …  — на экземпляре N (scripts/inst.sh), например по карте на экземпляр
set -uo pipefail
cd "$(dirname "$0")/.."
source scripts/inst.sh
n=$1; shift
mkdir -p run/stages
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
