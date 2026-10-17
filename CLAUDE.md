# Factorio Bot

Бот играет в Factorio 2.0.77 (base) до запуска ракеты. План — `plan.md`.

Правила честности (утверждены пользователем; соблюдать ВСЕГДА, без исключений):
- Действия — только эмуляция ввода ОС (мышь/клавиатура через XTEST) в окно клиента.
- Мод `botbridge` и RCON — только чтение состояния. Никаких команд, меняющих игру (в т.ч. генерации чанков).
- Бот знает только то, что видит игрок: открытые (charted) чанки своей силы, свой инвентарь/исследования, справочник прототипов. Каждый поиск сущностей/тайлов — с фильтром `force.is_chunk_charted`. Новый сенсор — сначала вопрос «мог бы игрок это знать?».

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
- Клиент изолирован от Steam: `HOME=run/client/home` — Steam API не инициализируется. Иначе Steam-версия читает/пишет `player-data.json` только через облако Steam (общее с основной игрой пользователя!), локальный файл не читает, и при сбое облака имя становится пустым → на сервер заходит новый игрок. Без Steam имя всегда "" — стабильно игрок №2. Игрок №1 `ATPstealer` (первые запуски) отключён, бросать.
- `--disable-audio` + `[sound] master-muted=true` — без звука.
- Перезапуск только клиента: `pkill -f "^<путь factorio> .*run/client/config.ini"`, затем та же команда, что в `run.sh` (с `setsid nohup`).
- Клиент изолирован от Steam: `HOME=run/client/home` — Steam API не инициализируется. Иначе Steam-версия читает/пишет `player-data.json` только через облако Steam (общее с основной игрой пользователя!), локальный файл не читает, и при сбое облака имя становится пустым → на сервер заходит новый игрок. Без Steam имя всегда "" — стабильно игрок №2. Игрок №1 `ATPstealer` (первые запуски) отключён, бросать.
- `--disable-audio` + `[sound] master-muted=true` — без звука.
- Перезапуск только клиента: `pkill -f "^<путь factorio> .*run/client/config.ini"`, затем та же команда, что в `run.sh` (с `setsid nohup`).
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
- Файлы: `control.lua` (точка входа, игрок, поиск, события), `util.lua` (общие функции, `visible`), `prototypes.lua` (`dump_prototypes`), `scan.lua` (`list_charted`, `scan_chunks`, `get_force`, `get_production_stats`).
- Поля прототипов, применимые не ко всем типам, читать через `try`/`g()` — иначе ошибка на чужом типе.
- Методы — в таблице `methods`; только чтение: `ping`, `tick`, `get_player`, `find_nearest{type,name,radius,limit,position}`, `find_minable{target,radius,limit,position}` (+products, mining_time, amount), `entity_at{name,position}`, `poll_events{after}`.
- Буфер событий в `storage.events` (до 1000, номер `seq`): mined_entity, built_entity, crafted_item, research_finished, entity_died, resource_depleted, gui_opened/closed, chunk_charted. `poll_events` ничего не удаляет.
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

- `go build -o bin/bot ./cmd/bot` (bin/ в .gitignore). Команды: `bin/bot [-t 5m] ping | player | events [after] | walk X Y | mine ЦЕЛЬ [N] [R] | craft Р [N] | place П X Y [n|e|s|w] | insert П N X Y | take X Y | pickup X Y | map ... | patches [R] | protos [-refresh] | force | shot [-o F]`. Полная справка — `bin/bot` без аргументов; новые команды добавлять в `usage` в `cmd/bot/main.go`.
  - `mine`: ЦЕЛЬ — группа `tree|rock|wreck|ore|fish|any`, имя сущности или предмета (`coal`, `stone`, `wood`); N — для руды единицы, для остального сущности (по умолчанию 1); R — радиус поиска (64).
  - `-t` — общий лимит времени (по умолчанию 5m). Любой запуск действий в игре — только с лимитом (`-t`, `timeout`): пользователь прерывал неограниченные прогоны.
- `internal/rcon` — Source RCON; ответы до 2 МБ одним пакетом проверены — пагинация не нужна.
- `internal/bridge` — `Call(method, args, &out)` + типизированные `Ping/Tick/Player/FindNearest/EntityAt/PollEvents`. JSON передаётся в Lua длинными скобками `[=[...]=]`.
  - Пустая Lua-таблица приходит из `table_to_json` как `{}`, не `[]` — массивы декодировать в `bridge.List[T]`, словари — делать в моде `nil`, если пусты.
- `internal/input` — XTEST на чистом Go (`github.com/jezek/xgb`): `KeyDown/KeyUp/Tap(key, mods...)/Type`, `Move/ButtonDown/ButtonUp/Click/Wheel`, `FindWindow(prefix)`. После каждого события round trip + `Delay` 20 мс.
- `internal/camera` — мир ↔ пиксели дисплея: центр окна = позиция игрока, 1 тайл = 32 × zoom × display_scale (zoom 1.5 → 48 px).
- `internal/knowledge` — справочник `Prototypes` (рецепты, предметы, сущности, технологии, жидкости), `LoadOrFetch(b, "data")` — кэш `data/prototypes-<версия>.json` (data/ в .gitignore), `RecipesFor(item)`, `TechFor(recipe)`.
- `internal/world` — модель открытой карты: `Sync(b, full)` (новые чанки + чанки из событий; остатки руды меняются без событий → `full`), `TileAt/ResourceAt/Blocked/Walkable`, `Patches()` (руда — 8-связность, нефть — радиус 12), `Render(opts)` → PNG.
- `internal/act` — `Hands` (B, In, Win, Layout, Protos, World, DebugDir): `WalkTo/WalkNear` (A* при `World != nil`), `Hover`, `Mine/MineUntil/Gather`, `Clean` (меню/окна/рука), `CloseGUI`, `OpenInventory`, `Pick` (быстрая панель), `ClearCursor`, `Craft/CraftWait`, `Place`, `Rotate`, `Insert`, `Take`, `Pickup`, `MenuOpen`. Раскладка интерфейса — `Layout1856` в `gui.go` (окно 1856x1016, UI scale 1; при другом размере — перекалибровать по `bin/bot shot`).
- `internal/world` также: `Path(from, to, near, max)` (A*), `FreeSpot/FreeSpotWhere` (место под постройку).

## Ввод — что выяснено

- В Xephyr нет WM: фокус клавиатуры следует за указателем — `Hands.Focus()` перед клавишами.
- Окно клиента 1856x1016 со смещением +32+32 (пользователя устраивает). UI scale зафиксирован в `run/client/config.ini`, звук выключен там же (`[sound] master-muted=true`).
- Зум в конфиге не хранится — бот читает `zoom` из игры и не меняет его.
- Ходьба ~8.9 тайл/с, инерция после отпускания 0.6–1.2 тайла. `WalkTo`: бег до `stop+1`, затем доводка нажатиями с адаптивной длительностью (короткое нажатие игра может не заметить). Точность ≤0.5 тайла.
- Наведение: точки внутри `selection_box` от центра, подтверждение — `player.selected`. Сущность может быть целиком перекрыта рамкой соседа (dead-grey-trunk под tree-08) → `act.ErrHover`, брать другую цель.
- Интерфейс: где можно — хоткеи (пользователь просил); клики только в точки `Layout`, каждый шаг подтверждается модом (`opened_gui`, `cursor`, `quickbar`, очередь крафта, события).
- `Tap` держит клавишу 50 мс (`Input.Hold`): нажатие короче ~30 мс игра теряет (Z, E).
- `Z` с предметом в руке над сущностью — ровно 1 предмет; удержание — автоповтор (кладёт всё).
- `Q` с пустой рукой — пипетка; `Esc` без окна — меню игры (мод не видит; `MenuOpen` по пикселю кнопки Resume (146,232,151) в (880,413)).
- В 2.0 буры ставятся только на ресурсы (`can only be built on resources`).
- Зум после перезапуска клиента может быть другим (был 1.5, стал 1.0) — всегда брать `zoom` из игры.
- При сбое действия снимок экрана — в `Hands.DebugDir` (тесты: `run/debug/`).
- Открытое окно (инвентарь и т.п.) перехватывает ввод — ходьба/добыча не работают. `Hands.CloseGUI` (Esc только при `opened_gui_type != 0`, иначе Esc откроет меню) — при старте бота и в тестах. После экспериментов с GUI — закрывать.
- Hover только когда персонаж стоит (камера едет за игроком).
- К сущностям подходить `WalkNear` — к ближайшей точке `selection_box`, а не к `position`: у больших (обломки, камни) центр внутри коллизии, персонаж упрётся в край.
- Зажатый ввод переживает процесс: если бота убить во время добычи, X-сервер держит ПКМ — персонаж копает дальше. Поэтому: `Input.Close/ReleaseAll` на выходе (SIGINT/SIGTERM, `check`), `Input.Reset()` при старте. Вручную: `DISPLAY=:1 xdotool mouseup 3 keyup w keyup a keyup s keyup d`.
- Руда: ПКМ добывает единицу за единицей, сущность остаётся — стоп по приросту продукта в инвентаре. Персонаж не копает >1.5 с при зажатой ПКМ → `ErrNotMining` (инвентарь полон / далеко).
- Добываемое руками (`find_minable`): minable, без `required_fluid` (уран нельзя), `last_user == nil` (не свои постройки). Обломки корабля — `container`/`simple-entity-with-owner` с префиксом `crash-site-`; `huge-rock` даёт stone+coal.
- Игрок — `game.connected_players[1]` (индекс 2, имя "").
- `xdotool` установлен — для ручной отладки (`DISPLAY=:1 xdotool ...`).

## Shell

- `pkill -f`/`pgrep -f` с шаблоном из командной строки матчат собственный bash-процесс инструмента и убивают его (exit 144). Бота убивать `pkill -x bot`; игру — `scripts/stop.sh` (шаблон с `^`).

## Окружение

- Сессия X11 (XFCE), основной экран 5120x1440.
- Go 1.26 (apt `golang-go`), модуль `factorio-bot`.
