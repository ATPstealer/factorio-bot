#!/usr/bin/env bash
# Регрессия: юнит-тесты + e2e всех стадий. e2e требуют запущенного scripts/run.sh.
#   scripts/test.sh            — всё
#   scripts/test.sh Stage0     — только тесты, чьё имя совпадает с шаблоном (-run)
#   E2E_SLOW=1 scripts/test.sh — плюс долгие тесты (улитка из печей)
# Каждый прогон — на новой случайной карте (scripts/newmap.sh); сид — в выводе и run/map.json.
#   SEED=123 scripts/test.sh   — воспроизвести на конкретной карте
#   KEEP_MAP=1 scripts/test.sh — на текущей карте, без пересоздания
set -euo pipefail
cd "$(dirname "$0")/.."
go vet ./... && go vet -tags e2e ./test/e2e/
go test ./...
go build -o bin/bot ./cmd/bot
if [[ "${KEEP_MAP:-0}" != 1 ]]; then
  scripts/newmap.sh
fi
echo "карта: $(cat run/map.json 2>/dev/null)"
go test -tags e2e -count=1 -timeout 30m ${1:+-run "$1"} -v ./test/e2e/ | grep -E '^(=== RUN|--- |    |PASS|FAIL|ok)' | grep -v '^=== RUN'
