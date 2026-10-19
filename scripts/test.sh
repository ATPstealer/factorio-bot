#!/usr/bin/env bash
# Тесты: юнит + e2e (e2e требуют запущенной игры — scripts/run.sh/newmap.sh). Наборы:
#   scripts/test.sh              — база (SET=base): функции, без которых ничего не работает
#   scripts/test.sh Walk         — один тест/шаблон (-run), на текущей карте
#   SET=game scripts/test.sh     — прохождение игры по шагам на одной карте; номер шага =
#                                  пункт плана (7.0, 7.2 …); после шага — снимок game-7.N
#   SET=game GAME_FROM=7.7 …     — продолжить с шага 7.7 (грузит снимок предыдущего, game-7.6)
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
# шаги прохождения по порядку (номер = пункт плана) и описания их снимков
steps=(7.0 7.2 7.3 7.4 7.5 7.6 7.7 7.8 8.1 8.2 8.3 8.5 8.6 8.7 8.8)
declare -A note=(
  [7.0]="7.0 старт: 50 дерева, 200 камня, точки на бурах (5 угольных пар, 5 железо, 5 медь, камень 4→сундук)"
  [7.2]="7.2 + электричество: станция у воды (бойлер с сундуком-кормушкой), лаборатории, столбы"
  [7.3]="7.3 + изучена automation (наука руками)"
  [7.4]="7.4 + сборщик с рецептом шестерён"
  [7.5]="7.5 + изучены электробуры"
  [7.6]="7.6 + фабрика красной науки (электробуры, 4 пары сборщиков, 6 лабораторий)"
  [7.7]="7.7 + проверка «пропало питание» (станция дозаправлена/расширена)"
  [7.8]="7.8 красная наука для ракеты изучена: automation, logistics, logistic-science-pack, steel-processing, radar (конец стадии 7)"
  [8.1]="8.1 + блок зелёной науки (кабель, схемы, инсертеры, шестерни, ленты → 2 сборщика), ряды плавки 3+1"
  [8.2]="8.2 + изучено на красной+зелёной до oil-gathering (fluid-handling, engine, automation-2)"
  [8.3]="8.3 + сталь: 3 столбика бур → печь → печь (сталь) → сундук"
  [8.5]="8.5 + нефть: насосы → нефтезавод → химзаводы (пластик, сера) в сундуки"
  [8.6]="8.6 + угольная шахта: 3 пары электробуров с сундуками, кормушки бойлеров по 300"
  [8.7]="8.7 + лента пластика с нефти на базу (буфер — сундук у фабрики)"
  [8.8]="8.8 + блок синей науки, синяя лентой в лаборатории")
GAME_FROM=${GAME_FROM:-${steps[0]}}
[[ $SET == all ]] && GAME_FROM=${steps[0]}
from=-1
for i in "${!steps[@]}"; do [[ ${steps[$i]} == "$GAME_FROM" ]] && from=$i; done
if [[ $from -lt 0 ]]; then echo "GAME_FROM: один из ${steps[*]}" >&2; exit 2; fi
if [[ $SET == game || $SET == all ]] && [[ $from -gt 0 && -z "${1:-}" ]]; then
  SNAP=game-${steps[$((from - 1))]}
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
  go test -tags e2e -count=1 -timeout 150m -run "$1" -v ./test/e2e/ | grep -E '^(=== RUN|--- |    |PASS|FAIL|ok|panic|fatal|\[signal|\t/)' | grep -v '^=== RUN'
  local code=${PIPESTATUS[0]}
  set -e
  return "$code"
}

game() {
  local i n
  for ((i = from; i < ${#steps[@]}; i++)); do
    n=${steps[$i]}
    if [[ $i == 0 && "${NEW_MAP:-0}" != 1 && -z "${SEED:-}" ]]; then
      scripts/newmap.sh # старт — только с нуля
    fi
    e2e "^TestGame${n/./_}_" || return 1
    scripts/snapshot.sh save "game-$n" "${note[$n]}"
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
