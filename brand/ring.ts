/**
 * Kolo mark — single source of truth.
 *
 * The logo is generated, not stored. Both the ASCII build and the vector build
 * come from the same geometry: a circle with a smaller circle subtracted,
 * offset up and to the right, so the ring tapers.
 *
 * Edited by hand. The icons are what is generated, by node brand/build.mjs.
 */

export const RAMP = " .:-=+*#%@";
/** Ramp steps that take the brass accent. Nothing else is ever brass. */
export const HOT = new Set(["%", "@"]);

const R_IN = 0.71;          // inner radius, outer is 1
const OFFSET = 0.1824;      // inner centre distance from origin (0.129 * sqrt2)
const REST = -Math.PI / 4;  // thin end at top right
/** JetBrains Mono advance / line-height. Cells are not square. */
export const CELL_ASPECT = 0.60205 / 1.17;

function isRing(x: number, y: number, cx: number, cy: number): boolean {
  if (x * x + y * y > 1) return false;
  const dx = x - cx, dy = y - cy;
  return dx * dx + dy * dy > R_IN * R_IN;
}

/** Ring thickness along the ray through (x, y), in units of outer radius. */
function thickness(x: number, y: number, cx: number, cy: number): number {
  const d = Math.hypot(x, y);
  if (d < 1e-6) return 0;
  const b = (x / d) * cx + (y / d) * cy;
  const disc = b * b - (cx * cx + cy * cy) + R_IN * R_IN;
  if (disc <= 0) return 1;
  return Math.max(0, 1 - (b + Math.sqrt(disc)));
}

export interface RingOptions {
  /** Rotation of the taper. Leave at rest unless work is actually running. */
  phase?: number;
  /** Padding around the ring, 1 = ring touches the edge. */
  span?: number;
  /** Supersamples per axis per cell. 3 is fine live, 4 for exported assets. */
  samples?: number;
}

/**
 * Render the mark as rows of characters.
 * Minimum 12 rows — below that use the vector mark instead.
 */
export function asciiRing(rows: number, opts: RingOptions = {}): string[] {
  const { phase = REST, span = 1.1, samples: ss = 3 } = opts;
  const cols = Math.round(rows / CELL_ASPECT);
  const cx = OFFSET * Math.cos(phase);
  const cy = OFFSET * Math.sin(phase);
  const maxT = 1 + OFFSET - R_IN;
  const out: string[] = [];

  for (let r = 0; r < rows; r++) {
    let line = "";
    for (let c = 0; c < cols; c++) {
      let cover = 0, th = 0;
      for (let sy = 0; sy < ss; sy++) {
        for (let sx = 0; sx < ss; sx++) {
          const x = (((c + (sx + 0.5) / ss) / cols) * 2 - 1) * span;
          const y = (((r + (sy + 0.5) / ss) / rows) * 2 - 1) * span;
          if (isRing(x, y, cx, cy)) { cover++; th += thickness(x, y, cx, cy); }
        }
      }
      if (!cover) { line += " "; continue; }
      const weight =
        (cover / (ss * ss)) * (0.42 + 0.58 * Math.pow(th / cover / maxT, 0.85));
      line += RAMP[Math.min(9, Math.max(1, Math.round(weight * 9)))];
    }
    out.push(line);
  }
  return out;
}

/** Split a rendered line into runs so the accent rule can be applied. */
export function accentRuns(line: string): { text: string; hot: boolean }[] {
  const runs: { text: string; hot: boolean }[] = [];
  for (const ch of line) {
    const hot = HOT.has(ch);
    const last = runs[runs.length - 1];
    if (last && last.hot === hot) last.text += ch;
    else runs.push({ text: ch, hot });
  }
  return runs;
}

/** Vector build, for anything under 12 rows. */
export function markSvg(size = 256, fill = "currentColor"): string {
  const r = size * 0.46, ri = size * 0.3266, c = size / 2;
  const ix = c + size * 0.0593, iy = c - size * 0.0593;
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" ` +
    `viewBox="0 0 ${size} ${size}" role="img" aria-label="Kolo">` +
    `<path fill-rule="evenodd" fill="${fill}" ` +
    `d="M${c - r},${c} a${r},${r} 0 1,0 ${2 * r},0 a${r},${r} 0 1,0 ${-2 * r},0 Z ` +
    `M${ix - ri},${iy} a${ri},${ri} 0 1,0 ${2 * ri},0 a${ri},${ri} 0 1,0 ${-2 * ri},0 Z"/></svg>`
  );
}
