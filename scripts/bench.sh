#!/usr/bin/env bash
# Бенч: все e2e на каждой карте-эталоне — снимках run/snapshots/<сид>-0 (по очереди: снимок
# грузится, тесты — scripts/test.sh). Лог карты — run/bench/<снимок>.log, итог — run/bench/summary.txt,
# статистика — test/e2e/stats.tsv (в «Когда» — снимок).
#   scripts/bench.sh            — все карты
#   scripts/bench.sh Walk       — только тесты по шаблону
set -uo pipefail
cd "$(dirname "$0")/.."
mkdir -p run/bench
: > run/bench/summary.txt
for d in run/snapshots/*-0/; do
  snap=$(basename "$d")
  echo "== $snap $(date +%T)" | tee -a run/bench/summary.txt
  SNAP=$snap timeout 3600 scripts/test.sh "$@" > "run/bench/$snap.log" 2>&1
  code=$?
  pass=$(grep -c '^--- PASS' "run/bench/$snap.log")
  fail=$(grep -c '^--- FAIL' "run/bench/$snap.log")
  skip=$(grep -c '^--- SKIP' "run/bench/$snap.log")
  echo "   прошло $pass, упало $fail, пропущено $skip (код $code)$(grep '^--- FAIL' "run/bench/$snap.log" | sed 's/--- FAIL: Test/ /; s/ (.*//' | tr '\n' ' ')" | tee -a run/bench/summary.txt
done
echo "== готово $(date +%T)" | tee -a run/bench/summary.txt
