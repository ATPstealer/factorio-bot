# Factorio Bot

Бот играет в Factorio 2.0.77 (base) до запуска ракеты. Сейчас — фундамент (связь с игрой, ввод, ходьба, руки, знания, раскладки «на бумаге»); планировщик, кластерная база и прохождение пишутся заново мелкими задачами. Прежняя версия целиком — тег `v1`.

Документы:
- `capabilities.md` — что умеет бот (общими словами).
- `stages-report.md` — отчёты по стадиям: итоги на картах и только находки (стадии не пересказывать; ничего не нашлось — «Пусто»).
- `stages.md` — стадии игры (бенч по стадиям; пишет пользователь). Код — `internal/stage` (файл на стадию), запуск `bin/bot stage N`; стадия пройдена — выполнена на 3 новых случайных картах, снимок `<сид>-N`.
- `cluster.md` — дизайн кластерной базы (что и зачем, без привязки к коду).
- `test/e2e/README.md` — тесты, бенч, статистика прогонов.
- Задачи, наблюдения во время прогонов и отчёты — в чате.

## Правила честности

Утверждены пользователем; соблюдать ВСЕГДА, без исключений:
- Действия — только эмуляция ввода ОС (мышь/клавиатура через XTEST) в окно клиента.
- Мод `botbridge` и RCON — только чтение состояния. Никаких команд, меняющих игру (в т.ч. генерации чанков).
- Исключение (просьба пользователя): бот пишет в консоль игры, что делает (`Hands.Say` → `Bridge.Say`: обычный текст по RCON игра показывает как `<server>: [bot] ...`). Только отображение, без Lua.
- Бот знает всю уже сгенерированную карту — без разведки (решение пользователя 2026-10-21; раньше — только открытые чанки), свой инвентарь/исследования, справочник прототипов. Генерировать новые чанки нельзя (никаких команд): карта растёт сама, когда персонаж ходит. Каждый поиск сущностей/тайлов — с фильтром `util.chunk_visible` (`surface.is_chunk_generated`). Новый сенсор — сначала вопрос «мог бы игрок это знать?».

## Игра

- Игра: `/mnt/games/steam/steamapps/common/Factorio/bin/x64/factorio`. Пользовательский `~/.factorio` не трогать.
- `run/server/` — write-data сервера (`config.ini`, `mods/`, `saves/bot.zip`); `run/client/` — клиента (`config.ini`, `mods/`, `player-data.json`, `steam_appid.txt`, `home/`).
- Логи: `run/server.log`, `run/client.log`, `run/xephyr.log`. Сид текущей карты — `run/map.json`.

```bash
scripts/run.sh                      # сервер → Xephyr :1 → клиент; Ctrl+C — остановить всё
scripts/stop.sh                     # остановить извне (сервер по SIGTERM, с сохранением)
scripts/newmap.sh                   # новая СЛУЧАЙНАЯ карта, игра в фоне, ждёт персонажа
SEED=123 scripts/newmap.sh          # конкретная карта
NEW_MAP=0 scripts/newmap.sh         # перезапустить игру на текущей карте
RES=2560x1440 scripts/run.sh        # другое разрешение Xephyr
```

- Бот должен работать на любой карте (решение пользователя): утёсы как в обычной игре, кусак нет. Нет нужного на открытой карте — разведка, а не опора на конкретные места.
- Новая карта начинается с заставки крушения (персонажа нет, `controller_name` = "cutscene") — пропускается `Tab` (`guard`, `newmap.sh`).
- Сервер грузит `run/server/saves/bot.zip`, по SIGINT и SIGTERM сохраняет в него же. Автосейвы — `_autosave1..5.zip` раз в 10 мин.
- Готовность сервера — строка `to(InGame)` в `run/server.log` (не `Hosting game at` — RCON тогда ещё отвечает пусто).
- Клиент запускается из `run/client` с `steam_appid.txt` и `SteamAppId=427520`, иначе Steam перезапускает игру на основном дисплее `:0`.
- Клиент изолирован от Steam: `HOME=run/client/home` — Steam API не инициализируется. Иначе Steam-версия читает/пишет `player-data.json` только через облако Steam (общее с основной игрой пользователя!), и при сбое облака на сервер заходит новый игрок. Без Steam имя всегда "" — стабильно игрок №2 (`game.connected_players[1]`). Игрок №1 `ATPstealer` (первые запуски) отключён.
- `--disable-audio` + `[sound] master-muted=true` — без звука.
- Перезапуск только клиента: `pkill -f "^<путь factorio> .*run/client/config.ini"`, затем та же команда, что в `run.sh` (с `setsid nohup`).
- Окно Xephyr `factorio-bot-N` (N — экземпляр), после запуска свёрнуто (`SHOW=1` — нет); смотреть — `scripts/view.sh N` (видео без ввода). Ввод бота — только в дисплей своего экземпляра.
- `scripts/map-gen-settings.json` — без кусак (`enemy-base` frequency 0, `peaceful_mode`), утёсы обычные (`richness` 1). Огонь у места крушения (`crash-site-fire-flame`) — сила enemy, но не кусаки.
- `scripts/server-settings.json` — `auto_pause: false`, `require_user_verification: false`, не публичный.

## Снимки

- `scripts/snapshot.sh save ИМЯ "описание" | load ИМЯ | list` — сервер штатно сохраняет `bot.zip`, копия с `map.json` (и `state*.json`, если есть) и `NOTE.txt` в `run/snapshots/ИМЯ/`; по RCON ничего не пишется.
- Имя — `<сид карты>-<номер>` (решение пользователя): `-0` — карты для бенча e2e; `-1`, `-2`, … — после стадий (стадии распишет пользователь). Другие имена не заводить.
- Мелкие проверки и продолжение — со снимка, не с нуля.

## Тесты

- `scripts/test.sh` — go vet + юнит-тесты + все e2e; `scripts/test.sh Walk` — по шаблону `-run`. Карта по умолчанию текущая (перезапуск — если игра не запущена или изменился мод, хеш `run/mod.hash`); `NEW_MAP=1`, `SEED=…`, `SNAP=имя`, `SAVE_AS=имя`.
- Бенч — `scripts/bench.sh`: все e2e на каждом снимке `<сид>-0` (итог — `run/bench/summary.txt`); `PAR=4` — параллельно, по экземпляру на карту (запуск через 8 с).
- Экземпляры игры (`scripts/inst.sh`): `INST=0` — основной (`run/`, `:1`, RCON 27015, порт 34197); `INST=1–4` — `run/iN`, `:N+1`, RCON 27015+N, порт 34197+N; все скрипты (`run`, `stop`, `newmap`, `snapshot`, `test`, `stage`) работают с `INST`; бот и тесты берут экземпляр из окружения (`BOT_RUN`, `BOT_DISPLAY`, `BOT_RCON`). Снимки общие.
- Статистика прогонов ведётся сама: `scripts/test_stats.py` → `test/e2e/stats.tsv` и таблица в `test/e2e/README.md`.
- e2e: `test/e2e/`, build tag `e2e`, разложены по файлам групп: `env`, `interface`, `world`, `walk`, `hands`, `knowledge`; помощники — `helpers_test.go`: `game(t)` (RCON), `lua(t, expr, &out)` (`s` = surface 1, `p` = игрок), `bot(t, method, args, &out)`, `xdo(t, ...)`, `readJSON`, `ini`, `handsT`, `worldT`, `protosT`, `ctxT`, `clean`, `freeArea`, `have(t, h, item, n)` (сырьё — руками, остальное — крафт; нужны пластины, а их нет — пропуск).
- Порядок (пользователь): проблема механики бота в игре (не дошёл, не поставил, не взял…) → сначала e2e-тест, точно повторяющий ситуацию из игры → прогон на всех 4 картах `-0` (`scripts/bench.sh Шаблон`) → чего на карте нет для теста (технологии, предметы) — в раздел «Что добавить на карты» README тестов и в чат, пользователь добавит потом (работа не ждёт) → исправления кода и прогоны, дальше по задаче.
- Правило (пользователь): тест пишется один раз; упал — доделывать код бота под сложную ситуацию, код теста после падения НЕ менять никогда (только по прямой просьбе пользователя).
- Правила (пользователь): по одному гранулярному тесту на способность, без перекрытий; тесты не убирают за собой; сначала тест текущей задачи, зелёный — потом все; после зелёных — рефакторинг (`~/go/bin/deadcode -test -tags e2e ./...`, дубли, устаревшее в доках).
- Запросы в тестах — только чтение; действия — только ввод.
- Любой запуск действий в игре — только с лимитом времени (`-t`, `timeout`): пользователь прерывал неограниченные прогоны.

## Мод botbridge

- Исходник `mod/botbridge/`, симлинки в `run/server/mods/` и `run/client/mods/` (версии у сервера и клиента обязаны совпадать). После правки мода — перезапуск сервера и клиента; поднимать `version` в `info.json` (тест сверяет версию в игре с файлом).
- Единая точка входа: `/sc remote.call("bot", "call", '{"method":"ping","args":{...}}')` → одна строка JSON `{"ok":true,"result":...}` / `{"ok":false,"error":"..."}`.
- Файлы: `control.lua` (точка входа, игрок, поиск, события), `util.lua` (общие функции, `visible`), `prototypes.lua` (`dump_prototypes`), `scan.lua` (`list_charted`, `scan_chunks`, `get_force`).
- Методы (только чтение): `ping`, `get_player`, `get_entity`, `get_entities{list}` (пакетом), `find_nearest{type,name,radius,limit,position}`, `find_minable{target,radius,limit,position}` (+products, mining_time, amount), `entity_at{name,position}`, `poll_events{after}`, `list_charted`, `scan_chunks`, `get_force`, `dump_prototypes`.
- `get_player`: позиция, зум, инвентарь и его ячейки, размер инвентаря, рука, выделение, быстрая панель, открытое окно, `render_mode`, очередь крафта.
- `get_entity`: name, type, position, unit_number, selection_box, status, own, inventories, recipe, burning, network (электросеть), belt (что на ленте), fluids, filters (фильтр инсертера), ug_type/ug_paired (подземка). `get_force`: researched, current, queue, recipes, produced (статистика производства; добыча руками туда НЕ попадает). Прототипы: `pipe_connections` (при «север»).
- Буфер событий `storage.events` (до 1000, номер `seq`): mined_entity, built_entity, crafted_item, research_finished, entity_died, resource_depleted, gui_opened/closed, chunk_charted. `poll_events` ничего не удаляет.
- Поля прототипов, применимые не ко всем типам, читать через `try`/`g()` — иначе ошибка на чужом типе.
- Утёсы: для `cliff` мод отдаёт настоящую рамку (`bb`, зависит от ориентации).
- Добываемое руками (`find_minable`): minable, без `required_fluid` (уран нельзя), `last_user == nil` (не свои постройки). Обломки корабля — `container`/`simple-entity-with-owner` с префиксом `crash-site-`; `huge-rock` даёт stone+coal.
- Проверка мода без остановки основного сервера: временный сервер с write-data в scratchpad, копией сейва, `--port 34198 --rcon-port 27016`.

## RCON

- `127.0.0.1:27015`, пароль `botpass` (или `$RCON_PASSWORD`). Source RCON: auth (type 3) → ответ type 2; команда (type 2) → ответ type 0. Ответы до 2 МБ одним пакетом — пагинация не нужна.
- Консоль: `scripts/rcon.py` (REPL) или `scripts/rcon.py "/sc rcon.print(game.tick)"`.

## Код (Go)

- Go 1.26 (apt `golang-go`), модуль `factorio-bot`. Правки: `~/go/bin/goimports -w`, `gofmt`.
- `go build -o bin/bot ./cmd/bot` (bin/ в .gitignore). Команды: `bin/bot [-t 5m] ping | player | events [after] | path X Y | walk X Y | mine ЦЕЛЬ [N] [R] | craft Р [N] | place П X Y [n|e|s|w] | insert П N X Y | take X Y | pickup X Y | recipe X Y Р | rotate X Y D | filter X Y П | research ТЕХ | map ... | patches [R] | protos [-refresh] | force | shot [-o F] | calc ... | tech ЦЕЛЬ | raw П [N]`. Справка — `bin/bot` без аргументов; новые команды — в `usage` (`cmd/bot/main.go`).
  - `mine`: ЦЕЛЬ — группа `tree|rock|wreck|ore|fish|any`, имя сущности или предмета (`coal`, `stone`, `wood`); N — для руды единицы, для остального сущности; R — радиус (64).
  - `calc/tech/raw` работают без игры по кэшу `data/`.

Пакеты:
- `internal/rcon` — Source RCON (`Dial`, `Exec`).
- `internal/bridge` — `Call(method, args, &out)` + типизированные `Ping/Player/FindNearest/FindMinable/EntityAt/GetEntity/GetEntities/PollEvents/ListCharted/ScanChunks/Force/DumpPrototypes/Say`. JSON в Lua — длинными скобками `[=[...]=]`. Пустая Lua-таблица приходит как `{}`, не `[]` — массивы декодировать в `bridge.List[T]`, словари в моде делать `nil`, если пусты.
- `internal/input` — XTEST на чистом Go (`github.com/jezek/xgb`): `KeyDown/KeyUp/Tap(key, mods...)/Type`, `Move/ButtonDown/ButtonUp/Click/Wheel`, `FindWindow(prefix)`, `Screenshot`. После каждого события round trip + `Delay` 20 мс.
- `internal/camera` — мир ↔ пиксели: центр окна = позиция игрока, 1 тайл = 32 × zoom × display_scale (zoom 1.5 → 48 px).
- `internal/knowledge` — справочник `Prototypes` (рецепты, предметы, сущности, технологии, жидкости), `LoadOrFetch(b, "data")` — кэш `data/prototypes-<игра>-botbridge<мод>.json`; `RecipesFor`, `TechFor`, `NewAvailability`, `Usable`, `Resource`, `BestCrafter/BestDrill/BestBelt`, `TechPath`, `Cost`, `TechsFor`. Снимок для юнит-тестов — `internal/knowledge/testdata/prototypes.json` (в git; e2e `TestSnapshotMatchesGame` сверяет с игрой; при смене версии/полей — пересобрать `bin/bot`, ПОТОМ `bin/bot protos -refresh`, и пересохранить, иначе старый бинарник выкинет новые поля).
- `internal/calc` — калькулятор: `Solve(p, targets/с, Options{Researched, All, Machines, Have})` → `Plan` (рецепты с машинами, сырьё, буры, МВт, пар, уголь, ленты, излишки, нужные техи по порядку + цена); симплекс `Minimize`, веса сырья `rawCost` выбирают рецепт при альтернативах (нефть); `Handcraft` — ручной крафт; `SteamFor`.
- `internal/world` — модель открытой карты: `Sync(b, full)` (новые чанки + чанки из событий; остатки руды меняются без событий → `full`), `TileAt/ResourceAt/Blocked/Walkable/Buildable/Standable`, `Patches()` (руда — 8-связность, нефть — радиус 12), `Path/Route` (A*), `FreeSpot/FreeSpotWhere`, `ExploreRing`, `MarkStuck`, `Without`, `Hide`, `Enclosed`, `Render` → PNG.
- `internal/act` — руки `Hands` (B, In, Win, Layout, Protos, World, Log, DebugDir, Console): `WalkTo/WalkNear/WalkToMine`, `Explore`, `Hover`, `Mine/MineUntil/Gather` (Mine: ствол под чужой кроной — сначала соседи), `Clean`, `CloseGUI`, `OpenInventory`, `Pick`, `ClearCursor`, `Craft/CraftWait`, `Place/PlaceAll/PlaceDirs`, `Rotate`, `Insert/InsertAll`, `Take`, `Pickup/PickupAll`, `Drop/PickupItem`, `SetRecipe`, `SetFilter`, `StartResearch`, `StandFor`, `LeaveArea`, `MenuOpen`, `Say`. Раскладка интерфейса — `Layout1856` в `gui.go` (окно 1856x1016, UI scale 1; при другом размере — перекалибровать по `bin/bot shot`).
- `internal/cluster` — кластерная база (из v1, `cluster.md`): `Split(protos, calc.Plan, reserve, have, separate...)` → кластеры, `Labs`, `StorageCluster`, `AddPower`; `Base.PlaceAll` (места: Area + коридор `Corridor`; шина — головой к плавке), `Base.Layout` (внутренности), `Base.Connect` (ленты, шина: подвод в голову или боком у головы, порядок поставщиков перебирается), `Base.Draw`. План кластеров — стадия 4: `state-stage4.json` (`stage.LoadPlan`), картинка `run/stages/s4-<сид>.png`. Стадия 3 — разведка квадрата 1024×1024 вокруг старта (`Hands.ExploreWhere`).
- `internal/layout` — раскладки «на бумаге»: `New(protos, world)`, `RouteBelt` (A*, подземки; правило пользователя: устранимое — деревья, камни, обломки — не обходить, а убрать и класть ровно: такие тайлы стоят как свободные, поворот — `turnCost`), `RoutePipe` (подземные трубы), `WaterPump`, `SteamPower`, `Poles`, `OreUnder` (бур на одном ресурсе), `Validate`, `Draw`. Координаты бывают отрицательными — не использовать -1 как «не найдено».

## Что выяснено

Интерфейс:
- Мышь пользователя над окном Xephyr двигает курсор в игре — клик бота уходит не туда (столб встал в стороне). Во время прогонов над окнами игры мышью не водить.
- В Xephyr нет WM: фокус клавиатуры следует за указателем — `Hands.Focus()` перед клавишами; курсор — в `Layout.Focus` (60,300), т.к. центр окна — кнопка «Save game» в меню.
- Окно клиента 1856x1016 со смещением +32+32. UI scale и звук — в `run/client/config.ini`. Координаты `bin/bot shot` — относительно окна (+32+32 к экрану); `shot` закрывает окна при старте — для снимка открытого окна: `DISPLAY=:1 import -window root -crop 1856x1016+32+32 F.png`.
- Где можно — хоткеи (пользователь просил); клики только в точки `Layout`, каждый шаг подтверждается модом (`opened_gui`, `cursor`, `quickbar`, очередь крафта, события, recipe, filters).
- `Tap` держит клавишу 50 мс (`Input.Hold`): нажатие короче ~30 мс игра теряет (Z, E). Повторное `E` не жать (окно, открывшееся с опозданием, закроется).
- `Esc` без окна — меню игры (мод не видит; `MenuOpen` по пикселю кнопки Resume (146,232,151) в (880,413); второй оттенок — (94,182,99)). `CloseGUI` жмёт Esc только при `opened_gui_type != 0` и ждёт закрытия до 1 с. `guard` закрывает меню и карту в начале действий.
- Карта / удалённый вид (Tab; Ctrl+F без окна открывает поиск по карте!): мод отдаёт `render_mode` ("game" | "chart" | "chart_zoomed_in"). Текст печатать только после проверки `inInventory()` (до Ctrl+F, после него и после ввода) — иначе буквы уйдут в игру как хоткеи.
- Открытое окно перехватывает ввод — ходьба/добыча не работают; после экспериментов с GUI — закрывать.
- Alt-режим (просьба пользователя): `guard` один раз проверяет по кнопке ALT на экране (`Layout.AltButton`, оранжевая — включён) и включает. Не по `get_player.alt_mode`: после перезапуска сервера он может не совпадать с клиентом.
- Наведение пропускает точки под панелями (`Layout.Panels`); не навёлся — `closeStray`. Наведение — точки внутри `selection_box` от центра, подтверждение — `player.selected`; сущность может быть целиком перекрыта рамкой соседа → `act.ErrHover`. Hover — только когда персонаж стоит (камера едет за игроком).
- Рецепт (`SetRecipe`): клик по сборщику → окно выбора → Ctrl+F → название → первая ячейка (по пикселям `firstSlotY`: окно меньше у машин с малым числом рецептов); рецепт уже есть — кнопка смены `Layout.RecipeChange`.
- Фильтр (`SetFilter`): окно инсертера → «Use filters» → ячейка → поиск → найденная (несколько — следующая ячейка); у burner-инсертера окно другое — не откалибровано.
- Исследование (`StartResearch`): T → Ctrl+F → название → первая в списке (доступные первыми) → «Start research» (`Layout.TechFirst/TechStart`); окно технологий мод не видит — по пикселям `TechProbe`.
- `Hands.Say(...)` — перед каждым значимым действием: лог + консоль игры. Новые действия — тоже с `Say`.
- При сбое действия снимок экрана — в `Hands.DebugDir` (тесты: `run/debug/`).
- Зажатый ввод переживает процесс: `Input.Close/ReleaseAll` на выходе (SIGINT/SIGTERM), `Input.Reset()` при старте. Вручную: `DISPLAY=:1 xdotool mouseup 3 keyup w keyup a keyup s keyup d`.

Ходьба:
- ~8.9 тайл/с, инерция 0.6–1.2 тайла. `WalkTo`: бег до `stop+1`, затем доводка нажатиями с адаптивной длительностью. Точность ≤0.5 тайла.
- `world.Route`: A* на сетке полутайлов по настоящим рамкам коллизий, раздутым на полуширину персонажа (0.2 + 0.08) — на тайловой сетке лес был стеной; затем спрямление отрезками под любым углом. Следование: клавиши ближайшего из 8 направлений на точку впереди — 1.5 тайла в открытом месте, 0.5 в тесноте (`World.Tight`). Пути нет — к ближайшей достижимой точке, `ErrNotReached`.
- Деревья на маршруте рубятся по дороге, если обход дольше (`treeCost` 6 за ячейку; остановка за `cutAhead` 1.8). `bin/bot path X Y` — путь без движения.
- `World.MarkStuck` — где персонаж упёрся (щель уже, чем в модели); `Hands.breach` — путь закрыт кольцом своих построек — поднять, пройти, поставить назад (`World.Without`). Утёсы-углы — повёрнутые рамки (`Box.Orientation`, `Contains`, `Bounds`).
- Персонаж на ленте не останавливается (лента несёт): `Standable` исключает ленты, `waitStill` и `guard` (перед каждым действием) сводят с ленты (`stepOffBelt`, в сторону цели ходьбы).
- Разведка (`Hands.Explore`): к ближайшему от персонажа неоткрытому чанку на краю открытой карты (`World.NearestUnknown`); не пройти или подошёл, а карта не прибавилась — этот чанк пропускается.
- К сущностям подходить `WalkNear` — к ближайшей точке `selection_box` (у больших центр внутри коллизии). Действия не ходят, если уже в досягаемости (~8 для вложить/забрать, ~2.4 для добычи).

Руки:
- Добыча руками — досягаемость ~2.7 тайла до рамки **коллизии** (`WalkToMine`; рамка выделения у деревьев высокая). Reach 10 — только для взаимодействия.
- Руда: ПКМ добывает единицу за единицей, сущность остаётся — стоп по приросту в инвентаре. Не копает >1.5 с при зажатой ПКМ → `ErrNotMining` (инвентарь полон / далеко).
- `Z` с предметом в руке над сущностью — ровно 1 предмет; удержание — автоповтор. `Q` с пустой рукой — пипетка. `InsertAll` — Ctrl+ЛКМ с предметом в руке (весь вид; бывает по пачке — повторять до полного ящика). Просьба пользователя: «положить всё» — Ctrl; много, но не всё — Z.
- `PlaceAll`: предмет в руку один раз, без отхода, если в досягаемости ~8.5; направление руки доворачивается R, неверно поставленные — Rotate в конце. `PickupAll`: ближайшая досягаемая без отпускания ПКМ, иначе встать туда, откуда достаётся больше всего (`bestStand`).
- Подземные ленты в 2.0: вторая, поставленная по ходу, сама становится выходом; нет — `act.ugExit` (поставить навстречу и R). Выход ставить только после своего входа — иначе игра делает его входом. Мод: `ug_paired`.
- В 2.0 буры ставятся только на ресурсы.
- Постройка (`PlaceDirs`) сначала убирает деревья и камни, чья рамка коллизии задевает место (`clearSpots`; игра иначе — «… is in the way»).
- Крафт при полном инвентаре стоит — `CraftWait` возвращает ошибку.
- Зум после перезапуска клиента может быть другим — всегда брать `zoom` из игры.
- Трубы: обычная труба не касается чужих жидкостных соединений, под ними — пара `pipe-to-ground` (у конца одно наземное соединение: вход смотрит назад, выход — вперёд).

## Shell и окружение

- `pkill -f`/`pgrep -f` с шаблоном из командной строки матчат собственный bash-процесс инструмента и убивают его (exit 144). Игру останавливать `scripts/stop.sh`.
- `xdotool` установлен — для ручной отладки (`DISPLAY=:1 xdotool ...`).
- Сессия X11 (XFCE), основной экран 5120x1440.
