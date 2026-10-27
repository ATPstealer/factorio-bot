#!/usr/bin/env python3
"""Где уходит время стадии (stages-report, правила скорости 27.10): по журналам
run/stages/sN-<сид>.log время от строки до следующей относится к делу этой строки.

    scripts/stage_time.py                 # все журналы run/stages/s*.log
    scripts/stage_time.py 14 16-17        # стадии 14, 16, 17 (все сиды)
    scripts/stage_time.py 14 -s 859088572 # стадия 14 одного сида

Дела: ходьба (маршрут, иду, ходьба, застрял, путь закрыт), крафт (крафчу — ждёт очередь),
стройка (ставлю, поворачиваю, рецепт, фильтр), сундуки (беру, кладу, забираю, привязываю),
добыча руками (добываю, рублю, подбор), ожидание (жду), прочее. Строка «крафчу» без следующей
записи долго — бот стоит и ждёт очередь крафта.
"""
import glob
import os
import re
import sys
from collections import defaultdict
from datetime import datetime, timedelta

CATS = [
    ("ходьба", r"^(маршрут|ходьба|иду к|застрял|путь закрыт|обступили|пролом|к месту|к центру|по дороге|разведка: к)"),
    ("крафт", r"^(крафчу|в очередь)"),
    ("стройка", r"^(ставлю|поворачиваю|рецепт|фильтр|ставлю рецепт|настраиваю)"),
    ("сундуки", r"^(беру|кладу|забираю|привязываю|инвентарь полон|свободно)"),
    ("добыча руками", r"^(добываю|рублю|подбор|подбираю|расчистка)"),
    ("ожидание", r"^(жду|ждём|ожидаю)"),
]
LINE = re.compile(r"^(\d\d:\d\d:\d\d\.\d+) (.*)$")


def category(text):
    for name, pat in CATS:
        if re.match(pat, text):
            return name
    return "прочее"


def parse(path):
    rows = []
    day = 0
    prev = None
    for ln in open(path, encoding="utf-8", errors="replace"):
        m = LINE.match(ln.rstrip("\n"))
        if not m:
            continue
        t = datetime.strptime(m.group(1)[:15], "%H:%M:%S.%f") + timedelta(days=day)
        if prev and t < prev - timedelta(hours=1):  # полночь
            day += 1
            t += timedelta(days=1)
        rows.append((t, m.group(2)))
        prev = t
    return rows


def stages_arg(args):
    res = set()
    for a in args:
        if "-" in a:
            x, y = a.split("-")
            res.update(range(int(x), int(y) + 1))
        else:
            res.add(int(a))
    return res


def main():
    args = sys.argv[1:]
    seed = None
    if "-s" in args:
        i = args.index("-s")
        seed = args[i + 1]
        args = args[:i] + args[i + 2:]
    want = stages_arg(args)
    root = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "run", "stages")
    files = sorted(glob.glob(os.path.join(root, "s*-*.log")))
    names = [c for c, _ in CATS] + ["прочее"]
    print(f"{'стадия':>6} {'сид':>11} {'всего':>6} " + " ".join(f"{n:>13}" for n in names) + "   долгие паузы")
    for f in files:
        m = re.match(r"s(\d+)-(\d+)\.log$", os.path.basename(f))
        if not m:
            continue
        n, s = int(m.group(1)), m.group(2)
        if want and n not in want or seed and s != seed:
            continue
        rows = parse(f)
        if len(rows) < 2:
            continue
        acc = defaultdict(float)
        long = []
        for (t, text), (t2, _) in zip(rows, rows[1:]):
            d = (t2 - t).total_seconds()
            acc[category(text)] += d
            if d > 300:
                long.append(f"{int(d // 60)} мин после «{text[:40]}»")
        total = (rows[-1][0] - rows[0][0]).total_seconds()
        cells = " ".join(f"{acc[c] / 60:10.0f} мин" for c in names)
        print(f"{n:>6} {s:>11} {total / 60:5.0f}м {cells}   {'; '.join(long[:3])}")


if __name__ == "__main__":
    main()
