#!/usr/bin/env python3
"""Статистика e2e: разобрать вывод go test (строки «--- PASS/FAIL/SKIP: TestX (N.NNs)»),
дописать прогоны в test/e2e/stats.tsv и пересобрать таблицу в test/e2e/README.md
(между <!-- stats --> и <!-- /stats -->).

  scripts/test_stats.py ЛОГ [ДАТА]   — учесть прогон из лога
  scripts/test_stats.py              — только пересобрать таблицу
"""
import os
import re
import sys
from datetime import datetime

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
STATS = os.path.join(ROOT, "test", "e2e", "stats.tsv")
README = os.path.join(ROOT, "test", "e2e", "README.md")
LINE = re.compile(r"^--- (PASS|FAIL|SKIP): Test(?:Base_|Extra_)?([A-Za-z0-9]+) \(([0-9.]+)s\)")


def load():
    st = {}
    if os.path.exists(STATS):
        with open(STATS) as f:
            next(f, None)
            for ln in f:
                name, runs, passed, failed, skipped, last, when, secs = ln.rstrip("\n").split("\t")
                st[name] = dict(runs=int(runs), passed=int(passed), failed=int(failed), skipped=int(skipped),
                                last=last, when=when, secs=secs)
    return st


def save(st):
    with open(STATS, "w") as f:
        f.write("test\truns\tpassed\tfailed\tskipped\tlast\twhen\tsecs\n")
        for name in sorted(st):
            s = st[name]
            f.write(f"{name}\t{s['runs']}\t{s['passed']}\t{s['failed']}\t{s['skipped']}\t{s['last']}\t{s['when']}\t{s['secs']}\n")


def add(st, log, when):
    with open(log, errors="replace") as f:
        for ln in f:
            m = LINE.match(ln)
            if not m:
                continue
            res, name, secs = m.groups()
            s = st.setdefault(name, dict(runs=0, passed=0, failed=0, skipped=0, last="", when="", secs=""))
            s["runs"] += 1
            s[{"PASS": "passed", "FAIL": "failed", "SKIP": "skipped"}[res]] += 1
            s["last"], s["when"], s["secs"] = res, when, secs


def table(st):
    mark = {"PASS": "✅ прошёл", "FAIL": "❌ упал", "SKIP": "⏭ пропущен"}
    rows = ["| Тест | Прогонов | Прошёл | Упал | Пропущен | Последний раз | Когда | Время, с |",
            "|---|---|---|---|---|---|---|---|"]
    for name in sorted(st):
        s = st[name]
        rows.append(f"| `{name}` | {s['runs']} | {s['passed']} | {s['failed']} | {s['skipped']} | "
                    f"{mark.get(s['last'], s['last'])} | {s['when']} | {s['secs']} |")
    return "\n".join(rows)


def render(st):
    with open(README) as f:
        text = f.read()
    a, b = "<!-- stats -->", "<!-- /stats -->"
    if a not in text or b not in text:
        return
    head, rest = text.split(a, 1)
    _, tail = rest.split(b, 1)
    with open(README, "w") as f:
        f.write(head + a + "\n" + table(st) + "\n" + b + tail)


def main():
    st = load()
    if len(sys.argv) > 1:
        when = sys.argv[2] if len(sys.argv) > 2 else datetime.now().strftime("%Y-%m-%d %H:%M")
        add(st, sys.argv[1], when)
        save(st)
    render(st)


if __name__ == "__main__":
    main()
