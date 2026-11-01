"""Коридоры между кластерами в зонах этажей и заполнение зон шин — по плану (state-plan.json) карты."""
import json, sys, collections, statistics

def load(run):
    z = json.load(open(f'{run}/state-zones.json'))['zones']
    p = json.load(open(f'{run}/state-plan.json'))
    return z, p

def inside(r, z):
    return r['Min']['X'] >= z['Min']['X'] - 1 and r['Max']['X'] <= z['Max']['X'] + 1 and r['Min']['Y'] >= z['Min']['Y'] - 1 and r['Max']['Y'] <= z['Max']['Y'] + 1

gaps_all = collections.defaultdict(list)
fill = collections.defaultdict(list)
bus = collections.defaultdict(list)
for run in sys.argv[1:]:
    try:
        zones, plan = load(run)
    except Exception:
        continue
    cl = [c for c in plan['clusters'] if c['kind'] not in ('bus',)]
    for z in zones:
        zr = z['rect']
        w, h = zr['Max']['X'] - zr['Min']['X'], zr['Max']['Y'] - zr['Min']['Y']
        if z['kind'] == 'bus':
            # ряды шины: тайлы по y, где лежат ленты/трубы её тега вдоль шины
            rows = collections.Counter()
            for pl in plan['layout']:
                if pl['tag'] == 'c/' + z['name'] or pl['tag'].startswith('c/' + z['name'] + '/'):
                    y = int(pl['pos']['y'] // 1)
                    if zr['Min']['Y'] <= y < zr['Max']['Y']:
                        rows[y] += 1
            used = sum(1 for y, n in rows.items() if n >= 20)  # ряд с лентой вдоль (не поперечные подводы)
            bus[z['name']].append((h, used))
            continue
        if z['kind'] not in ('processing', 'supply', 'final'):
            continue
        inz = [c for c in cl if inside(c['area'], zr)]
        area = sum((c['area']['Max']['X'] - c['area']['Min']['X']) * (c['area']['Max']['Y'] - c['area']['Min']['Y']) for c in inz)
        if w * h:
            fill[z['kind']].append(area / (w * h))
        # соседи по горизонтали: перекрытие по y, ближайший справа
        for a in inz:
            best = None
            for b in inz:
                if b is a:
                    continue
                oy = min(a['area']['Max']['Y'], b['area']['Max']['Y']) - max(a['area']['Min']['Y'], b['area']['Min']['Y'])
                g = b['area']['Min']['X'] - a['area']['Max']['X']
                if oy > 0 and g >= 0 and (best is None or g < best):
                    best = g
            if best is not None:
                gaps_all[z['kind']].append(best)
for k, g in gaps_all.items():
    c = collections.Counter(g)
    print(f'{k}: промежутков {len(g)}, медиана {statistics.median(g)}, 0:{c[0]} 1:{c[1]} 2:{c[2]} 3–5:{sum(c[i] for i in range(3,6))} 6–10:{sum(c[i] for i in range(6,11))} >10:{sum(v for i,v in c.items() if i>10)}')
for k, f in fill.items():
    print(f'заполнение {k}: медиана {statistics.median(f):.0%}, от {min(f):.0%} до {max(f):.0%} ({len(f)} зон)')
for k, v in sorted(bus.items()):
    print(f'{k}: высота зоны {[h for h,_ in v]}, рядов с лентой/трубой вдоль {[u for _,u in v]}')

# занятость коридоров: в промежутке между соседями — сколько колонок с лентой/трубой вдоль
print('--- занятость коридоров (колонок с постройками на ≥30% высоты перекрытия)')
usedc = collections.Counter()
tot = 0
for run in sys.argv[1:]:
    try:
        zones, plan = load(run)
    except Exception:
        continue
    occ = {}
    for pl in plan['layout']:
        occ[(int(pl['pos']['x'] // 1), int(pl['pos']['y'] // 1))] = pl['name']
    cl = [c for c in plan['clusters'] if c['kind'] != 'bus']
    for z in zones:
        if z['kind'] not in ('processing', 'supply', 'final'):
            continue
        inz = [c for c in cl if inside(c['area'], z['rect'])]
        for a in inz:
            best = None
            for b in inz:
                if b is a: continue
                y0 = max(a['area']['Min']['Y'], b['area']['Min']['Y']); y1 = min(a['area']['Max']['Y'], b['area']['Max']['Y'])
                g = b['area']['Min']['X'] - a['area']['Max']['X']
                if y1 - y0 > 0 and g >= 0 and (best is None or g < best[0]):
                    best = (g, y0, y1)
            if not best: continue
            g, y0, y1 = best
            x0 = a['area']['Max']['X']
            cols = 0
            for x in range(x0, x0 + g):
                n = sum(1 for y in range(y0, y1) if (x, y) in occ)
                if n >= 0.3 * (y1 - y0):
                    cols += 1
            usedc[cols] += 1
            tot += 1
print({k: usedc[k] for k in sorted(usedc)}, 'всего', tot)
