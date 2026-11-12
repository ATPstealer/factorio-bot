#!/usr/bin/env python3
"""Отчёт по экземплярам (скил /report): таблица — экземпляр, сид, стадия, перезапуски, что делает.

    scripts/report.py

Только чтение: процессы (/proc), run/iN/map.json, журналы стадий run/stages/sN-<сид>.log.
"""
import glob
import json
import os
import re
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TS = re.compile(r"^(\d\d):(\d\d):(\d\d)\.\d+ (.*)$")


def procs():
    """Процессы экземпляров: {N: [что запущено]} — bot, stage.sh, test.sh."""
    res = {}
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        try:
            cmd = open(f"/proc/{pid}/cmdline", "rb").read().split(b"\0")
            env = open(f"/proc/{pid}/environ", "rb").read().split(b"\0")
        except OSError:
            continue
        args = [a.decode(errors="replace") for a in cmd if a]
        if not args:
            continue
        envd = dict(e.decode(errors="replace").split("=", 1) for e in env if b"=" in e)
        n = None
        if m := re.search(r"/run/i(\d)$", envd.get("BOT_RUN", "")):
            n = int(m[1])
        elif envd.get("INST", "").isdigit():
            n = int(envd["INST"])
        if n is None:
            continue
        exe = os.path.basename(args[0])
        if exe == "bot":
            res.setdefault(n, []).append("bot " + " ".join(a for a in args[1:] if not a.startswith("-")))
        elif any(a.endswith("scripts/stage.sh") for a in args):
            res.setdefault(n, []).append("stage.sh " + " ".join(args[args.index(next(a for a in args if a.endswith("stage.sh"))) + 1:]))
        elif any(a.endswith("scripts/fullrun.sh") for a in args):
            # между стадиями бот не запущен: fullrun ждёт автосейва для снимка
            res.setdefault(n, []).append("fullrun.sh (между стадиями: снимок из автосейва)")
        elif any(a.endswith("scripts/test.sh") for a in args):
            res.setdefault(n, []).append("test.sh " + " ".join(args[-1:]))
    return res


def secs(m):
    return int(m[1]) * 3600 + int(m[2]) * 60 + int(m[3])


def dur(s):
    s = int(s)
    return f"{s // 3600} ч {s % 3600 // 60:02d} мин" if s >= 3600 else f"{s // 60} мин"


def stage_log(seed):
    """Последний журнал стадии сида: (номер, путь)."""
    best = None
    for f in glob.glob(f"{ROOT}/run/stages/s*-{seed}.log"):
        if m := re.match(r"s(\d+)-", os.path.basename(f)):
            if best is None or os.path.getmtime(f) > os.path.getmtime(best[1]):
                best = (int(m[1]), f)
    return best


def rerun_ok(seed, n):
    """Упавшая стадия перепрогнана со снимка <сид>-(n-1) на другом экземпляре одним запуском
    (без повторов) и прошла: журнал run/out/rerun-<сид>-<n>.out кончается «стадия n выполнена»."""
    try:
        lines = [l.rstrip("\n") for l in open(f"{ROOT}/run/out/rerun-{seed}-{n}.out", errors="replace") if l.strip()]
    except OSError:
        return False
    return bool(lines) and lines[-1].endswith(f"стадия {n} выполнена")


def restarts(seed, upto):
    """Перезапуски стадий сида до стадии upto включительно: повторы на сохранении и продолжения
    («== повтор», «== продолжение» в журнале стадии; журнал стадии с её начала — заново).
    Текст: «всего (стадия×раз, …)», нет — «0»; ✓ у стадии — перепрогнана со снимка на другом
    экземпляре и прошла с первого раза (rerun_ok)."""
    per = {}
    for f in glob.glob(f"{ROOT}/run/stages/s*-{seed}.log"):
        m = re.match(r"s(\d+)-", os.path.basename(f))
        if not m or int(m[1]) > upto:
            continue
        k = sum(1 for l in open(f, errors="replace") if l.startswith(("== повтор", "== продолжение")))
        if k:
            per[int(m[1])] = k
    if not per:
        return "0"
    return f"{sum(per.values())} (" + ", ".join(
        f"{n}" + (f"×{k}" if k > 1 else "") + (" ✓" if rerun_ok(seed, n) else "") for n, k in sorted(per.items())) + ")"


def main():
    ps = procs()
    rows = []
    for n in range(1, 5):
        try:
            seed = json.load(open(f"{ROOT}/run/i{n}/map.json"))["seed"]
        except (OSError, ValueError, KeyError):
            rows.append((f"bot-{n}", "—", "—", "нет карты", "—", ""))
            continue
        running = ps.get(n, [])
        # карта другого экземпляра, у которого идёт прогон (снимок для перепрогона или разбора):
        # журналы стадий — его, не этого
        other = next((k for k in range(1, 5) if k != n and ps.get(k) and os.path.exists(f"{ROOT}/run/i{k}/map.json")
                      and json.load(open(f"{ROOT}/run/i{k}/map.json")).get("seed") == seed), None)
        if other and not running:
            rows.append((f"bot-{n}", str(seed), "—", f"свободен (снимок карты bot-{other})", "—", ""))
            continue
        sl = stage_log(seed)
        if not sl:
            rows.append((f"bot-{n}", str(seed), "—", "журнала стадий нет", "—", "; ".join(running)))
            continue
        num, path = sl
        lines = [m for l in open(path, errors="replace") if (m := TS.match(l.rstrip("\n")))]
        name = next((m[4].split(": ", 1)[1] for m in lines if m[4].startswith(f"стадия {num}: ")), "")
        limit = next((m[4].split("— ", 1)[-1].split(" ")[0] for m in lines if m[4].startswith("лимит стадии")), "")
        last = lines[-1][4] if lines else ""
        el = (secs(lines[-1]) - secs(lines[0])) % 86400 if lines else 0
        idle = time.time() - os.path.getmtime(path)
        bot = any(r.startswith("bot ") for r in running)
        if bot:
            state = f"идёт {dur(el)}" + (f" (лимит {limit})" if limit else "")
            now = last or "только началась"
        elif re.match(rf"стадия {num} выполнена", last):
            state = f"выполнена за {dur(el)}" + ("" if running else ", стоит")
            now = "; ".join(running) or "прогон окончен"
        else:
            state = f"упала через {dur(el)}, стоит {dur(idle)}" if not running else f"упала; {'; '.join(running)}"
            now = last
        if len(now) > 110:
            now = now[:107] + "…"
        rows.append((f"bot-{n}", str(seed), f"{num} {name}", state, restarts(seed, num), now))
    print("| | сид | стадия | состояние | перезапусков (стадии) | сейчас |")
    print("|---|---|---|---|---|---|")
    for r in rows:
        print("| " + " | ".join(c.replace("|", "/") for c in r) + " |")


if __name__ == "__main__":
    main()
