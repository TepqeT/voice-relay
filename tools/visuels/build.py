import base64
import os
HERE = os.path.dirname(os.path.abspath(__file__))
A = os.path.join(HERE, "avatars")
def img(n): return "data:image/png;base64," + base64.b64encode(open(f"{A}/{n}.png", "rb").read()).decode()

W, H = 1600, 945
CX = W // 2

chefs = "".join(f'<div class="chef"><span class="dot"></span>Chef {i}</div>' for i in range(1, 6))

squads = ""
for i in range(5):
    squads += f'''
    <div class="sq">
      <div class="sq-t">Escouade {i+1}</div>
      <img src="{img(f'ely-{i+1}')}">
      <div class="sq-row">
        <div class="mini-chef">Chef</div>
        <svg width="34" height="14"><line x1="32" y1="7" x2="8" y2="7" stroke="#8fe0a8" stroke-width="2.5"/><path d="M10,1 L0,7 L10,13 z" fill="#8fe0a8"/></svg>
        <div class="crowd"><span></span><span></span><span></span><span></span></div>
      </div>
    </div>'''

html = f'''<!doctype html><html><head><meta charset="utf-8"><style>
*{{box-sizing:border-box;margin:0;padding:0}}
body{{width:{W}px;height:{H}px;font-family:Inter,sans-serif;color:#efe7d6;position:relative;overflow:hidden;
 background:radial-gradient(ellipse at 50% -10%,#24406e 0%,#121a33 45%,#090c17 100%)}}
.top{{position:absolute;top:26px;left:0;right:0;text-align:center}}
.kicker{{font:600 15px Inter;letter-spacing:.42em;color:#d8b25a}}
h1{{font:700 50px Lora,serif;margin-top:4px;color:#f6edd8;text-shadow:0 0 30px rgba(216,178,90,.35)}}
.subtitle{{font:400 19px Inter;color:#c9bfae;margin-top:4px}}

.tier{{position:absolute;left:50%;transform:translateX(-50%);text-align:center}}
.t1{{top:162px;width:560px;height:150px;clip-path:polygon(18% 0,82% 0,100% 100%,0 100%);
 background:linear-gradient(180deg,rgba(242,194,92,.42),rgba(120,84,20,.55));}}
.t2{{top:322px;width:860px;height:170px;clip-path:polygon(16% 0,84% 0,100% 100%,0 100%);
 background:linear-gradient(180deg,rgba(120,190,255,.30),rgba(30,70,130,.55));}}
.t3{{top:502px;width:1180px;height:250px;clip-path:polygon(12% 0,88% 0,100% 100%,0 100%);
 background:linear-gradient(180deg,rgba(150,150,170,.22),rgba(40,44,70,.6));}}
.lvl{{font:700 26px Lora,serif;color:#fff3d6;letter-spacing:.03em}}
.t1 .in{{padding-top:16px;display:flex;align-items:center;justify-content:center;gap:16px}}
.t1 img{{width:74px;height:74px;border-radius:50%;box-shadow:0 0 18px rgba(242,194,92,.6)}}
.t1 .lvl{{color:#ffe3a3}}
.desc{{font:500 14.5px Inter;color:#efe7d6;margin-top:4px;line-height:1.35}}
.desc b{{color:#ffd77a;font-weight:700}}
.t2 .in{{padding-top:14px}}
.t2 .lvl{{color:#cfeaff}}
.chefs{{display:flex;justify-content:center;gap:12px;margin-top:10px}}
.chef{{display:flex;align-items:center;gap:6px;padding:5px 12px;border-radius:999px;background:#bfe3ff;color:#0d1a2a;font:700 13px Inter}}
.chef .dot{{width:9px;height:9px;border-radius:50%;background:#2a6fd6}}
.t3 .in{{padding-top:10px}}
.squads{{display:flex;justify-content:center;gap:14px;margin-top:8px}}
.sq{{width:148px;padding:8px 6px 10px;border-radius:12px;background:rgba(9,12,23,.85);border:1.5px solid rgba(226,184,84,.7)}}
.sq-t{{font:700 15px Lora,serif}}
.sq img{{width:46px;height:46px;border-radius:50%;margin-top:4px}}
.sq-row{{display:flex;align-items:center;justify-content:center;gap:4px;margin-top:6px}}
.mini-chef{{padding:3px 7px;border-radius:999px;background:#bfe3ff;color:#0d1a2a;font:700 10.5px Inter}}
.crowd{{display:flex;gap:3px}}
.crowd span{{width:10px;height:10px;border-radius:50%;background:#a59d8e}}
.t3 .desc{{margin-top:8px}}

.side{{position:absolute;font:600 14px Inter;line-height:1.35}}
.side.l{{left:46px;top:300px;width:230px;text-align:right;color:#f2c25c}}
.side.r1{{right:46px;top:236px;width:250px;color:#bfe3ff}}
.side.r2{{right:46px;top:560px;width:250px;color:#bfe3ff}}
.side small{{display:block;font:400 13px Inter;color:#c9bfae;margin-top:3px}}
svg.arrows{{position:absolute;left:0;top:0}}

.legend{{position:absolute;bottom:62px;left:40px;right:40px;display:flex;gap:18px}}
.lg{{flex:1;border-radius:14px;background:rgba(9,12,23,.75);border:1px solid rgba(239,231,214,.15);padding:14px 18px}}
.lg b{{display:block;font:700 17px Lora,serif;margin-bottom:4px}}
.lg span{{font:400 14px Inter;color:#d8cfbe;line-height:1.4}}
.lg.g b{{color:#ffd77a}} .lg.b b{{color:#bfe3ff}} .lg.m b{{color:#8fe0a8}}
.foot{{position:absolute;bottom:24px;left:0;right:0;text-align:center;font:400 13.5px Inter;color:#a59d8e}}
</style></head><body>
<div class="top">
  <div class="kicker">RVR AION 2 FRANCE - ÉLYSÉENS</div>
  <h1>Qui entend qui ?</h1>
  <div class="subtitle">Le relais vocal RvR en 3 niveaux. Chacun parle normalement, aucune touche à presser.</div>
</div>

<svg class="arrows" width="{W}" height="{H}">
  <defs>
    <marker id="g" markerWidth="10" markerHeight="10" refX="6" refY="5" orient="auto"><path d="M0,0 L10,5 L0,10 z" fill="#f2c25c"/></marker>
    <marker id="b" markerWidth="10" markerHeight="10" refX="6" refY="5" orient="auto"><path d="M0,0 L10,5 L0,10 z" fill="#bfe3ff"/></marker>
  </defs>
  <path d="M440,250 C330,330 300,470 320,640" fill="none" stroke="#f2c25c" stroke-width="6" marker-end="url(#g)"/>
  <path d="M1110,420 C1210,380 1230,300 1140,250" fill="none" stroke="#bfe3ff" stroke-width="4" stroke-dasharray="9 7" marker-end="url(#b)"/>
  <path d="M1150,440 C1260,500 1290,560 1270,640" fill="none" stroke="#bfe3ff" stroke-width="4" stroke-dasharray="9 7" marker-end="url(#b)"/>
</svg>

<div class="side l">Calls des Raid Leads<small>entendus dans toutes les escouades</small></div>
<div class="side r1">Infos des chefs<small>remontent aux Raid Leads</small></div>
<div class="side r2">Infos des chefs<small>entendues dans toutes les autres escouades</small></div>

<div class="tier t1"><div class="in">
  <img src="{img('ely-voix')}">
  <div><div class="lvl">Raid Leads</div>
  <div class="desc">Salon Raid Lead<br>Entendent <b>tous les chefs d'escouade</b></div></div>
</div></div>

<div class="tier t2"><div class="in">
  <div class="lvl">Chefs d'escouade</div>
  <div class="chefs">{chefs}</div>
  <div class="desc">Entendus par <b>les Raid Leads</b> et par <b>toutes les escouades</b><br>Entendent les Raid Leads, les autres chefs et leur escouade</div>
</div></div>

<div class="tier t3"><div class="in">
  <div class="lvl">Membres d'escouade</div>
  <div class="squads">{squads}</div>
  <div class="desc">Votre voix reste <b>dans votre escouade</b> : seuls votre chef et vos coéquipiers l'entendent.<br>Donnez vos infos à votre chef, il les fait remonter à tout le monde.</div>
</div></div>

<div class="legend">
  <div class="lg g"><b>Tu es Raid Lead</b><span>Ta voix part dans toutes les escouades. Tu entends tous les chefs d'escouade.</span></div>
  <div class="lg b"><b>Tu es chef d'escouade</b><span>Ta voix part aux Raid Leads et dans toutes les escouades. Tu entends aussi ton escouade : c'est elle qui te renseigne.</span></div>
  <div class="lg m"><b>Tu es membre d'une escouade</b><span>Tu entends les Raid Leads, tous les chefs et ton escouade. Ta voix reste dans ton escouade.</span></div>
</div>
<div class="foot">Les Hérauts (bots) portent les voix d'un salon à l'autre. Ton propre chef n'est jamais rediffusé dans ton salon : il y est déjà.</div>
</body></html>'''
open(os.path.join(HERE, "pyra.html"), "w", encoding="utf-8").write(html)
