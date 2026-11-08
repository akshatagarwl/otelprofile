import fs from 'node:fs';
import { svg, C, show } from './lib.mjs';

const s = svg(900, 330, { dur: 9, freezeAt: 6.6 });
const T = (str, x, y, o) => s.text(str, x, y, o);

// your service
s.rect(16, 70, 196, 200, { stroke: C.blue, fill: C.fblue });
T('your Go service', 114, 100, { anchor: 'middle', weight: 600, size: 19 });
T('runtime/pprof', 114, 150, { anchor: 'middle', size: 16, color: C.grey });
T('godeltaprof', 114, 175, { anchor: 'middle', size: 16, color: C.grey });
s.rect(34, 210, 160, 40, { stroke: C.ink, fill: '#fff' });
T('otelprofile.Start()', 114, 236, { anchor: 'middle', size: 15 });
// clock
s.ellipse(114, 32, 36, 36, { stroke: C.ink, fill: C.fyellow });
s.group('hand', () => s.line(114, 32, 114, 20, { strokeWidth: 2.5 }));
T('every 15 s', 140, 38, { size: 15 });
s.anim('hand', [[0, 'transform:rotate(0deg)'], [100, 'transform:rotate(360deg)']], 'transform-origin:114px 32px');

// otelprofile box with three steps
s.rect(240, 44, 410, 250, { stroke: C.violet, fill: C.fviolet });
T('otelprofile', 445, 76, { anchor: 'middle', weight: 600, size: 19 });
const step = (x, title, sub1, sub2) => {
  s.rect(x, 106, 120, 150, { stroke: C.ink, fill: '#fff' });
  T(title, x + 60, 134, { anchor: 'middle', weight: 600, size: 16 });
  T(sub1, x + 60, 226, { anchor: 'middle', size: 13, color: C.grey });
  T(sub2, x + 60, 244, { anchor: 'middle', size: 13, color: C.grey });
};
step(254, 'collect', 'cpu, heap, mutex,', 'goroutine, block');
step(386, 'pprof to OTLP', 'one dictionary,', 'deduplicated');
step(518, 'gzip', 'retries,', 'Retry-After');
s.arrow(212, 170, 250, 170);
s.arrow(375, 170, 384, 170, { head: 7 });
s.arrow(507, 170, 516, 170, { head: 7 });

// receivers
const rx = [['OTel Collector', 40, C.green, C.fgreen], ['Grafana Pyroscope', 135, C.orange, C.forange], ['sigiro', 230, C.blue, C.fblue]];
for (const [name, y, st, fl] of rx) {
  s.rect(676, y, 214, 58, { stroke: st, fill: fl });
  T(name, 768, y + 35, { anchor: 'middle', weight: 600, size: 16 });
  s.arrow(640, 175, 670, y + 29, { bend: 0 });
}
T('POST /v1development/profiles', 660, 318, { anchor: 'middle', size: 14, color: C.grey });

// moving pieces
const cards = [['cpu', C.orange], ['heap', C.green], ['goroutine', C.blue], ['mutex', C.red], ['block', C.violet]];
cards.forEach(([n, col], i) => {
  const cls = `c${i}`;
  s.group(cls, () => { s.raw(`<rect x="214" y="150" width="54" height="18" rx="4" fill="${col}"/>`); T(n, 241, 164, { anchor: 'middle', size: n.length > 6 ? 10 : 12, color: '#fff', weight: 600 }); });
  const [X, Y] = [[259, 146], [316, 146], [259, 168], [316, 168], [288, 190]][i], dx = X - 214, dy = Y - 150;
  s.anim(cls, [[0, 'opacity:0;transform:translate(0,0)'], [4 + i * 2, 'opacity:0;transform:translate(0,0)'], [8 + i * 2, 'opacity:1;transform:translate(0,0)'],
    [26, `opacity:1;transform:translate(${dx}px,${dy}px)`], [36, `opacity:1;transform:translate(${dx}px,${dy}px)`], [42, `opacity:0;transform:translate(${180}px,0px)`], [100, 'opacity:0']]);
});
// OTLP envelope: forms in step 2, shrinks in step 3, then fans out
const env = (cls, frames) => {
  s.group(cls, () => {
    s.raw(`<rect x="410" y="152" width="72" height="44" rx="5" fill="${C.violet}"/><path d="M410 156 L446 178 L482 156" stroke="#fff" stroke-width="2" fill="none"/>`);
    T('OTLP', 446, 191, { anchor: 'middle', size: 12, color: '#fff', weight: 600 });
  });
  s.anim(cls, frames, 'transform-box:fill-box;transform-origin:center');
};
env('e0', [[0, 'opacity:0'], [38, 'opacity:0;transform:translate(0,0) scale(.5)'], [44, 'opacity:1;transform:translate(0,0) scale(1)'], [52, 'opacity:1;transform:translate(0,0) scale(1)'],
  [58, 'opacity:1;transform:translate(128px,0) scale(.6)'], [64, 'opacity:1;transform:translate(128px,0) scale(.6)'], [66, 'opacity:0;transform:translate(128px,0) scale(.6)'], [100, 'opacity:0']]);
rx.forEach(([, y], i) => env(`f${i}`, [[0, 'opacity:0'], [64, 'opacity:0;transform:translate(128px,0) scale(.6)'], [66, 'opacity:1;transform:translate(128px,0) scale(.6)'],
  [78, `opacity:1;transform:translate(298px,${y + 29 - 174}px) scale(.5)`], [80, `opacity:0;transform:translate(298px,${y + 29 - 174}px) scale(.5)`], [100, 'opacity:0']]));
rx.forEach(([, y, st], i) => {
  s.group(`k${i}`, () => s.raw(`<path d="M860 ${y + 26} l8 9 l16 -18" stroke="${C.green}" stroke-width="4" fill="none" stroke-linecap="round" stroke-linejoin="round"/>`));
  s.anim(`k${i}`, show(80 + i * 2, 96));
});

fs.writeFileSync('../pipeline.svg', s.render());
console.log('pipeline', fs.statSync('../pipeline.svg').size);
