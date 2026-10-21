#!/usr/bin/env bash
# Смотреть экземпляр игры без ввода: видео его дисплея (ffplay x11grab) в отдельном окне —
# мышь над ним в игру не попадает (окно Xephyr свёрнуто, scripts/run.sh).
#   scripts/view.sh [N]   — экземпляр N (по умолчанию 0: основной, :1)
INST=${1:-${INST:-0}}
source "$(dirname "$0")/inst.sh"
exec ffplay -loglevel error -window_title "factorio-bot-$INST (просмотр)" -f x11grab -framerate 15 \
  -video_size "${RES:-1920x1080}" -i "$DISPLAY_NUM" -vf scale=960:-1
