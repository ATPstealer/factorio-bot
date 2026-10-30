#!/bin/bash
# Замер коротких походов (ускорение, ночь 30–31.10): INST=N, снимок уже загружен; N=число
# походов (16) по квадрату со стороной D (12) тайлов вокруг текущей точки, одним процессом
# бота (замеры задержки и тормозного пути копятся, как в стадии); в выводе — каждая строка
# «ходьба:» и итог: тайлы/с, средние остановка и доводка.
#   INST=4 scripts/snapshot.sh load 46082668-0; INST=4 N=16 D=12 scripts/walkshort.sh
cd /home/mav/GolandProjects/Factorio
source scripts/inst.sh
N=${N:-16}; D=${D:-12}; BOT=${BOT:-bin/bot}  # BOT=путь — другой бинарник (сравнение до/после)
p=$(timeout 30 bin/bot player 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin); print(d['position']['x'], d['position']['y'])")
set -- $p; x0=${1%.*}; y0=${2%.*}
pts=()
for ((i = 0; i < N; i++)); do
  case $((i % 4)) in 0) pts+=($((x0 + D)) $y0) ;; 1) pts+=($((x0 + D)) $((y0 + D))) ;; 2) pts+=($x0 $((y0 + D))) ;; 3) pts+=($x0 $y0) ;; esac
done
out=$(timeout $((N * 60)) $BOT -t $((N))m walk "${pts[@]}" 2>&1 | grep -E "ходьба: маршрут|маршрут [0-9.]+ тайлов|стою в")
echo "$out" | grep "ходьба:\|стою в"
echo "$out" | python3 -c '
import re,sys
t=s=st=ap=0;n=0;tiles=0
for ln in sys.stdin:
    m=re.search(r"маршрут ([\d.]+) тайлов",ln)
    if m: tiles=float(m.group(1)); continue
    m=re.search(r"ходьба: маршрут ([\d.]+) с, остановка ([\d.]+) с, доводка ([\d.]+) с",ln)
    if m: n+=1; t+=tiles; s+=float(m.group(1)); st+=float(m.group(2)); ap+=float(m.group(3))
if n: print(f"походов {n}: {t:.0f} тайлов за {s+st+ap:.1f} с = {t/(s+st+ap):.2f} т/с; на поход: маршрут {s/n:.2f} с, остановка {st/n:.2f} с, доводка {ap/n:.2f} с")
'
