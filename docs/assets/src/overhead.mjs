import fs from 'node:fs';
import { svg, C } from './lib.mjs';

const s = svg(900, 340, { dur: 8, freezeAt: 4 });
const T = (str, x, y, o) => s.text(str, x, y, o);
const panels = [
  ['CPU', 'extra CPU seconds in 32 min', 0.6, 4.7, v => `+${v} s`],
  ['allocations', 'extra MB allocated in 32 min', 143, 153, v => `+${v} MB`],
  ['memory', 'extra mean RSS', 6.3, 5.7, v => `+${v} MB`],
];
const BASE = 262, MAXH = 150;
panels.forEach(([title, sub, a, b, fmt], i) => {
  const x = 22 + i * 292, max = Math.max(a, b);
  T(title, x + 130, 34, { anchor: 'middle', size: 18, weight: 600 });
  T(sub, x + 130, 56, { anchor: 'middle', size: 13, color: C.grey });
  s.line(x + 20, BASE, x + 240, BASE, { strokeWidth: 2 });
  [[a, C.blue, 'otelprofile', 50], [b, C.orange, 'pyroscope-go', 140]].forEach(([v, col, name, off], j) => {
    const h = Math.max(6, (v / max) * MAXH), bx = x + off;
    const cls = `b${i}${j}`;
    s.group(cls, () => s.rect(bx, BASE - h, 70, h, { stroke: col, hachure: col, roughness: 1.3 }));
    const t0 = 6 + i * 8 + j * 3;
    s.anim(cls, [[0, 'transform:scaleY(0)'], [t0, 'transform:scaleY(0)'], [t0 + 14, 'transform:scaleY(1)'], [92, 'transform:scaleY(1)'], [98, 'transform:scaleY(0)'], [100, 'transform:scaleY(0)']], 'transform-box:fill-box;transform-origin:50% 100%');
    const vc = `v${i}${j}`;
    s.group(vc, () => T(fmt(v), bx + 35, BASE - h - 10, { anchor: 'middle', size: 16, weight: 600, color: col }));
    s.anim(vc, [[0, 'opacity:0'], [t0 + 14, 'opacity:0'], [t0 + 18, 'opacity:1'], [92, 'opacity:1'], [95, 'opacity:0'], [100, 'opacity:0']]);
    T(name, bx + 35, BASE + 22, { anchor: 'middle', size: 13, color: col });
  });
});
T('Same demo app, 32-minute soak, linux/arm64 container, cpu + heap every 15 s, compared with the app running no profiler', 450, 318, { anchor: 'middle', size: 13, color: C.grey });
fs.writeFileSync('../overhead.svg', s.render());
console.log('overhead', fs.statSync('../overhead.svg').size);
