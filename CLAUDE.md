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
scripts/stop.sh                     # остановить извне (сервер по SIGTERM, с сохранением)
NEW_MAP=1 SEED=12345 scripts/run.sh # пересоздать карту
RES=2560x1440 scripts/run.sh        # другое разрешение Xephyr
```

- Сервер грузит `run/server/saves/bot.zip`, по SIGINT и SIGTERM сохраняет в него же (проверено). Автосейвы — `_autosave1..5.zip` раз в 10 мин.
- Готовность сервера — строка `to(InGame)` в `run/server.log` (не `Hosting game at` — RCON тогда ещё отвечает пусто).
- Клиент запускается из `run/client` с `steam_appid.txt` и `SteamAppId=427520`, иначе Steam перезапускает игру на основном дисплее `:0`.
- Окно Xephyr называется `factorio-bot`, дисплей `:1`. Ввод бота — только в `:1`.

## Настройки

- `scripts/map-gen-settings.json` — без кусак (`enemy-base` frequency 0, `peaceful_mode`), без утёсов (`cliff_settings.richness` 0).
- `scripts/server-settings.json` — `auto_pause: false`, `require_user_verification: false`, не публичный.

## Тесты (регрессия по стадиям)

- `scripts/test.sh` — go vet + юнит-тесты + e2e всех стадий; `scripts/test.sh Stage0` — по шаблону `-run`. Гонять после каждой стадии и перед коммитом.
- e2e: `test/e2e/`, build tag `e2e`, требуют запущенного `scripts/run.sh` (сервер + клиент в `:1`).
- Каждая стадия — свой файл `test/e2e/stageN_test.go` с тестами `TestStageN_*`; закрывая стадию, добавлять тесты её критерия готовности.
- Хелперы (`helpers_test.go`): `game(t)` — общее RCON, `lua(t, expr, &out)` — выражение → JSON (`s` = surface 1, `p` = игрок), `bot(t, method, args, &out)` — вызов мода, `xdo(t, ...)` — xdotool в `:1`, `readJSON`, `ini`.
- Запросы в тестах — только чтение; действия — только ввод.

## Мод botbridge

- Исходник `mod/botbridge/`, симлинки в `run/server/mods/` и `run/client/mods/` (версии у сервера и клиента обязаны совпадать). После правки мода — перезапуск сервера и клиента.
- Единая точка входа: `/sc remote.call("bot", "call", '{"method":"ping","args":{...}}')` → печатает одну строку JSON `{"ok":true,"result":...}` / `{"ok":false,"error":"..."}`.
- Методы — в таблице `methods` в `control.lua`; только чтение: `ping`, `tick`, `get_player`, `find_nearest{type,name,radius,limit,position}`, `entity_at{name,position}`, `poll_events{after}`.
- Буфер событий в `storage.events` (до 1000, номер `seq`): mined_entity, built_entity, crafted_item, research_finished, entity_died, resource_depleted, gui_opened/closed. `poll_events` ничего не удаляет.
- `entity_info` отдаёт name, type, position, unit_number, selection_box.
- Меняя мод — поднимать `version` в `info.json` (тест сверяет версию в игре с файлом).
- Проверка мода без остановки основного сервера: временный сервер с write-data в scratchpad, копией сейва, `--port 34198 --rcon-port 27016`.

## RCON

- Go-клиент: `internal/rcon` (`Dial`, `Exec`), юнит-тест с фейковым сервером.

- `127.0.0.1:27015`, пароль `botpass` (или `$RCON_PASSWORD`).
- Протокол Source RCON: auth (type 3) → ответ type 2; команда (type 2) → ответ type 0.
- Проверка: `/sc rcon.print(game.tick)`.
- Консоль RCON: `scripts/rcon.py` (REPL) или `scripts/rcon.py "/sc rcon.print(game.tick)"`.

## Код бота (Go)

- `go build -o bin/bot ./cmd/bot` (bin/ в .gitignore). Отладочные команды: `bin/bot ping | player | events [after] | walk X Y | chop [радиус]`.
- `internal/rcon` — Source RCON; ответы до 2 МБ одним пакетом проверены — пагинация не нужна.
- `internal/bridge` — `Call(method, args, &out)` + типизированные `Ping/Tick/Player/FindNearest/EntityAt/PollEvents`. JSON передаётся в Lua длинными скобками `[=[...]=]`.
  - Пустая Lua-таблица приходит из `table_to_json` как `{}`, не `[]` — массивы декодировать в `bridge.List[T]`, словари — делать в моде `nil`, если пусты.
- `internal/input` — XTEST на чистом Go (`github.com/jezek/xgb`): `KeyDown/KeyUp/Tap(key, mods...)/Type`, `Move/ButtonDown/ButtonUp/Click/Wheel`, `FindWindow(prefix)`. После каждого события round trip + `Delay` 20 мс.
- `internal/camera` — мир ↔ пиксели дисплея: центр окна = позиция игрока, 1 тайл = 32 × zoom × display_scale (zoom 1.5 → 48 px).
- `internal/act` — `Hands`: `WalkTo(ctx, pos, stop)`, `Hover(ctx, entity)`, `Mine(ctx, entity, timeout)`.

## Ввод — что выяснено

- В Xephyr нет WM: фокус клавиатуры следует за указателем — `Hands.Focus()` перед клавишами.
- Окно клиента 1856x1016 со смещением +32+32 (пользователя устраивает). UI scale зафиксирован в `run/client/config.ini`.
- Зум 1.5 пользователя устраивает; в конфиге не хранится — бот читает `zoom` и не меняет его.
- Ходьба ~8.9 тайл/с, инерция после отпускания 0.6–1.2 тайла. `WalkTo`: бег до `stop+1`, затем доводка нажатиями с адаптивной длительностью (короткое нажатие игра может не заметить). Точность ≤0.5 тайла.
- Наведение: точки внутри `selection_box` от центра, подтверждение — `player.selected`. Сущность может быть целиком перекрыта рамкой соседа (dead-grey-trunk под tree-08) → `act.ErrHover`, брать другую цель.
- Hover только когда персонаж стоит (камера едет за игроком).
- Игрок — `game.connected_players[1]`, имя из Steam (`ATPstealer`).
- `xdotool` установлен — для ручной отладки (`DISPLAY=:1 xdotool ...`).

## Окружение

- Сессия X11 (XFCE), основной экран 5120x1440.
- Go 1.26 (apt `golang-go`), модуль `factorio-bot`.
