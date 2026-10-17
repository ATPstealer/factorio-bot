# Factorio Bot

Бот играет в Factorio 2.0.77 (base) до запуска ракеты. План — `plan.md`.

Правила честности (утверждены пользователем):
- Действия — только эмуляция ввода ОС (мышь/клавиатура через XTEST) в окно клиента.
- Мод `botbridge` и RCON — только чтение состояния. Никаких команд, меняющих игру.

## Пути

- Игра: `/mnt/games/steam/steamapps/common/Factorio/bin/x64/factorio`
- `run/server/` — write-data сервера (`config.ini`, `mods/`, `saves/bot.zip`)
- `run/client/` — write-data клиента (`config.ini`, `mods/`, `player-data.json` с именем `bot`, `steam_appid.txt`)
- Логи: `run/server.log`, `run/client.log`, `run/xephyr.log`
- Пользовательский `~/.factorio` не трогать.

## Запуск

```bash
scripts/run.sh                      # сервер → Xephyr :1 → клиент; Ctrl+C — остановить всё
NEW_MAP=1 SEED=12345 scripts/run.sh # пересоздать карту
RES=2560x1440 scripts/run.sh        # другое разрешение Xephyr
```

- Сервер грузит `run/server/saves/bot.zip`, при штатной остановке сохраняет в него же. Автосейвы — `_autosave1..5.zip` раз в 10 мин.
- Готовность сервера — строка `to(InGame)` в `run/server.log` (не `Hosting game at` — RCON тогда ещё отвечает пусто).
- Клиент запускается из `run/client` с `steam_appid.txt` и `SteamAppId=427520`, иначе Steam перезапускает игру на основном дисплее `:0`.
- Окно Xephyr называется `factorio-bot`, дисплей `:1`. Ввод бота — только в `:1`.

## Настройки

- `scripts/map-gen-settings.json` — без кусак (`enemy-base` frequency 0, `peaceful_mode`), без утёсов (`cliff_settings.richness` 0).
- `scripts/server-settings.json` — `auto_pause: false`, `require_user_verification: false`, не публичный.

## RCON

- `127.0.0.1:27015`, пароль `botpass` (или `$RCON_PASSWORD`).
- Протокол Source RCON: auth (type 3) → ответ type 2; команда (type 2) → ответ type 0.
- Проверка: `/sc rcon.print(game.tick)`.
- Консоль RCON: `scripts/rcon.py` (REPL) или `scripts/rcon.py "/sc rcon.print(game.tick)"`.
- Как модуль: `from rcon import Rcon; Rcon().cmd(...)` — одно соединение на скрипт.

## Ввод (XTEST)

- `scripts/walk.py [клавиша[:сек] ...]` — держит клавиши в `:1` (или `$BOT_DISPLAY`), печатает `x y walking` по RCON. Пример: `scripts/walk.py d s:0.5 a w`.
- `walk.py` — ввод через ctypes (`libX11.so.6` + `libXtst.so.6`); `chop_tree.py` — через xdotool.
- В Xephyr нет WM: фокус следует за указателем — перед вводом курсор двигать в окно клиента.
- Окно клиента в Xephyr: 1856x1016 со смещением +32+32.
- Скорость ходьбы ~8.9 тайл/с; после отпускания клавиши инерция 0.6–1.2 тайла — точное позиционирование только с коррекцией по координатам.
- `scripts/chop_tree.py [радиус=50]` — ближайшее дерево → дойти WASD (стоп в 2.5 тайла, обход при застревании) → навести курсор → удерживать ПКМ, пока дерево не исчезнет. Печатает дерево в инвентаре до/после.
- Камера: центр окна = позиция игрока; пикселей на тайл = 32 × `p.zoom` (сейчас zoom 1.5 → 48 px). `p.zoom`, `p.display_resolution`, `p.display_scale` читаются по RCON.
- Наведение подтверждать через `p.selected` (позиция совпадает с целью); у дерева курсор берётся на позицию ствола, при промахе — перебор небольших смещений.
- xdotool установлен; `xdotool mousemove --window <id>` даёт координаты относительно окна клиента (`xdotool search --name ^Factorio`).
- Игрок на сервере — `game.connected_players[1]`, имя берётся из Steam (`ATPstealer`), не из `player-data.json`.

## Окружение

- Сессия X11 (XFCE), основной экран 5120x1440.
- Go не установлен; ставить `sudo apt install golang-go` (1.26).
