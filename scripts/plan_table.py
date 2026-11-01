"""Таблица: какие производственные здания ставит каждая стадия (по раскладкам стадий на бумаге),
их выход (по скоростям машин) и сколько нужно на науку уровня (bin/bot calc)."""
import json, sys, subprocess, collections, math

run = sys.argv[1]               # папка прогона без игры одной карты (state-*.json)
P = json.load(open('data/prototypes-2.1.24-botbridge0.28.5.json'))
E, R = P['entities'], P['recipes']

STAGES = [  # стадия, файл раскладки
    (11, 'state-power.json'), (16, 'state-coal.json'), (17, 'state-smelting.json'),
    (18, 'state-bus-plates.json'), (19, 'state-make-iron-gear-wheel.json'),
    (20, 'state-make-automation-science-pack.json'), (21, 'state-labs.json'),
    (23, 'state-make-electronic-circuit.json'), (23, 'state-grow.json'),
    (24, 'state-make-inserter.json'), (25, 'state-make-transport-belt.json'),
    (26, 'state-make-logistic-science-pack.json'), (27, 'state-labs-green.json'),
    (29, 'state-power-blue.json'), (30, 'state-income.json'), (31, 'state-steel.json'),
    (32, 'state-fast.json'), (33, 'state-tanks.json'), (34, 'state-oil.json'),
    (35, 'state-water.json'), (36, 'state-refinery.json'), (37, 'state-chem.json'),
    (38, 'state-engines.json'), (39, 'state-adv-circuits.json'), (40, 'state-blue-science.json'),
    (41, 'state-labs-blue.json'), (43, 'state-brick.json'), (44, 'state-electric-furnaces.json'),
    (45, 'state-modules.json'), (46, 'state-rails.json'), (47, 'state-purple-science.json'),
    (48, 'state-labs-purple.json'), (50, 'state-grow-yellow.json'), (51, 'state-refinery-adv.json'),
    (52, 'state-cracking.json'), (53, 'state-lubricant.json'), (54, 'state-acid.json'),
    (55, 'state-battery.json'), (56, 'state-electric-engines.json'), (57, 'state-frames.json'),
    (58, 'state-lds.json'), (59, 'state-processing-units.json'), (60, 'state-yellow-science.json'),
    (61, 'state-labs-yellow.json')]
LEVEL = lambda n: 3 if n <= 22 else 4 if n <= 28 else 5 if n <= 41 else 6 if n <= 48 else 7
SCI = ['automation-science-pack', 'logistic-science-pack', 'chemical-science-pack',
       'production-science-pack', 'utility-science-pack']
MACH = ('assembling-machine', 'furnace', 'mining-drill', 'chemical-plant', 'oil-refinery', 'lab', 'boiler')

plan = json.load(open(f'{run}/state-plan.json'))
clusters = {c['name']: c for c in plan['clusters']}
ORE = {'iron-ore', 'copper-ore', 'coal', 'stone'}

def is_machine(name):
    t = E.get(name, {}).get('type', '')
    return t in ('assembling-machine', 'furnace', 'mining-drill', 'lab', 'boiler')

def rate(pl):
    """(продукт, выход в секунду) одной машины."""
    n, e = pl['name'], E.get(pl['name'], {})
    if e.get('type') == 'boiler':
        return 'пар (МВт)', 1.8
    if e.get('type') == 'lab':
        return 'лаборатория', 0
    if n == 'pumpjack':
        return 'crude-oil', None
    c = clusters.get(pl.get('tag', '')[2:].split('/')[0])
    if e.get('type') == 'mining-drill':
        ore = c['product'] if c else '?'
        return ore, e['mining_speed'] / 1.0
    if e.get('type') == 'furnace':
        prod = pl.get('recipe') or (c['product'] if c else '?')
        r = R.get(prod)
        return prod, (e['crafting_speed'] / r['energy'] * r['products'][0]['amount']) if r else None
    r = R.get(pl.get('recipe', ''))
    if not r:
        return pl.get('recipe') or '?', None
    p = r['products'][0]
    return p['name'], e['crafting_speed'] / r['energy'] * p['amount']

def required(level):
    tg = []
    for s in SCI[:level - 2]:
        tg += [s, '0.12/s']
    out = subprocess.run(['bin/bot', 'calc', '-all', '-json'] + tg, capture_output=True, text=True).stdout
    d = json.loads(out[out.index('{'):])
    need = collections.defaultdict(float)
    for nd in d['Nodes']:
        for k, v in nd['Out'].items():
            need[k] += v
    need['пар (МВт)'] = sum(nd['PowerW'] for nd in d['Nodes']) / 1e6
    return need

REQ = {lv: required(lv) for lv in range(3, 8)}
seen = set()
have = collections.defaultdict(float)   # продукт → выход всего
rows = []
per = {}
for n, f in STAGES:
    try:
        lst = json.load(open(f'{run}/{f}'))
    except FileNotFoundError:
        continue
    if not isinstance(lst, list):
        continue
    new = collections.Counter()
    for pl in lst:
        if 'name' not in pl or not is_machine(pl['name']):
            continue
        key = (pl['name'], pl['pos']['x'], pl['pos']['y'])
        if key in seen:
            continue
        seen.add(key)
        prod, r = rate(pl)
        new[(prod, pl['name'])] += 1
        per[(prod, pl['name'])] = r
        if r:
            have[prod] += r
    for (prod, m), k in sorted(new.items()):
        req = REQ[LEVEL(n)].get(prod)
        r1 = per.get((prod, m))
        nn = math.ceil(req / r1 - 1e-9) if req and r1 else None
        rows.append((n, prod, m, k, have.get(prod), req, nn))

def fmt(x):
    if x is None: return '—'
    return f'{x:.2f}' if x < 10 else f'{x:.0f}'
print('| Стадия | Продукт | Здание | Ставит, шт | Выход всего, /с | Нужно на науку уровня, /с | Нужно всего, шт |')
print('|---|---|---|---|---|---|---|')
for n, prod, m, k, h, req, nn in rows:
    print(f'| {n} | {prod} | {m} | {k} | {fmt(h)} | {fmt(req)} | {nn if nn is not None else "—"} |')
