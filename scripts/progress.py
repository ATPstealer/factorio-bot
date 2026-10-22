#!/usr/bin/env python3
"""Прогресс e2e: читает вывод `go test -v` со stdin, пропускает его дальше как есть и после
каждого законченного теста печатает строку прогресса:

    [########............] 8/20 40%  ok 6  fail 1  skip 1  | Walk ok 4.0s

Та же строка — в файл (аргумент 2, перезаписывается) и в консоль игры экземпляра (обычный
текст по RCON: игра показывает его как сообщение сервера — только отображение).
Аргументы: всего тестов, файл прогресса.
"""
import re
import subprocess
import sys

total = max(int(sys.argv[1]), 1)
out = sys.argv[2]
done = {"ok": 0, "fail": 0, "skip": 0}
res = {"PASS": "ok", "FAIL": "fail", "SKIP": "skip"}
line_re = re.compile(r"^--- (PASS|FAIL|SKIP): Test(\S+) \(([\d.]+)s\)")
WIDTH = 20

for line in sys.stdin:
    sys.stdout.write(line)
    sys.stdout.flush()
    m = line_re.match(line)  # только тесты верхнего уровня (подтесты — с отступом)
    if not m:
        continue
    kind, name, secs = res[m.group(1)], m.group(2), float(m.group(3))
    done[kind] += 1
    n = sum(done.values())
    fill = round(WIDTH * n / total)
    bar = "#" * fill + "." * (WIDTH - fill)
    msg = (f"[{bar}] {n}/{total} {100 * n // total}%  ok {done['ok']}  fail {done['fail']}  "
           f"skip {done['skip']}  | {name} {kind} {secs:.1f}s")
    sys.stdout.write(f"    прогресс: {msg}\n")
    sys.stdout.flush()
    try:
        with open(out, "w") as f:
            f.write(msg + "\n")
        subprocess.run(["scripts/rcon.py", f"[тесты] {msg}"], timeout=5,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except Exception:
        pass
