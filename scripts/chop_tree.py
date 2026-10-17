#!/usr/bin/env python3
# Найти ближайшее дерево, дойти до него клавишами и срубить ПКМ: scripts/chop_tree.py [радиус поиска]
# Ввод — xdotool в $BOT_DISPLAY (:1), состояние — read-only запросы по RCON.
import json, math, os, subprocess, sys, time

from rcon import Rcon

DISPLAY = os.environ.get("BOT_DISPLAY", ":1")
STOP_DIST = 2.5  # тайлов до дерева, где останавливаемся (reach персонажа — 10)
DEAD = 0.3  # тайлов: по оси ближе — клавишу не держим

r = Rcon()


def q(lua):
    out = r.cmd("/sc local p=game.connected_players[1] rcon.print(helpers.table_to_json(" + lua + "))")
    return json.loads(out)


def xdo(*args):
    subprocess.run(["xdotool", *map(str, args)], env={**os.environ, "DISPLAY": DISPLAY}, check=True)


def player():
    return q('{x=p.position.x, y=p.position.y, zoom=p.zoom, w=p.display_resolution.width, '
             'h=p.display_resolution.height, sel=p.selected and {name=p.selected.name, type=p.selected.type, '
             'x=p.selected.position.x, y=p.selected.position.y} or nil, '
             'wood=p.get_main_inventory().get_item_count("wood")}')


def find_tree(radius):
    return q('(function() local best,bd for _,e in pairs(p.surface.find_entities_filtered{type="tree",'
             f'position=p.position,radius={radius}}}) do local dx,dy=e.position.x-p.position.x,'
             'e.position.y-p.position.y local dd=dx*dx+dy*dy if not bd or dd<bd then best,bd=e,dd end end '
             'return best and {name=best.name,x=best.position.x,y=best.position.y} or {} end)()')


def tree_exists(t):
    return q(f'{{n=#p.surface.find_entities_filtered{{type="tree",position={{{t["x"]},{t["y"]}}},radius=0.01}}}}')["n"] > 0


def walk_to(tx, ty, timeout=30):
    held = set()
    last, last_t = None, time.time()
    t0 = time.time()
    try:
        while time.time() - t0 < timeout:
            p = player()
            dx, dy = tx - p["x"], ty - p["y"]
            if math.hypot(dx, dy) <= STOP_DIST:
                return True
            want = set()
            if dx > DEAD: want.add("d")
            if dx < -DEAD: want.add("a")
            if dy > DEAD: want.add("s")
            if dy < -DEAD: want.add("w")
            # застряли — пробуем обойти перпендикулярно
            if last and math.hypot(p["x"] - last[0], p["y"] - last[1]) < 0.05 and time.time() - last_t > 0.5:
                want = {"w", "a"} if "d" in want or "s" in want else {"s", "d"}
            if not last or math.hypot(p["x"] - last[0], p["y"] - last[1]) >= 0.05:
                last, last_t = (p["x"], p["y"]), time.time()
            for k in held - want: xdo("keyup", k)
            for k in want - held: xdo("keydown", k)
            held = want
            time.sleep(0.05)
        return False
    finally:
        for k in held: xdo("keyup", k)


def to_screen(p, wx, wy):
    ppt = 32 * p["zoom"]  # пикселей на тайл
    return round(p["w"] / 2 + (wx - p["x"]) * ppt), round(p["h"] / 2 + (wy - p["y"]) * ppt)


def hover(t):
    """Навести курсор на дерево; подтверждение — player.selected. Перебор смещений вокруг ствола."""
    for oy in (0, -0.5, -1, 0.3, -1.5):
        for ox in (0, -0.3, 0.3):
            p = player()
            sx, sy = to_screen(p, t["x"] + ox, t["y"] + oy)
            xdo("mousemove", "--window", WIN, sx, sy)
            time.sleep(0.1)
            s = player()["sel"]
            if s and s["type"] == "tree" and abs(s["x"] - t["x"]) < 0.01 and abs(s["y"] - t["y"]) < 0.01:
                return True
    return False


def mine(t, timeout=10):
    xdo("mousedown", 3)
    try:
        t0 = time.time()
        while time.time() - t0 < timeout:
            if not tree_exists(t):
                return True
            time.sleep(0.1)
        return False
    finally:
        xdo("mouseup", 3)


WIN = subprocess.run(["xdotool", "search", "--name", "^Factorio"], env={**os.environ, "DISPLAY": DISPLAY},
                     capture_output=True, text=True, check=True).stdout.split()[0]

if __name__ == "__main__":
    radius = int(sys.argv[1]) if len(sys.argv) > 1 else 50
    p = player()
    tree = find_tree(radius)
    if not tree:
        sys.exit(f"дерева в радиусе {radius} нет")
    print(f"игрок ({p['x']:.1f}, {p['y']:.1f}), дерево {tree['name']} ({tree['x']:.1f}, {tree['y']:.1f}), "
          f"{math.hypot(tree['x'] - p['x'], tree['y'] - p['y']):.1f} тайлов, дерева в инвентаре: {p['wood']}")

    # фокус клавиатуры следует за курсором (в Xephyr нет WM)
    xdo("mousemove", "--window", WIN, p["w"] // 2, p["h"] // 2)
    if not walk_to(tree["x"], tree["y"]):
        sys.exit("не дошёл")
    p = player()
    print(f"дошёл: ({p['x']:.1f}, {p['y']:.1f}), до дерева {math.hypot(tree['x'] - p['x'], tree['y'] - p['y']):.1f}")

    if not hover(tree):
        sys.exit("не удалось навести курсор на дерево")
    print("курсор на дереве, рублю")
    if not mine(tree):
        sys.exit("не срубил за отведённое время")
    time.sleep(0.2)
    print(f"срублено, дерева в инвентаре: {player()['wood']}")
