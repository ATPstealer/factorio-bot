# Factorio Bot

Бот, который играет в Factorio 2.1.23 (base) до запуска ракеты — как человек: действия только
вводом ОС (мышь и клавиатура через XTEST) в окно клиента игры. Состояние игры бот читает через
RCON и мод `botbridge` (только чтение, ничего в игре не меняет).

Устройство и правила — `CLAUDE.md`; стадии игры — `stages.md`; дизайн базы — `zones.md`,
`cluster.md`; тесты — `test/e2e/README.md`.

## Как это устроено

- **Сервер** — тот же исполняемый файл Factorio в режиме `--start-server`, с RCON.
- **Клиент** — обычная игра в отдельном X-дисплее **Xephyr** (`:1`, окно `factorio-bot-0`),
  подключается к серверу как игрок. Бот жмёт клавиши и мышь только в этот дисплей — основной
  рабочий стол не трогается.
- **Мод `botbridge`** (`mod/botbridge/`) — на сервере и клиенте: отвечает боту на запросы по RCON
  (позиция, инвентарь, постройки, карта, события).
- **Бот** — программа на Go (`bin/bot`): читает состояние по RCON, действует вводом в Xephyr.
- Всё, что пишет игра (сейвы, логи, конфиги), — в `run/` внутри репозитория; ваш `~/.factorio`
  не используется. Клиент запускается изолированно от Steam (`HOME=run/client/home`).

## Что нужно

- Linux с X11 (проверено на XFCE). Под Wayland без X11 не заработает (XTEST, Xephyr).
- **Factorio 2.1.23** — полная версия (Steam или с factorio.com), не demo. До 24.10.2026 бот жил на
  2.0.77 (Steam обновил игру сам); на другой версии мод `botbridge` может не загрузиться — в
  Steam стоит отключить автообновление игры.
- **Go 1.26** или новее.
- Пакеты (Debian/Ubuntu):

  ```bash
  sudo apt install xserver-xephyr python3 xdotool imagemagick ffmpeg
  ```

  `xserver-xephyr` и `python3` обязательны; `xdotool` (ручная отладка), `imagemagick` (снимки
  экрана) и `ffmpeg` (`scripts/view.sh` — смотреть игру) — по желанию.

## Установка

1. **Путь к игре.** В `scripts/inst.sh` — переменная `F` (исполняемый файл):

   ```bash
   F=/путь/к/Factorio/bin/x64/factorio
   ```

2. **Конфиги игры** — `run/server/config.ini` и `run/client/config.ini` (в git): в разделе
   `[path]` поправить `read-data` (папка `data` игры) и `write-data` (папка `run/server` и
   `run/client` в вашем клоне):

   ```bash
   GAME=/путь/к/Factorio            # где лежат bin/ и data/
   for s in server client; do
     sed -i "s|^read-data=.*|read-data=$GAME/data|; s|^write-data=.*|write-data=$PWD/run/$s|" run/$s/config.ini
   done
   ```

3. **Мод** — ссылки на `mod/botbridge` и список модов для сервера и клиента:

   ```bash
   for s in server client; do
     mkdir -p run/$s/mods run/$s/saves
     ln -sfn "$PWD/mod/botbridge" run/$s/mods/botbridge
     printf '{"mods":[{"name":"base","enabled":true},{"name":"botbridge","enabled":true}]}\n' > run/$s/mods/mod-list.json
   done
   ```

4. **Сборка бота:**

   ```bash
   go build -o bin/bot ./cmd/bot
   bin/bot            # справка по командам
   ```

5. **Справочник прототипов** (рецепты, предметы, сущности) кэшируется в `data/` при первом
   обращении к игре — отдельно делать ничего не нужно (`bin/bot protos -refresh` — обновить).

## Запуск игры

```bash
scripts/newmap.sh              # новая случайная карта: сервер + Xephyr + клиент, в фоне
SEED=851731946 scripts/newmap.sh   # конкретная карта
NEW_MAP=0 scripts/newmap.sh    # перезапуск на текущей карте
scripts/stop.sh                # остановить (сервер сохраняет карту в run/server/saves/bot.zip)
scripts/view.sh                # смотреть игру (видео, мышь в игру не попадает)
scripts/run.sh                 # то же, что newmap, но на переднем плане; Ctrl+C — стоп
```

- Настройки новой карты — постоянные, в `scripts/map-gen-settings.json`; меняется только сид
  (случайный или `SEED=…`):

  | Параметр | Значение | Что значит |
  |---|---|---|
  | `width`, `height` | 0 | карта бесконечная |
  | `starting_area` | 1 | стартовая зона обычного размера |
  | `peaceful_mode` | `true` | мирный режим |
  | `enemy-base` | `frequency` 0, `size` 0 | кусак нет вообще |
  | `cliff_settings` | `richness` 1 (остальное — по умолчанию) | утёсы как в обычной игре |
  | `starting_points` | (0, 0) | старт в центре |

  Всё остальное (руда, вода, деревья, камни, нефть) — по умолчанию игры: бот должен справляться
  на любой обычной карте, без подгонки под удобные места.

  Бот отлаживался только на этих настройках. Другие (больше или меньше руды, воды, утёсов и
  т. п.) возможны, но не проверены. Кусаки — точно нет: бот не умеет обороняться.
- Готовность сервера — строка `to(InGame)` в `run/server.log`; логи клиента — `run/client.log`.
- Окно Xephyr после запуска свёрнуто. **Не водите мышью над окном Xephyr во время работы
  бота** — курсор попадёт в игру, клики уйдут не туда.
- Если бота убили посреди действия и персонаж «сам» идёт:
  `DISPLAY=:1 xdotool mouseup 3 keyup w keyup a keyup s keyup d`.

## Бот

Команды по отдельности (игра должна быть запущена):

```bash
bin/bot ping                   # связь с игрой
bin/bot player                 # позиция, инвентарь, рука
bin/bot walk 10 -5             # дойти до точки
bin/bot mine tree 5            # срубить 5 деревьев
bin/bot craft wooden-chest 2   # скрафтить
bin/bot -t 5m stage 1          # выполнить стадию 1 (stages.md) с лимитом 5 минут
bin/bot calc ...               # калькулятор (работает без игры)
```

Любой запуск действий — с лимитом времени (`-t`).

## Стадии

Игра разбита на стадии (`stages.md`): 1–3 — старт, добывающие точки, разведка; 4–10 —
планирование «на бумаге»; с 11 — стройка базы.

```bash
scripts/stage.sh 1-27 851731946 859088572 794457638   # стадии 1–27 на трёх картах параллельно
scripts/stage.sh 12 851731946                          # одна стадия на одной карте
PLAN=1 scripts/stage.sh 4-10 851731946                 # только планирование, без стройки
```

- Каждая карта — в своём экземпляре игры (`INST=1, 2, …`: свой дисплей `:N+1`, порт RCON
  `27015+N`, папка `run/iN/`). Стадия идёт на всех картах сразу; следующая — когда все
  закончили; не прошла хоть на одной — стоп.
- После стадии N — снимок `run/snapshots/<сид>-N/` (сейв + состояние бота); стадия N
  начинается со снимка `<сид>-(N-1)`, стадия 1 без снимка — с новой карты.
- Итоги — `run/stages/summary.txt`, лог стадии — `run/stages/sN-<сид>.log`, картинки планов —
  `run/stages/*.png`.

## Снимки

```bash
scripts/snapshot.sh list
scripts/snapshot.sh save 851731946-0 "описание"
scripts/snapshot.sh load 851731946-0
```

## Тесты

```bash
scripts/test.sh                # go vet + юнит-тесты + все e2e на текущей карте
scripts/test.sh Walk           # e2e по шаблону
go test ./internal/...         # только юнит-тесты (без игры)
PAR=4 scripts/bench.sh         # все e2e на картах-эталонах (снимки <сид>-0), параллельно
```

Подробно — `test/e2e/README.md`.

## Экземпляры

Все скрипты работают с экземпляром из `INST` (0 — основной: `run/`, `:1`, RCON 27015; 1–4 —
`run/iN/`, `:N+1`, RCON `27015+N`):

```bash
INST=2 scripts/newmap.sh
INST=2 bash -c 'source scripts/inst.sh; bin/bot player'
INST=2 scripts/stop.sh
```

Консоль сервера (только чтение): `scripts/rcon.py "/sc rcon.print(game.tick)"`.
