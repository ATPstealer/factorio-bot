#!/usr/bin/env python3
"""Спячка прогонов (скил hybernate): остановить и потом продолжить с того же места.

    scripts/hybernate.py on    — идущие прогоны стадий: что и где (сид, стадия, конец диапазона),
                                 остановить (stage.sh, бот, игра — сервер сохраняет карту),
                                 сохранение и состояние экземпляра — в run/hybernate/iN/,
                                 список — run/hybernate/state.json
    scripts/hybernate.py off   — вернуть сохранения на экземпляры и продолжить каждую карту с её
                                 стадии: CONT=1 INST=N PAR=0 scripts/stage.sh K-M <сид>
                                 (журнал — run/out/run-<сид>.out, как у прогона); прогон fullrun —
                                 CONT=1 FROM=K TO=M INST=N … scripts/fullrun.sh <сид> с теми же
                                 LOOP, EXPLORE, SHOW (журнал — run/out/fullrun-N.out)
    scripts/hybernate.py show  — что записано

Контекст (что делалось, задачи, вопросы) пишет агент в run/hybernate/hybernate.md (скил).
"""
import glob
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HYB = f"{ROOT}/run/hybernate"
STATE = f"{HYB}/state.json"
# что экземпляра сохранить: карта сервера, состояние бота, сид, поворот руки
KEEP = ["server/saves/bot.zip", "map.json", "hand.json"]


FULLRUN_ENV = ["LOOP", "EXPLORE", "SHOW", "TO", "LIMIT", "RETRIES", "AUTOSAVE"]


def procs():
    """Процессы экземпляров: [(pid, N, exe, args)] — bot, stage.sh и fullrun.sh (у него args —
    [сид?, {настройки}])."""
    res = []
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
            res.append((int(pid), n, "bot", args[1:]))
        elif any(a.endswith("scripts/stage.sh") for a in args):
            i = args.index(next(a for a in args if a.endswith("stage.sh")))
            res.append((int(pid), n, "stage.sh", args[i + 1:]))
        elif any(a.endswith("scripts/fullrun.sh") for a in args):
            i = args.index(next(a for a in args if a.endswith("fullrun.sh")))
            res.append((int(pid), n, "fullrun.sh", args[i + 1:] + [{k: envd[k] for k in FULLRUN_ENV if k in envd}]))
    return res


def last_done(seed):
    """Последняя выполненная стадия сида по журналам run/stages/sN-<сид>.log (fullrun)."""
    done = 0
    for f in glob.glob(f"{ROOT}/run/stages/s*-{seed}.log"):
        m = re.match(r"s(\d+)-", os.path.basename(f))
        if m and any(re.search(rf"стадия {m[1]} выполнена|выполнена \(пустая\)|ракета уже улетела", l)
                     for l in open(f, errors="replace")):
            done = max(done, int(m[1]))
    return done


def runs(ps):
    """Прогоны по экземплярам: {N: {seed, stage, end}} — стадия идущая (бот или stage.sh N),
    конец — из stage.sh K-M."""
    res = {}
    for pid, n, exe, args in ps:
        r = res.setdefault(n, {"inst": n, "seed": None, "stage": None, "end": None})
        if exe == "fullrun.sh":
            r["kind"], r["env"] = "fullrun", args[-1]
            r["end"] = int(args[-1].get("TO", "68"))
            continue
        if exe == "stage.sh" and args:
            rng, seeds = args[0], args[1:]
            if seeds:
                r["seed"] = seeds[0]
            if "-" in rng:
                r["end"] = int(rng.split("-")[1])
                r["stage"] = r["stage"] or int(rng.split("-")[0])
            elif rng.isdigit():
                r["stage"] = int(rng)  # идущая стадия (вложенный stage.sh N)
        if exe == "bot" and "stage" in args:
            k = args[args.index("stage") + 1]
            if k.isdigit():
                r["stage"] = int(k)
    for n, r in list(res.items()):
        if r["seed"] is None:
            try:
                r["seed"] = str(json.load(open(f"{ROOT}/run/i{n}/map.json"))["seed"])
            except (OSError, ValueError, KeyError):
                del res[n]
                continue
        r["end"] = r["end"] or r["stage"]
        busy = any(k == n and (exe == "bot" or (exe == "stage.sh" and args and args[0].isdigit()))
                   for _, k, exe, args in ps)
        if r.get("kind") == "fullrun":  # сид — карта экземпляра (LOOP берёт новые)
            r["seed"] = str(json.load(open(f"{ROOT}/run/i{n}/map.json"))["seed"])
            if not busy:  # между стадиями (снимок из автосейва) — следующая после выполненной
                r["stage"] = last_done(r["seed"]) + 1
            continue
        if not busy:  # между стадиями: следующая после последней выполненной
            done = [int(m[1]) for f in glob.glob(f"{ROOT}/run/stages/ok-*-{r['seed']}")
                    if (m := re.match(r"ok-(\d+)-", os.path.basename(f)))]
            done = [k for k in done if k <= r["end"]]
            if done:
                r["stage"] = max(r["stage"] or 0, max(done) + 1)
    return res


def stop_inst(n, ps):
    """fullrun.sh и stage.sh экземпляра (иначе начнут повтор), потом бот (SIGTERM — клавиши
    отпускает), потом игра (stop.sh)."""
    for sig_order in (("fullrun.sh", "stage.sh"), ("bot",)):
        pids = [pid for pid, k, exe, _ in ps if k == n and exe in sig_order]
        for pid in pids:
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        deadline = time.time() + 30
        while time.time() < deadline and any(os.path.exists(f"/proc/{p}") for p in pids):
            time.sleep(1)
        for pid in pids:
            if os.path.exists(f"/proc/{pid}"):
                os.kill(pid, signal.SIGKILL)
    subprocess.run(["timeout", "200", "scripts/stop.sh"], cwd=ROOT, env={**os.environ, "INST": str(n)},
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def save_inst(n):
    dst = f"{HYB}/i{n}"
    shutil.rmtree(dst, ignore_errors=True)
    os.makedirs(dst)
    src = f"{ROOT}/run/i{n}"
    files = KEEP + [os.path.relpath(f, src) for f in glob.glob(f"{src}/state*.json")]
    for rel in files:
        if os.path.exists(f"{src}/{rel}"):
            os.makedirs(os.path.dirname(f"{dst}/{rel}"), exist_ok=True)
            shutil.copy2(f"{src}/{rel}", f"{dst}/{rel}")
    return files


def restore_inst(n):
    src = f"{HYB}/i{n}"
    dst = f"{ROOT}/run/i{n}"
    for f in glob.glob(f"{dst}/state*.json"):  # состояние — ровно как было
        os.remove(f)
    for root, _, names in os.walk(src):
        for name in names:
            rel = os.path.relpath(f"{root}/{name}", src)
            os.makedirs(os.path.dirname(f"{dst}/{rel}"), exist_ok=True)
            shutil.copy2(f"{root}/{name}", f"{dst}/{rel}")


def on():
    ps = procs()
    rs = runs(ps)
    if not rs:
        print("прогонов нет — нечего усыплять")
        return
    os.makedirs(HYB, exist_ok=True)
    for n, r in sorted(rs.items()):
        print(f"экземпляр {n}: сид {r['seed']}, стадия {r['stage']} (до {r['end']}) — останавливаю")
        stop_inst(n, ps)
        save_inst(n)
    json.dump({"when": time.strftime("%Y-%m-%d %H:%M"), "runs": [rs[n] for n in sorted(rs)]},
              open(STATE, "w"), ensure_ascii=False, indent=1)
    print(f"записано: {STATE}")


def off():
    st = json.load(open(STATE))
    busy = {n for _, n, _, _ in procs()}
    for r in st["runs"]:
        n = r["inst"]
        if n in busy:
            print(f"экземпляр {n} занят — {r['seed']} не продолжаю")
            continue
        restore_inst(n)
        if r.get("kind") == "fullrun":
            env = " ".join(f"{k}={v}" for k, v in sorted(r.get("env", {}).items()) if k != "TO")
            cmd = (f"mkdir -p run/out; CONT=1 FROM={r['stage']} TO={r['end']} INST={n} {env} "
                   f"scripts/fullrun.sh {r['seed']} >> run/out/fullrun-{n}.out 2>&1")
        else:
            cmd = (f"mkdir -p run/out; CONT=1 INST={n} PAR=0 scripts/stage.sh {r['stage']}-{r['end']} {r['seed']} "
                   f">> run/out/run-{r['seed']}.out 2>&1; echo \"код $?\" >> run/out/run-{r['seed']}.out; "
                   f"INST={n} timeout 150 scripts/stop.sh > /dev/null 2>&1")
        subprocess.Popen(["setsid", "nohup", "bash", "-c", cmd], cwd=ROOT,
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        print(f"экземпляр {n}: сид {r['seed']} — со стадии {r['stage']} до {r['end']}")
        time.sleep(8)  # экземпляры — не разом: загрузка игры тяжёлая
    os.rename(STATE, f"{HYB}/state-{time.strftime('%Y%m%d-%H%M')}.json")


def show():
    st = json.load(open(STATE))
    print(f"усыплено {st['when']}")
    for r in st["runs"]:
        print(f"экземпляр {r['inst']}: сид {r['seed']}, стадия {r['stage']} (до {r['end']})")


if __name__ == "__main__":
    {"on": on, "off": off, "show": show}.get(sys.argv[1] if len(sys.argv) > 1 else "", lambda: print(__doc__))()
