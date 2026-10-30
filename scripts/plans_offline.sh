#!/bin/bash
# Планы без игры на всех сохранённых картах (run/worlds/*.gob): стадии 2–TO (TO=6) подряд на
# каждой, J (6) карт параллельно; состояние и картинки каждой — OUT/<сид>/ (OUT — папка вывода,
# по умолчанию run/plans-offline), итог — OUT/summary.txt. Для проверки стадий планирования
# после правок (stages.md, стадия 6): прошла ли на всех картах, где база ложилась.
#   scripts/plans_offline.sh                 # все карты до стадии 6
#   TO=41 SEEDS="427965634 267145981" scripts/plans_offline.sh   # эти карты, и стадии 10–41 на бумаге
cd "$(dirname "$0")/.."
OUT=${OUT:-run/plans-offline}
TO=${TO:-6}
J=${J:-6}
mkdir -p "$OUT"
: > "$OUT/summary.txt"
seeds=${SEEDS:-$(ls run/worlds/*.gob | sed 's|.*/||; s|\.gob||')}
one() {
  local seed=$1 d=$OUT/$1
  mkdir -p "$d"
  BOT_RUN=$d timeout 1500 bin/bot -t 24m rough "run/worlds/$seed.gob" "$TO" > "$d/out.log" 2>&1
  echo "$seed: код $? — $(tail -1 "$d/out.log" | cut -c17-160)" >> "$OUT/summary.txt"
}
export -f one
export OUT TO
printf '%s\n' $seeds | xargs -P "$J" -I{} bash -c 'one {}'
sort "$OUT/summary.txt"
