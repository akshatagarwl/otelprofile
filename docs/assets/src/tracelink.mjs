import fs from 'node:fs';
import { svg, C } from './lib.mjs';

const s = svg(900, 390, { dur: 11, freezeAt: 9.2 });
const T = (str, x, y, o) => s.text(str, x, y, o);
const windows = (ws, f = 1.5) => {
  const fr = [[0, 'opacity:0']];
  for (const [a, b] of ws) fr.push([a - f, 'opacity:0'], [a, 'opacity:1'], [b, 'opacity:1'], [b + f, 'opacity:0']);
  return fr.concat([[100, 'opacity:0']]);
};
const later = (cls, ws, fn) => { s.group(cls, fn); s.anim(cls, windows(ws)); };

const X0 = 130, X1 = 880, S0 = 210, S1 = 640, Q0 = 380, Q1 = 560;
T('trace', 16, 74, { size: 15, color: C.grey });
T('goroutine', 16, 158, { size: 15, color: C.grey });
T('CPU samples', 16, 206, { size: 15, color: C.grey });
T('100 Hz', 16, 224, { size: 13, color: C.grey });

// timeline content, revealed by a sliding cover
s.rect(S0, 50, S1 - S0, 34, { stroke: C.blue, fill: C.fblue });
T('GET /slow', S0 + 12, 73, { size: 15, weight: 600, color: C.blue });
T('span 00f067aa…', S1 - 12, 73, { size: 13, color: C.blue, anchor: 'end' });
s.rect(Q0, 92, Q1 - Q0, 30, { stroke: C.violet, fill: C.fviolet });
T('query-db', Q0 + 12, 112, { size: 14, weight: 600, color: C.violet });
T('span 1c2d…', Q1 - 10, 112, { size: 12, color: C.violet, anchor: 'end' });
s.line(X0, 152, X1, 152, { stroke: C.grey, strokeWidth: 1.5, roughness: 0.6 });
s.raw(`<rect x="${S0}" y="146" width="${S1 - S0}" height="12" rx="4" fill="${C.blue}" opacity=".18"/>`);
T('pprof labels: otel.trace_id, otel.span_id', (S0 + S1) / 2, 176, { size: 13, color: C.blue, anchor: 'middle' });
const ticks = [];
for (let x = X0 + 6; x < X1 - 6; x += 22) {
  const col = x >= Q0 && x < Q1 ? C.violet : x >= S0 && x < S1 ? C.blue : C.grey;
  ticks.push([x, col]);
  s.raw(`<rect x="${x}" y="194" width="12" height="${col === C.grey ? 18 : 24}" rx="3" fill="${col}" opacity="${col === C.grey ? 0.45 : 0.9}"/>`);
}
s.group('cover', () => s.raw(`<rect x="${X0}" y="40" width="${X1 - X0 + 12}" height="196" fill="#fff"/><path d="M${X0} 44 v188" stroke="${C.red}" stroke-width="2.5"/>`));
s.anim('cover', [[0, 'transform:translateX(0)'], [52, `transform:translateX(${X1 - X0 + 40}px)`], [100, `transform:translateX(${X1 - X0 + 40}px)`]]);

// export box and backend box
s.rect(16, 262, 420, 112, { stroke: C.ink, fill: C.fgrey });
T('exporter', 30, 290, { size: 16, weight: 600 });
T('labels become Sample.link_index', 30, 316, { size: 15 });
T('link 1 = trace 4bf92f35…, span 00f067aa… (GET /slow)', 30, 340, { size: 13, color: C.blue });
T('link 2 = trace 4bf92f35…, span 1c2d… (query-db)', 30, 358, { size: 13, color: C.violet });
s.rect(480, 262, 404, 112, { stroke: C.blue, fill: C.fblue });
T('any OTLP backend', 494, 290, { size: 16, weight: 600 });
T('pick a span › "Open profile"', 494, 316, { size: 14 });
// tiny flame graph of that span
later('fg', [[74, 97]], () => {
  const fr = [[0, 600, C.yellow], [1, 300, C.orange], [1, 270, C.orange], [2, 220, C.red], [2, 90, C.red]];
  let rows = { 0: 0, 1: 0, 2: 0 };
  const bars = [[0, 0, 300], [1, 0, 190], [1, 192, 108], [2, 0, 140], [2, 192, 90]];
  bars.forEach(([r, x, w], i) => s.raw(`<rect x="${494 + x}" y="${330 + r * 13}" width="${w - 2}" height="11" rx="2" fill="${[C.yellow, C.orange, C.orange, C.red, C.red][i]}" opacity=".85"/>`));
  void fr; void rows;
});
later('ar1', [[68, 97]], () => s.arrow(438, 318, 476, 318, { strokeWidth: 2 }));

// linked samples fly into the exporter
const linked = ticks.filter(([, c]) => c !== C.grey);
s.group('fly', () => linked.forEach(([x, col]) => s.raw(`<rect x="${x}" y="194" width="12" height="24" rx="3" fill="${col}"/>`)));
s.anim('fly', [[0, 'opacity:0;transform:translate(0,0) scale(1)'], [54, 'opacity:0;transform:translate(0,0) scale(1)'], [56, 'opacity:1;transform:translate(0,0) scale(1)'],
  [66, 'opacity:1;transform:translate(-180px,118px) scale(.35)'], [69, 'opacity:0;transform:translate(-180px,118px) scale(.35)'], [100, 'opacity:0']], 'transform-box:fill-box;transform-origin:center');
later('cap', [[4, 50]], () => T('while a span runs, its goroutine carries the trace and span IDs as pprof labels', 450, 252, { size: 13, color: C.grey, anchor: 'middle' }));

fs.writeFileSync('../tracelink.svg', s.render());
console.log('tracelink', fs.statSync('../tracelink.svg').size);
