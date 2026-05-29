<script>
  /** @type {{ engine?: string, size?: number }} */
  let { engine = 'postgres', size = 12 } = $props()
  const fillMap = {
    postgres: 'var(--engine-pg)',
    mysql:    'var(--engine-mysql)',
    sqlite:   'var(--engine-sqlite)',
    mssql:    'var(--engine-mssql)',
    oracle:   'var(--engine-oracle)',
    mariadb:  'var(--engine-mysql)',
  }
  // FIX (PR #11 dc-2): mysql/mssql/maria all begin with "M" and were
  // indistinguishable at 12px. Map engines to a stable two-character
  // glyph instead of falling back to the first letter — Postgres "Pg",
  // MySQL "My", MariaDB "Ma", SQL Server "Ms", SQLite "Sl", Oracle "Or".
  const glyphMap = {
    postgres: 'Pg',
    mysql:    'My',
    sqlite:   'Sl',
    mssql:    'Ms',
    oracle:   'Or',
    mariadb:  'Ma',
  }
  const fill = $derived(fillMap[engine] || 'var(--text-mute)')
  const glyph = $derived(glyphMap[engine] || ((engine[0] || '?').toUpperCase()))
</script>

<!-- FIX (PR #11 dc-12): use a CSS class instead of a raw #fff fill so the
     glyph foreground tracks the theme token (--engine-glyph-fg). -->
<svg width={size} height={size} viewBox="0 0 14 12" class="engine-glyph tree-row__glyph" aria-hidden="true">
  <rect x="0" y="0" width="14" height="12" rx="2" fill={fill} />
  <text class="engine-glyph__text" x="7" y="8.5" text-anchor="middle" font-family="IBM Plex Sans, sans-serif" font-size="7" font-weight="600">{glyph}</text>
</svg>

<style>
  .engine-glyph__text { fill: var(--engine-glyph-fg, #ffffff); }
  :global(:root[data-theme="light"]) .engine-glyph__text { fill: #ffffff; }
</style>
