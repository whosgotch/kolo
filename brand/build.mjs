// Builds the icons the page serves, from the geometry in ring.ts beside it.
//
// The mark is generated, never stored (ring.ts is the geometry). At the
// sizes a browser puts it (a tab, a home screen, a corner of the page), it is
// under the twelve rows the ASCII build needs, so all of it is the vector build.
//
// SVG is what the geometry gives; the PNGs are that rasterised, because a
// favicon is a picture rather than a document. Rasterising wants a browser:
// pass one in KOLO_CHROME, or let it look in the usual places.
//
// Run: node brand/build.mjs
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, writeFileSync, copyFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { markSvg, asciiRing, accentRuns } from './ring.ts';

const VOID = '#101314', CHALK = '#E7E9E4', BRASS = '#D9A441', BRASS_INK = '#9C6C15';
const assets = fileURLToPath(new URL('../internal/hub/ui/assets/', import.meta.url));
const sizes = { 'icon-32': 32, 'icon-180': 180, icon: 256 };

const browsers = [
  process.env.KOLO_CHROME,
  `${process.env.HOME}/Library/Caches/ms-playwright/chromium-1228/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing`,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
  '/usr/bin/google-chrome',
  '/usr/bin/google-chrome-stable',
];
const chrome = browsers.find((path) => path && existsSync(path));
if (!chrome) {
  console.error('brand: no chrome to rasterise with; set KOLO_CHROME');
  process.exit(1);
}

mkdirSync(assets, { recursive: true });
// The pages read the tokens as a stylesheet, and the spec's copy is the one
// they read: served from assets because a page cannot embed a file outside the
// package it is compiled into.
copyFileSync(
  fileURLToPath(new URL('./tokens.css', import.meta.url)),
  join(assets, 'tokens.css'),
);
console.log('tokens.css');
const work = mkdtempSync(join(tmpdir(), 'kolo-brand-'));

for (const [name, size] of Object.entries(sizes)) {
  // The rounded square the mark sits on. Void ground: the mark is chalk, and
  // brass is never brand fill.
  const r = size * 0.2237;
  const svg = markSvg(size, CHALK).replace(
    /(<svg[^>]*>)/,
    `$1<rect width="${size}" height="${size}" rx="${r}" ry="${r}" fill="${VOID}"/>`,
  );
  const from = join(work, `${name}.svg`);
  writeFileSync(from, svg);
  execFileSync(chrome, [
    '--headless', '--disable-gpu', '--hide-scrollbars',
    `--window-size=${size},${size}`,
    `--screenshot=${join(work, name + '.png')}`,
    `file://${from}`,
  ], { stdio: 'ignore' });
  copyFileSync(join(work, `${name}.png`), join(assets, `${name}.png`));
  console.log(`${name}.png ${size}×${size}`);
}

// The README's logo: the ASCII mark as a picture, because a README cannot ask
// for JetBrains Mono, and in whatever face the reader's browser picks instead
// the ring stops being a ring. Drawn from ring.ts rather than kept as art
// somebody has to redraw when the ring moves. The mark only: the name is text
// in the README, not a picture of text.
//
// On no background, so it sits on whatever GitHub is painting. That costs a
// second copy: chalk glyphs vanish on a light page, so the light one is drawn
// in ink, and brass gives way to the brassInk the tokens keep for exactly this.
const FONT = 16;
const ADVANCE = 0.60205 * FONT;   // JetBrains Mono glyph width at this size
const LINE = 1.17 * FONT;         // and its line height; cells are not square
const PAD = 40;

const rows = asciiRing(18);
const cols = Math.max(...rows.map((r) => r.length));
const sheetW = Math.round(cols * ADVANCE + PAD * 2);
const sheetH = Math.round(rows.length * LINE + PAD * 2);

const art = rows
  .map((line) => accentRuns(line)
    .map((run) => `<span${run.hot ? ' class="hot"' : ''}>${run.text.replace(/ /g, '&nbsp;')}</span>`)
    .join(''))
  .join('<br>');

for (const [name, ink, hot] of [['logo-dark', CHALK, BRASS], ['logo-light', VOID, BRASS_INK]]) {
  const page = `<!doctype html><meta charset="utf-8"><style>
    @font-face { font-family: 'JBM'; font-weight: 700;
                 src: url('file://${join(assets, 'fonts/jetbrains-mono-700.woff2')}') format('woff2'); }
    html, body { margin: 0; background: transparent; }
    .sheet { width: ${sheetW}px; height: ${sheetH}px; box-sizing: border-box;
             padding: ${PAD}px 0; text-align: center; }
    .art  { font: 700 ${FONT}px/${LINE}px 'JBM', monospace; color: ${ink}; white-space: pre; }
    .hot  { color: ${hot}; }
  </style><div class="sheet"><div class="art">${art}</div></div>`;

  const logoHtml = join(work, `${name}.html`);
  writeFileSync(logoHtml, page);
  execFileSync(chrome, [
    '--headless', '--disable-gpu', '--hide-scrollbars', '--force-device-scale-factor=2',
    '--default-background-color=00000000',
    `--window-size=${sheetW},${sheetH}`,
    `--screenshot=${fileURLToPath(new URL(`./${name}.png`, import.meta.url))}`,
    `file://${logoHtml}`,
  ], { stdio: 'ignore' });
  console.log(`${name}.png ${sheetW}×${sheetH} (transparent)`);
}
