#!/usr/bin/env python3
# Тест ходьбы: scripts/walk.py [клавиша[:секунды] ...], например: scripts/walk.py d s:0.5 a w
# Держит клавишу через XTEST в дисплее $BOT_DISPLAY (по умолчанию :1), печатает позицию игрока по RCON.
import ctypes, os, subprocess, sys, time

RC = os.path.join(os.path.dirname(os.path.abspath(__file__)), "rcon.py")
DISPLAY = os.environ.get("BOT_DISPLAY", ":1").encode()

X = ctypes.cdll.LoadLibrary("libX11.so.6")
T = ctypes.cdll.LoadLibrary("libXtst.so.6")
X.XOpenDisplay.restype = ctypes.c_void_p
X.XStringToKeysym.restype = ctypes.c_ulong
X.XKeysymToKeycode.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
T.XTestFakeKeyEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
T.XTestFakeMotionEvent.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_ulong]
X.XFlush.argtypes = [ctypes.c_void_p]

d = X.XOpenDisplay(DISPLAY)
if not d:
    sys.exit(f"не открыть дисплей {DISPLAY.decode()}")


def key(k, down):
    T.XTestFakeKeyEvent(d, X.XKeysymToKeycode(d, X.XStringToKeysym(k.encode())), down, 0)
    X.XFlush(d)


def pos():
    lua = ('/sc local p=game.connected_players[1] rcon.print(string.format("%.2f %.2f %s",'
           'p.position.x,p.position.y,tostring(p.character.walking_state.walking)))')
    return subprocess.run([RC, lua], capture_output=True, text=True).stdout.strip()


# курсор в центр окна: без WM фокус клавиатуры следует за указателем
T.XTestFakeMotionEvent(d, -1, 960, 540, 0)
X.XFlush(d)

print("start", pos())
for arg in sys.argv[1:]:
    k, _, sec = arg.partition(":")
    key(k, 1)
    time.sleep(float(sec or 1))
    print(arg, "held ", pos())
    key(k, 0)
    time.sleep(0.3)
    print(arg, "after", pos())
