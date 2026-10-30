#!/usr/bin/env python3
# Цена постановки лент по журналу стадии: секунды на ленту в пачках ≥20 без ходьбы (ускорение, ночь 30–31.10).
#   scripts/belt_rate.py run/stages/s30-<сид>.log
import re,sys
t=open(sys.argv[1]).read().splitlines()
def ts(l):
    h,m,s=l[:15].split(":"); return int(h)*3600+int(m)*60+float(s)
res=[]; i=0
while i<len(t):
    m=re.search(r"ставлю transport-belt ×(\d+)",t[i])
    if m:
        n=int(m.group(1)); t0=ts(t[i]); walk=0; j=i+1
        while j<len(t) and not re.search(r"^\S+ (ставлю|крафчу|беру|кладу|рецепт|фильтр|стадия|рост|добыча|подбираю|поворачиваю|не встало|протяжка)",t[j]):
            mm=re.search(r"ходьба до .*всего ([0-9.]+) с",t[j])
            if mm: walk+=float(mm.group(1))
            j+=1
        if j<len(t):
            d=ts(t[j])-t0
            if d<0: d+=86400
            res.append((n,d,walk))
        i=j
    else: i+=1
big=[r for r in res if r[0]>=20]
if big: print(f"{sys.argv[1]}: пачек ≥20 лент {len(big)}, лент {sum(r[0] for r in big)}, {sum(r[1]-r[2] for r in big)/sum(r[0] for r in big):.2f} с/лента без ходьбы, ходьба в пачках {sum(r[2] for r in big)/60:.0f} мин, всего {sum(r[1] for r in big)/60:.0f} мин; протяжек не довело: {sum(1 for l in t if 'протяжка' in l)}")
else: print(f"{sys.argv[1]}: пачек ≥20 ещё нет ({len(res)} пачек)")
