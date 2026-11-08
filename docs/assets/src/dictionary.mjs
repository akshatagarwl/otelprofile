import fs from 'node:fs';
import { svg, C } from './lib.mjs';

const s = svg(900, 400, { dur: 12, freezeAt: 4.4 });
const T = (str, x, y, o) => s.text(str, x, y, o);
const RH = 34, Y0 = 86;
let n = 0;
// opacity on during each [a,b] window (percent)
const windows = (ws, f = 1.5) => {
  const fr = [[0, 'opacity:0']];
  for (const [a, b] of ws) fr.push([a - f, 'opacity:0'], [a, 'opacity:1'], [b, 'opacity:1'], [b + f, 'opacity:0']);
  fr.push([100, 'opacity:0']);
  return fr;
};
const hl = (x, y, w, color, ws) => {
  const cls = `h${n++}`;
  s.group(cls, () => s.raw(`<rect x="${x + 4}" y="${y + 3}" width="${w - 8}" height="${RH - 6}" rx="5" fill="${color}"/>`));
  s.anim(cls, windows(ws));
};
const later = (cls, ws, fn) => { s.group(cls, fn); s.anim(cls, windows(ws)); };

const A = [3, 44], B = [50, 94];
const tables = {
  samples: { x: 16, w: 224, color: C.orange, rows: ['stack 0 · 30 ms · link 1', 'stack 1 · 10 ms · link 1', 'stack 0 · 20 ms · link 0'] },
  stack_table: { x: 262, w: 128, color: C.ink, rows: ['0: loc 0, 1, 2', '1: loc 3, 1, 2'] },
  location_table: { x: 410, w: 110, color: C.ink, rows: ['0: fn 0', '1: fn 1', '2: fn 2', '3: fn 3'] },
  function_table: { x: 540, w: 110, color: C.ink, rows: ['0: str 1', '1: str 2', '2: str 3', '3: str 4'] },
  string_table: { x: 670, w: 216, color: C.ink, rows: ['0: ""', '1: "main.spinLCG"', '2: "main.slowHandler"', '3: "net/http.(*conn).serve"', '4: "main.queryDB"'] },
};
// highlight schedule: [table, row, color, windows]
const Y = C.fyellow, G = C.fgreen, Bl = C.fblue;
const sched = [
  ['samples', 0, Y, [[3, 44]]], ['stack_table', 0, Y, [[9, 44]]],
  ['location_table', 0, Y, [[15, 44]]], ['location_table', 1, Y, [[15, 44]]], ['location_table', 2, Y, [[15, 44]]],
  ['function_table', 0, Y, [[21, 44]]], ['function_table', 1, Y, [[21, 44]]], ['function_table', 2, Y, [[21, 44]]],
  ['string_table', 1, Y, [[27, 44]]], ['string_table', 2, Y, [[27, 44]]], ['string_table', 3, Y, [[27, 44]]],
  ['samples', 1, G, [[50, 94]]], ['stack_table', 1, G, [[56, 94]]],
  ['location_table', 3, G, [[62, 94]]], ['function_table', 3, G, [[68, 94]]], ['string_table', 4, G, [[74, 94]]],
  ['location_table', 1, Bl, [[62, 94]]], ['location_table', 2, Bl, [[62, 94]]],
  ['function_table', 1, Bl, [[68, 94]]], ['function_table', 2, Bl, [[68, 94]]],
  ['string_table', 2, Bl, [[74, 94]]], ['string_table', 3, Bl, [[74, 94]]],
];
for (const [name, t] of Object.entries(tables)) {
  const h = RH * t.rows.length + 8;
  s.rect(t.x, Y0 - 4, t.w, h, { stroke: t.color, fill: name === 'samples' ? C.forange : C.fgrey, roughness: 0.8 });
  T(name, t.x + 4, Y0 - 14, { size: 15, weight: 600, color: name === 'samples' ? C.orange : C.ink });
  for (const [tn, row, col, ws] of sched) if (tn === name) hl(t.x, Y0 + row * RH, t.w, col, ws);
  t.rows.forEach((r, i) => T(r, t.x + 12, Y0 + i * RH + 22, { size: 14 }));
}
// link table
const LY = 250;
s.rect(16, LY - 4, 374, RH * 2 + 8, { stroke: C.blue, fill: C.fblue, roughness: 0.8 });
T('link_table', 20, LY - 14, { size: 15, weight: 600, color: C.blue });
hl(16, LY + RH, 374, C.fyellow, [[33, 44], [80, 94]]);
T('0: (no span)', 28, LY + 22, { size: 14 });
T('1: trace 4bf92f35… span 00f067aa…', 28, LY + RH + 22, { size: 14 });

// arrows that appear with each step
const ar = (cls, ws, ...pts) => later(cls, ws, () => s.arrow(...pts, { stroke: C.ink, strokeWidth: 1.6, head: 8 }));
ar('a1', [[9, 44]], 240, Y0 + 17, 262, Y0 + 17);
ar('a2', [[15, 44]], 390, Y0 + 17, 410, Y0 + 17);
ar('a3', [[21, 44]], 520, Y0 + 17, 540, Y0 + 17);
ar('a4', [[27, 44]], 650, Y0 + 17, 670, Y0 + 17 + RH);
ar('a5', [[33, 44]], 120, Y0 + 3 * RH + 6, 120, LY + RH + 4);
ar('b1', [[56, 94]], 240, Y0 + RH + 17, 262, Y0 + RH + 17);
ar('b2', [[62, 94]], 390, Y0 + RH + 17, 410, Y0 + 3 * RH + 17);
ar('b3', [[68, 94]], 520, Y0 + 3 * RH + 17, 540, Y0 + 3 * RH + 17);
ar('b4', [[74, 94]], 650, Y0 + 3 * RH + 17, 670, Y0 + 4 * RH + 17);
ar('b5', [[80, 94]], 150, Y0 + 3 * RH + 6, 150, LY + RH + 4);

// captions
const cap = (cls, ws, lines) => later(cls, ws, () => lines.forEach((l, i) => T(l, 420, 280 + i * 26, { size: 16, color: i ? C.grey : C.ink, weight: i ? 400 : 600 })));
cap('c1', [[3, 44]], ['a sample is a few integers', 'stack › locations › functions › strings,', 'each one an index into a shared table']);
cap('c2', [[50, 94]], ['the next sample reuses rows', 'only loc 3 and "main.queryDB" are new;', 'every string is sent once per request']);
later('c3', [[80, 94]], () => T('same span, same link row', 210, LY + RH * 2 + 32, { size: 14, color: C.blue }));
later('c4', [[33, 44]], () => T('link_index › trace and span', 210, LY + RH * 2 + 32, { size: 14, color: C.blue }));

T('One ProfilesDictionary per OTLP request, shared by every profile in it', 450, 386, { anchor: 'middle', size: 14, color: C.grey });
fs.writeFileSync('../dictionary.svg', s.render());
console.log('dictionary', fs.statSync('../dictionary.svg').size);
