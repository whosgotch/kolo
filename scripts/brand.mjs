// Builds the icons the page serves, from the geometry in docs/brand/ring.ts.
//
// The mark is generated, never stored: see docs/brand/kolo-brand.md. At the
// sizes a browser puts it — a tab, a home screen, a corner of the page — it is
// under the twelve rows the ASCII build needs, so all of it is the vector build.
//
// SVG is what the geometry gives; the PNGs are that rasterised, because a
// favicon is a picture rather than a document. Rasterising wants a browser:
// pass one in KOLO_CHROME, or let it look in the usual places.
//
// Run: make brand
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, writeFileSync, copyFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { markSvg } from '../docs/brand/ring.ts';

const VOID = '#101314', CHALK = '#E7E9E4';
const assets = fileURLToPath(new URL('../internal/ui/assets/', import.meta.url));
const sizes = { 'icon-32': 32, 'icon-180': 180, icon: 256 };

const browsers = [
  process.env.KOLO_CHROME,
  `${process.env.HOME}/Library/Caches/ms-playwright/chromium-1228/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing`,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
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
  fileURLToPath(new URL('../docs/brand/kolo-tokens.css', import.meta.url)),
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
