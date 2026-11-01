"""Чьи ленты и трубы идут вдоль коридоров: свои (соседи коридора), кластеров той же стопки (выше/ниже
в столбце), сквозные (продукт на другой этаж) и прочие."""
import json, sys, collections
def inside(r, z):
    return r['Min']['X'] >= z['Min']['X'] - 1 and r['Max']['X'] <= z['Max']['X'] + 1 and r['Min']['Y'] >= z['Min']['Y'] - 1 and r['Max']['Y'] <= z['Max']['Y'] + 1
kinds = collections.Counter(); per = collections.Counter(); lanes_by_stack = collections.defaultdict(list)
for run in sys.argv[1:]:
    try:
        zones = json.load(open(f'{run}/state-zones.json'))['zones']; plan = json.load(open(f'{run}/state-plan.json'))
    except Exception:
        continue
    occ = collections.defaultdict(list)
    for pl in plan['layout']:
        occ[(int(pl['pos']['x'] // 1), int(pl['pos']['y'] // 1))].append(pl)
    cl = [c for c in plan['clusters'] if c['kind'] != 'bus']
    byname = {c['name']: c for c in cl}
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
                    best = (g, y0, y1, b)
            if not best: continue
            g, y0, y1, b = best
            x0 = a['area']['Max']['X']
            # кластеры стопок a и b (тот же столбец по x, другой y)
            stack = [c for c in inz if c is not a and c is not b and (min(c['area']['Max']['X'], a['area']['Max']['X']) - max(c['area']['Min']['X'], a['area']['Min']['X']) > 0 or min(c['area']['Max']['X'], b['area']['Max']['X']) - max(c['area']['Min']['X'], b['area']['Min']['X']) > 0)]
            stack_in = sum(len(c.get('in') or []) for c in stack)
            used = 0
            for x in range(x0, x0 + g):
                tags = collections.Counter()
                for y in range(y0, y1):
                    for pl in occ.get((x, y), []):
                        if 'pole' in pl['name']: continue
                        tags[pl['tag']] += 1
                if not tags: continue
                tag, n = tags.most_common(1)[0]
                if n < 0.3 * (y1 - y0): continue
                used += 1
                owner = tag.split('/')[-1]
                if owner in (a['name'], b['name']): kinds['свои (соседи коридора)'] += 1
                elif owner in [c['name'] for c in stack]: kinds['кластеры той же стопки'] += 1
                elif tag.startswith('c/bus-') or '/bus-' in tag and owner not in byname: kinds['ленты шин и сквозные'] += 1
                elif owner in byname: kinds['другие кластеры зоны'] += 1
                else: kinds['прочее: ' + tag.split('/')[1]] += 1
            lanes_by_stack[min(stack_in, 8)].append(used)
tot = sum(kinds.values())
for k, v in kinds.most_common():
    print(f'{k}: {v} ({v/tot:.0%})')
print('входов у кластеров стопки → колонок занято (медиана, макс, коридоров):')
import statistics
for k in sorted(lanes_by_stack):
    v = lanes_by_stack[k]
    print(f'  {k}: {statistics.median(v)}, {max(v)}, {len(v)}')
