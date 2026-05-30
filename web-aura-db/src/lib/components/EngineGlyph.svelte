<script>
  /** @type {{ engine?: string, size?: number }} */
  let { engine = 'postgres', size = 16 } = $props()

  // Prototype style: one clean database-cylinder line icon, color-coded by
  // engine (the topbar badge / connection name carries the exact engine).
  // Replaces the old lettered "Pg"/"My" box.
  const colorMap = {
    postgres: 'var(--engine-pg)',
    mysql:    'var(--engine-mysql)',
    sqlite:   'var(--engine-sqlite)',
    mssql:    'var(--engine-mssql)',
    oracle:   'var(--engine-oracle)',
    mariadb:  'var(--engine-mysql)',
  }
  const stroke = $derived(colorMap[engine] || 'var(--text-dim)')
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke={stroke}
  stroke-width="1.7"
  stroke-linecap="round"
  stroke-linejoin="round"
  class="tree-row__glyph"
  aria-hidden="true"
>
  <ellipse cx="12" cy="5" rx="7" ry="2.5" />
  <path d="M5 5v6c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5V5" />
  <path d="M5 11v6c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5v-6" />
</svg>
