#!/bin/bash
# Замер скорости бега (ускорение, ночь 30–31.10): INST=N, снимок уже загружен; для каждого BOT_AHEAD
# (дальность прицела ходьбы, act.go aheadOpen) — туда-обратно по 120 тайлов, в выводе тайлы/с.
#   INST=4 scripts/snapshot.sh load 46082668-0; INST=4 scripts/walkspeed.sh
cd /home/mav/GolandProjects/Factorio
source scripts/inst.sh
p=$(timeout 30 bin/bot player 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin); print(d['position']['x'], d['position']['y'])")
set -- $p; x0=${1%.*}; y0=${2%.*}
for a in 1.5 4 8 1.5; do
  for dx in 120 -120; do
    tx=$((x0 + (dx > 0 ? 120 : 0))); 
    out=$(BOT_AHEAD=$a timeout 180 bin/bot -t 3m walk $tx $y0 2>&1)
    tiles=$(echo "$out" | grep -oE "маршрут [0-9.]+ тайлов" | head -1 | grep -oE "[0-9.]+" | head -1)
    secs=$(echo "$out" | grep -oE "ходьба: маршрут [0-9.]+ с" | head -1 | grep -oE "[0-9.]+")
    echo "ahead=$a → x=$tx: $tiles тайлов за $secs с = $(python3 -c "print(round(${tiles:-0}/max(${secs:-1},0.01),2))") т/с; $(echo "$out" | grep -oE 'остановка [0-9.]+ с, доводка [0-9.]+ с' | head -1)"
  done
done
