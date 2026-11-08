// Tiny toolkit for hand-drawn (tldraw-style) animated SVGs that render inside <img> on GitHub:
// rough strokes from roughjs, text as Shantell Sans glyph outlines (no font loading), CSS keyframes.
import rough from 'roughjs/bundled/rough.esm.js';
import opentype from 'opentype.js';
import fs from 'node:fs';

const fontFile = w => `node_modules/@fontsource/shantell-sans/files/shantell-sans-latin-${w}-normal.woff`;
const fonts = { 400: opentype.parse(fs.readFileSync(fontFile(400)).buffer), 600: opentype.parse(fs.readFileSync(fontFile(600)).buffer) };

const pathData = p => p.commands.map(c => c.type === 'Z' ? 'Z' : c.type + [c.x1, c.y1, c.x, c.y].filter(v => v !== undefined).map(Math.round).join(' ')).join('');

export const C = {
  ink: '#1d1d1d', grey: '#7d8590', blue: '#4465e9', lblue: '#4ba1f1', green: '#099268', orange: '#e16919',
  red: '#e03131', violet: '#ae3ec9', yellow: '#f1ac4b',
  fblue: '#e4e9fd', fgreen: '#d6f2e5', forange: '#fde9dc', fviolet: '#f5e3fa', fyellow: '#fff1cc', fred: '#fde2e2', fgrey: '#eef0f2',
};

export function svg(W, H, { dur, freezeAt }) {
  const gen = rough.generator();
  let seed = 1;
  const glyphs = new Map();
  const body = [];
  const css = [];
  const r1 = n => Math.round(n * 10) / 10;
  const paths = (drawable, extra = '') => gen.toPaths(drawable).map(p =>
    `<path d="${p.d.replace(/(\d+\.\d)\d+/g, '$1')}" stroke="${p.stroke}" stroke-width="${p.strokeWidth}" fill="${p.fill ?? 'none'}"${extra}/>`).join('');
  const o = (opt = {}) => ({ seed: seed++, roughness: 1.1, bowing: 1, strokeWidth: 2, stroke: C.ink, ...opt });
  const api = {
    W, H, dur,
    raw: s => (body.push(s), api),
    group(cls, fn, attrs = '') { body.push(`<g${cls ? ` class="${cls}"` : ''}${attrs}>`); fn(); body.push('</g>'); return api; },
    rect(x, y, w, h, opt = {}) {
      if (opt.fill) body.push(`<rect x="${r1(x + 2)}" y="${r1(y + 2)}" width="${r1(w - 4)}" height="${r1(h - 4)}" rx="${opt.rx ?? 6}" fill="${opt.fill}"/>`);
      body.push(paths(gen.rectangle(x, y, w, h, o({ ...opt, fill: opt.hachure, fillStyle: 'hachure', hachureGap: 5, fillWeight: 1.5 }))));
      return api;
    },
    ellipse(cx, cy, w, h, opt = {}) {
      if (opt.fill) body.push(`<ellipse cx="${cx}" cy="${cy}" rx="${w / 2 - 2}" ry="${h / 2 - 2}" fill="${opt.fill}"/>`);
      body.push(paths(gen.ellipse(cx, cy, w, h, o({ ...opt, fill: undefined }))));
      return api;
    },
    line(x1, y1, x2, y2, opt = {}) { body.push(paths(gen.line(x1, y1, x2, y2, o(opt)))); return api; },
    arrow(x1, y1, x2, y2, opt = {}) {
      const bend = opt.bend ?? 0;
      const mx = (x1 + x2) / 2 - (y2 - y1) * bend, my = (y1 + y2) / 2 + (x2 - x1) * bend;
      body.push(paths(gen.curve([[x1, y1], [mx, my], [x2, y2]], o({ ...opt, roughness: 0.8 }))));
      const a = Math.atan2(y2 - my, x2 - mx), L = opt.head ?? 11;
      for (const s of [-1, 1]) {
        const ax = x2 - L * Math.cos(a + s * 0.45), ay = y2 - L * Math.sin(a + s * 0.45);
        body.push(paths(gen.line(x2, y2, ax, ay, o({ ...opt, roughness: 0.6 }))));
      }
      return api;
    },
    text(str, x, y, { size = 18, color = C.ink, weight = 400, anchor = 'start' } = {}) {
      const f = fonts[weight], s = size / f.unitsPerEm;
      let adv = 0; const uses = [];
      const chars = [...str];
      chars.forEach((ch, i) => {
        const g = f.charToGlyph(ch);
        const id = `g${weight}-${g.index}`;
        if (!glyphs.has(id)) glyphs.set(id, pathData(g.getPath(0, 0, f.unitsPerEm)));
        if (g.index === 0) throw new Error(`no glyph for ${JSON.stringify(ch)}`);
        if (ch !== ' ') uses.push(`<use href="#${id}" x="${Math.round(adv)}"/>`);
        adv += g.advanceWidth + (i < chars.length - 1 ? f.getKerningValue(g, f.charToGlyph(chars[i + 1])) : 0);
      });
      const w = adv * s, x0 = anchor === 'middle' ? x - w / 2 : anchor === 'end' ? x - w : x;
      body.push(`<g fill="${color}" transform="translate(${r1(x0)} ${r1(y)}) scale(${s.toFixed(5)})">${uses.join('')}</g>`);
      return api;
    },
    // keyframes: [[pct, 'css;css'], ...] applied to class `cls`
    anim(cls, frames, extra = '') {
      css.push(`@keyframes ${cls}{${frames.map(([p, d]) => `${p}%{${d}}`).join('')}}`);
      css.push(`.${cls}{animation:${cls} ${dur}s linear infinite;${extra}}`);
      return api;
    },
    css: s => (css.push(s), api),
    render() {
      const defs = [...glyphs].map(([id, d]) => `<path id="${id}" d="${d}"/>`).join('');
      return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 ${W} ${H}" width="${W}" height="${H}">` +
        `<style>${css.join('')}` +
        `@media (prefers-reduced-motion:reduce){*{animation-play-state:paused!important;animation-delay:-${freezeAt}s!important}}</style>` +
        `<defs>${defs}</defs><rect width="${W}" height="${H}" rx="14" fill="#ffffff"/>${body.join('')}</svg>`;
    },
  };
  return api;
}

// helper: opacity window (fade in at a, hold, fade out at b) in percent
export const show = (a, b, f = 2) => [[0, 'opacity:0'], [Math.max(a - f, 0), 'opacity:0'], [a, 'opacity:1'], [b, 'opacity:1'], [Math.min(b + f, 100), 'opacity:0'], [100, 'opacity:0']];
