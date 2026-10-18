#!/usr/bin/env bash
# Регрессия: юнит-тесты + e2e всех стадий. e2e требуют запущенного scripts/run.sh.
#   scripts/test.sh            — всё
#   scripts/test.sh Stage0     — только тесты, чьё имя совпадает с шаблоном (-run)
#   scripts/test.sh            — тесты отдельных функций (быстро)
#   COMPLEX=1 scripts/test.sh  — плюс комплексные сценарии (старт, станция, фабрика науки…)
# По умолчанию — на текущей карте (экономия времени); игра перезапускается на той же карте,
# только если не запущена или изменился мод. Сид — в выводе и run/map.json.
#   NEW_MAP=1 scripts/test.sh  — на новой случайной карте (финальная регрессия стадии)
#   SEED=123 scripts/test.sh   — на конкретной карте
#   SNAP=start scripts/test.sh — с снимка (scripts/snapshot.sh): не повторять долгий старт
#   SAVE_AS=start scripts/test.sh Stage7_Start — после зелёного прогона сохранить снимок
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
[[ "${COMPLEX:-0}" == 1 ]] && export E2E_COMPLEX=1
set +e
go test -tags e2e -count=1 -timeout 150m ${1:+-run "$1"} -v ./test/e2e/ | grep -E '^(=== RUN|--- |    |PASS|FAIL|ok)' | grep -v '^=== RUN'
code=${PIPESTATUS[0]}
if [[ $code == 0 && -n "${SAVE_AS:-}" ]]; then
  scripts/snapshot.sh save "$SAVE_AS"
fi
exit $code
