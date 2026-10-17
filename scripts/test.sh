#!/usr/bin/env bash
# Регрессия: юнит-тесты + e2e всех стадий. e2e требуют запущенного scripts/run.sh.
#   scripts/test.sh            — всё
#   scripts/test.sh Stage0     — только тесты, чьё имя совпадает с шаблоном (-run)
set -euo pipefail
cd "$(dirname "$0")/.."
go vet ./... && go vet -tags e2e ./test/e2e/
go test ./...
go test -tags e2e -count=1 ${1:+-run "$1"} -v ./test/e2e/ | grep -E '^(=== RUN|--- |    |PASS|FAIL|ok)' | grep -v '^=== RUN'
