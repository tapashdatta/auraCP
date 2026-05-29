// Inline SVG path strings. Used by EngineGlyph + small UI chevrons. Keeping
// these as raw `d` attrs avoids an icon-font dependency (forbidden by budget).

/** 12x12 viewBox icons unless noted. */
export const icons = {
  chevron:   'M4 2.5l4 3.5-4 3.5V2.5z',           // right-pointing; rotate 90° for down
  search:    'M5.25 8.5a3.25 3.25 0 100-6.5 3.25 3.25 0 000 6.5zm2.6-.7l2.4 2.4',
  x:         'M3 3l6 6M9 3l-6 6',
  plus:      'M6 2v8M2 6h8',
  refresh:   'M2 6a4 4 0 016.83-2.83L10 4M10 2v2H8M10 6a4 4 0 01-6.83 2.83L2 8M2 10V8h2',
  lock:      'M3 6V4.5a3 3 0 016 0V6M2.5 6h7v5h-7z',
}

/**
 * @param {keyof typeof icons} name
 * @returns {string}
 */
export function pathFor(name) {
  return icons[name]
}
