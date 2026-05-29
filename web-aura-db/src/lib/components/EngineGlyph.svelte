<script>
  /** @type {{ engine?: string, size?: number }} */
  let { engine = 'postgres', size = 12 } = $props()
  const fillMap = {
    postgres: 'var(--engine-pg)',
    mysql:    'var(--engine-mysql)',
    sqlite:   'var(--engine-sqlite)',
    mssql:    'var(--engine-mssql)',
    oracle:   'var(--engine-oracle)',
  }
  const fill = $derived(fillMap[engine] || 'var(--text-mute)')
  const initial = $derived((engine[0] || '?').toUpperCase())
</script>

<svg width={size} height={size} viewBox="0 0 12 12" class="tree-row__glyph" aria-hidden="true">
  <rect x="0" y="0" width="12" height="12" rx="2" fill={fill} />
  <text x="6" y="9" text-anchor="middle" font-family="IBM Plex Sans, sans-serif" font-size="8" font-weight="600" fill="#fff">{initial}</text>
</svg>
