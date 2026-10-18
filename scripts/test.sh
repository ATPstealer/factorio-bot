#!/usr/bin/env bash
# Тесты: юнит + e2e (e2e требуют запущенной игры — scripts/run.sh/newmap.sh). Наборы:
#   scripts/test.sh              — база (SET=base): функции, без которых ничего не работает
#   scripts/test.sh Walk         — один тест/шаблон (-run), на текущей карте
#   SET=game scripts/test.sh     — прохождение игры по шагам (TestGame01..08) на одной карте;
#                                  после каждого шага — снимок game-0N (scripts/snapshot.sh)
#   SET=game GAME_FROM=4 …       — продолжить с шага 4 (грузит снимок game-03)
#   SET=extra scripts/test.sh    — прочие сценарии (старая линия плавки, «улитка»)
#   SET=all scripts/test.sh      — финальная регрессия стадии: база, прочие, прохождение с нуля
# Карта: по умолчанию текущая (игра перезапускается, только если не запущена или изменился
# мод); NEW_MAP=1 — новая случайная, SEED=… — конкретная, SNAP=имя — со снимка.
# Шаг 1 прохождения (старт) всегда на новой карте.
set -euo pipefail
cd "$(dirname "$0")/.."
go vet ./... && go vet -tags e2e ./test/e2e/
go test ./...
go build -o bin/bot ./cmd/bot

SET=${SET:-base}
GAME_FROM=${GAME_FROM:-1}
[[ $SET == all ]] && GAME_FROM=1
if [[ $SET == game || $SET == all ]] && [[ $GAME_FROM -gt 1 && -z "${1:-}" ]]; then
  SNAP=game-0$((GAME_FROM - 1))
fi

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
  go test -tags e2e -count=1 -timeout 150m -run "$1" -v ./test/e2e/ | grep -E '^(=== RUN|--- |    |PASS|FAIL|ok)' | grep -v '^=== RUN'
  local code=${PIPESTATUS[0]}
  set -e
  return "$code"
}

# описания снимков шагов прохождения
note=(""
  "Прохождение 1: старт — 50 дерева, 200 камня, точки на бурах (5 угольных пар, 5 железо, 5 медь, камень 4→сундук)"
  "Прохождение 2: + электричество — станция у воды (бойлер с сундуком-кормушкой), 2 лаборатории, столбы"
  "Прохождение 3: + изучена automation (наука руками)"
  "Прохождение 4: + сборщик с рецептом шестерён (проверка рецептов)"
  "Прохождение 5: + изучены электробуры"
  "Прохождение 6: + фабрика красной науки построена (электробуры, 4 пары сборщиков, 6 лабораторий)"
  "Прохождение 7: + проверка «пропало питание» (станция дозаправлена/расширена)"
  "Прохождение 8: всё по красной изучено (конец стадии 7)")

game() {
  local n
  for n in $(seq "$GAME_FROM" 8); do
    if [[ $n == 1 && "${NEW_MAP:-0}" != 1 && -z "${SEED:-}" ]]; then
      scripts/newmap.sh # старт — только с нуля
    fi
    e2e "^TestGame0${n}_" || return 1
    scripts/snapshot.sh save "game-0$n" "${note[$n]}"
  done
}

code=0
if [[ -n "${1:-}" ]]; then
  e2e "$1" || code=$?
else
  case $SET in
  base) e2e '^TestBase_' || code=$? ;;
  extra) e2e '^TestExtra_' || code=$? ;;
  game) game || code=$? ;;
  all)
    e2e '^TestBase_' || code=$?
    e2e '^TestExtra_' || code=$?
    game || code=$?
    ;;
  *) echo "SET: base | game | extra | all" >&2; exit 2 ;;
  esac
fi
if [[ $code == 0 && -n "${SAVE_AS:-}" ]]; then
  scripts/snapshot.sh save "$SAVE_AS"
fi
exit $code
