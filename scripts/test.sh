#!/usr/bin/env bash
# Тесты: юнит + e2e (e2e требуют запущенной игры — scripts/run.sh/newmap.sh).
#   scripts/test.sh              — все e2e
#   scripts/test.sh Walk         — один тест/шаблон (-run), на текущей карте
# Карта: по умолчанию текущая (игра перезапускается, только если не запущена или изменился
# мод); NEW_MAP=1 — новая случайная, SEED=… — конкретная, SNAP=имя — со снимка;
# SAVE_AS=имя — снимок после зелёного прогона.
set -euo pipefail
cd "$(dirname "$0")/.."
go vet ./... && go vet -tags e2e ./test/e2e/
go test ./...
go build -o bin/bot ./cmd/bot

mod=$(find mod/botbridge -type f | sort | xargs cat | md5sum)
if [[ -n "${SNAP:-}" ]]; then
  scripts/snapshot.sh load "$SNAP"
elif [[ "${NEW_MAP:-0}" == 1 || -n "${SEED:-}" ]]; then
  scripts/newmap.sh
elif ! timeout 10 bin/bot ping >/dev/null 2>&1 || [[ "$mod" != "$(cat run/mod.hash 2>/dev/null)" ]]; then
  NEW_MAP=0 scripts/newmap.sh
fi
echo "карта: $(cat run/map.json 2>/dev/null)"

# e2e по шаблону: вывод — только итоги и логи тестов; код — go test
e2e() {
  set +e
  go test -tags e2e -count=1 -timeout 150m -run "$1" -v ./test/e2e/ | tee run/e2e-last.log | grep --line-buffered -E '^(=== RUN|--- |    |PASS|FAIL|ok|panic|fatal|\[signal|\t/)' | grep --line-buffered -v '^=== RUN'
  local code=${PIPESTATUS[0]}
  scripts/test_stats.py run/e2e-last.log "$(date "+%F %R")${SNAP:+ ($SNAP)}" # статистика — test/e2e/stats.tsv и таблица в README
  set -e
  return "$code"
}

code=0
e2e "${1:-.}" || code=$?
if [[ $code == 0 && -n "${SAVE_AS:-}" ]]; then
  scripts/snapshot.sh save "$SAVE_AS"
fi
exit $code
