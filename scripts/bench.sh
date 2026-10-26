#!/usr/bin/env bash
# Бенч: все e2e на каждой карте-эталоне — снимках run/snapshots/<сид>-0 (снимок грузится,
# тесты — scripts/test.sh). Лог карты — run/bench/<снимок>.log, итог — run/bench/summary.txt,
# статистика — test/e2e/stats.tsv (в «Когда» — снимок).
#   Карт — MAPS (по умолчанию 4) случайных из всех -0 (решение пользователя: не все каждый раз);
#   MAPS=all — все; MAPS="сид-0 сид-0" — эти
#   scripts/bench.sh            — карты по очереди (основной экземпляр)
#   scripts/bench.sh Walk       — только тесты по шаблону
#   PAR=4 scripts/bench.sh      — параллельно: карты по экземплярам 1–PAR (scripts/inst.sh),
#                                 каждый в своём окне, запуск через 8 с; после — остановлены
set -uo pipefail
cd "$(dirname "$0")/.."
mkdir -p run/bench
: > run/bench/summary.txt
PAR=${PAR:-0}
snaps=()
case ${MAPS:-4} in
  all) for d in run/snapshots/*-0/; do snaps+=("$(basename "$d")"); done ;;
  *[!0-9]*) read -ra snaps <<< "$MAPS" ;;
  *) mapfile -t snaps < <(for d in run/snapshots/*-0/; do basename "$d"; done | shuf -n "${MAPS:-4}") ;;
esac
echo "== карты: ${snaps[*]}" >> run/bench/summary.txt

# one ИНСТ СНИМОК [шаблон] — прогон карты на экземпляре, строка итога
one() {
  local inst=$1 snap=$2
  shift 2
  echo "== $snap $(date +%T)$([[ $PAR -gt 0 ]] && echo " (экземпляр $inst)")" >> run/bench/summary.txt
  INST=$inst SNAP=$snap SKIP_BUILD=$([[ $PAR -gt 0 ]] && echo 1 || echo 0) timeout 3600 scripts/test.sh "$@" > "run/bench/$snap.log" 2>&1
  local code=$?
  local pass fail skip
  pass=$(grep -c '^--- PASS' "run/bench/$snap.log")
  fail=$(grep -c '^--- FAIL' "run/bench/$snap.log")
  skip=$(grep -c '^--- SKIP' "run/bench/$snap.log")
  echo "   $snap: прошло $pass, упало $fail, пропущено $skip (код $code)$(grep '^--- FAIL' "run/bench/$snap.log" | sed 's/--- FAIL: Test/ /; s/ (.*//' | tr '\n' ' ')" >> run/bench/summary.txt
}

if [[ $PAR -gt 0 ]]; then
  go vet ./... && go vet -tags e2e ./test/e2e/ && go test ./... >/dev/null && go build -o bin/bot ./cmd/bot || exit 1
  for ((k = 1; k <= PAR; k++)); do
    (
      sleep $((8 * (k - 1))) # экземпляры — не разом: загрузка игры тяжёлая
      for ((i = k - 1; i < ${#snaps[@]}; i += PAR)); do one "$k" "${snaps[$i]}" "$@"; done
      INST=$k scripts/stop.sh
    ) &
  done
  wait
else
  for snap in "${snaps[@]}"; do one "${INST:-1}" "$snap" "$@"; done
fi
echo "== готово $(date +%T)" >> run/bench/summary.txt
cat run/bench/summary.txt
