// English-only string table with a t() hook in place so future i18n is a
// search-and-swap. Keys MUST be lowercase dotted; values can interpolate
// {name} placeholders via the vars parameter.

/** @type {Record<string,string>} */
const STRINGS = {
  // Brand + nav
  // FIX (PR #11 dc-6): canonical brand is "Aura DB" (two words), matching
  // the docs and the welcome screen. The compact spelling "AuraDB" only
  // appears in CSS class prefixes / DB identifiers.
  'brand': 'Aura DB',
  'nav.connections': 'Connections',
  'nav.queries': 'Queries',
  'nav.history': 'History',
  'nav.audit': 'Audit',
  'nav.account': 'Account',
  'nav.theme': 'Theme: {value}',
  'nav.theme.dark': 'Dark',
  'nav.theme.light': 'Light',
  'nav.signout': 'Sign out',

  // Tree
  'tree.empty.title': 'No connections',
  'tree.empty.body': 'Add one to begin.',
  'tree.empty.action': 'Add connection',
  // FIX (PR #11 a11y-22): "Search connections" is more descriptive than
  // just "Search…" inside the LeftTree filter; screen readers read the
  // placeholder when no explicit aria-label is present.
  'tree.search.placeholder': 'Search connections',
  'tree.engine.postgres': 'Postgres',
  'tree.engine.mysql': 'MySQL',
  'tree.engine.sqlite': 'SQLite',
  'tree.engine.mssql': 'SQL Server',
  'tree.engine.oracle': 'Oracle',
  'tree.readonly': 'read-only',

  // Welcome
  'welcome.title': 'Aura DB',
  // FIX (PR #11 dc-10): drop marketing copy; subtitle now states what
  // the screen actually is.
  'welcome.subtitle': 'Database workstation for auraCP.',
  'welcome.recent': 'Recent connections',
  'welcome.cheatsheet.title': 'Keyboard',
  'welcome.cheatsheet.search': 'Search connections',
  'welcome.cheatsheet.closeTab': 'Close tab',
  'welcome.cta': 'New connection',

  // Connection list
  'conn.list.title': 'Connections',
  'conn.list.empty.title': 'No saved connections',
  'conn.list.empty.body': 'Create a connection to start browsing schemas.',
  'conn.list.action.new': 'New connection',
  'conn.list.col.name': 'Name',
  'conn.list.col.engine': 'Engine',
  'conn.list.col.host': 'Host',
  'conn.list.col.lastUsed': 'Last used',
  'conn.list.col.status': 'Status',

  // Connection form
  'conn.form.title.new': 'New connection',
  'conn.form.title.edit': 'Edit connection',
  'conn.form.name': 'Display name',
  'conn.form.engine': 'Engine',
  'conn.form.host': 'Host',
  'conn.form.port': 'Port',
  'conn.form.database': 'Database',
  'conn.form.username': 'Username',
  'conn.form.password': 'Password',
  'conn.form.readonly': 'Read-only',
  'conn.form.save': 'Save',
  'conn.form.cancel': 'Cancel',
  'conn.form.test': 'Test connection',

  // Connection detail
  'conn.detail.test': 'Test',
  'conn.detail.reveal': 'Reveal password',
  'conn.detail.audit': 'View audit log',
  'conn.detail.delete': 'Delete connection',

  // Rows / grid
  'rows.title': 'Rows',
  'rows.placeholder.title': 'Grid coming in PR #12',
  'rows.placeholder.body': 'This route is reserved for the row viewer/editor.',
  'rows.toolbar.refresh': 'Refresh',
  'rows.toolbar.insert': '+ Row',
  'rows.toolbar.delete': 'Delete',
  'rows.toolbar.density': 'Density',
  'rows.toolbar.clearFilters': 'Clear filters',
  'rows.readonly.pill': 'READ-ONLY · no PK',
  'rows.readonly.toast': 'Read-only: table has no primary key',
  'rows.delete.confirm': 'Delete {n} row(s)? This cannot be undone.',
  'rows.editing.pkBlocked': 'PK columns are not editable',
  'rows.editing.binaryBlocked': 'Binary cells are not editable inline',
  'rows.editing.invalid': 'Value is not valid',
  'rows.footer.page': 'Page {n}',
  'rows.footer.of': 'of {n}',
  'rows.footer.rows': '{n} rows',
  'rows.footer.onPage': '{n} on page',
  'sql.title': 'SQL editor',
  'sql.placeholder.title': 'SQL editor coming in PR #13',
  'sql.placeholder.body': 'Run, explain, and stream queries — coming in the next PR.',
  'sql.exec': 'Execute',
  'sql.execAll': 'Execute all',
  'sql.cancel': 'Cancel',
  'sql.format': 'Format',
  'sql.save': 'Save query',
  'sql.classify.read': 'READ',
  'sql.classify.write': 'WRITE',
  'sql.classify.ddl': 'DDL',
  'sql.classify.dangerous': 'DANGEROUS',
  'sql.classify.forbidden': 'FORBIDDEN',
  'sql.forbidden.banner': 'This statement is forbidden and cannot run.',
  'sql.results.empty': 'No results yet. Press ⌘↵ to execute the statement under the cursor.',
  'sql.history.title': 'History',
  'sql.saved.title': 'Saved',
  'sql.saved.persistence': 'Saved queries are not yet persisted across server restarts.',

  // Schema browser
  'schema.title': 'Schema {schema}',
  'schema.objects.tables': 'Tables',
  'schema.objects.views': 'Views',
  'schema.objects.functions': 'Functions',

  // Table detail
  'table.title': '{table}',
  'table.tab.columns': 'Columns',
  'table.tab.indices': 'Indices',
  'table.tab.ddl': 'DDL',

  // History
  'history.title': 'Query history',
  'history.search.placeholder': 'Search query text…',
  'history.empty.title': 'No history yet',
  'history.empty.body': 'Queries you run will appear here.',
  'history.filters.starredOnly': 'Starred only',
  'history.col.class': 'Class',
  'history.col.sql': 'SQL',
  'history.col.conn': 'Connection',
  'history.col.dur': 'Duration',
  'history.col.when': 'When',
  'history.context.replay': 'Replay in editor',
  'history.context.copy': 'Copy SQL',
  'history.context.star': 'Star',
  'history.context.unstar': 'Unstar',
  'history.context.delete': 'Delete',

  // Command palette (PR #15)
  'palette.placeholder': 'Search connections, history, commands…',
  'palette.empty.title': 'No matches',
  'palette.empty.body': 'Try a connection name, table, SQL keyword, or “/” for actions.',
  'palette.section.connections': 'Connections',
  'palette.section.history': 'Recent history',
  'palette.section.saved': 'Saved queries',
  'palette.section.actions': 'Actions',
  'palette.hint.navigate': '↑↓ navigate',
  'palette.hint.select': '↵ select',
  'palette.hint.newtab': '⌘↵ open in new tab',
  'palette.hint.close': 'esc close',

  // Audit
  'audit.title': 'Audit log',
  'audit.empty.title': 'No audit events',
  'audit.empty.body': 'Connection mutations will appear here.',

  // Account
  'account.title': 'Account',
  'account.session': 'Signed in via panel session.',
  'account.signout': 'Sign out',
  'account.theme': 'Theme',
  'account.stepup': 'Step-up authentication',
  'account.stepup.enrolled': 'Enrolled.',
  'account.stepup.notEnrolled': 'Not enrolled.',

  // Auth gate
  'auth.title': 'Session expired',
  'auth.body': 'Your panel session is no longer valid. Sign in again to continue.',
  'auth.action': 'Sign in',

  // Status bar
  'status.connections.count': '{n} connections',
  'status.ready': 'ready',
  'status.loading': 'loading…',
  'status.ws.idle': 'sql stream: idle',
  'status.ws.opening': 'sql stream: opening',
  'status.ws.open': 'sql stream: open',
  'status.ws.closed': 'sql stream: closed',
  'status.ws.error': 'sql stream: error',

  // Per-route document titles (PR #11 a11y-07). All titles end with the
  // brand for orientation when multiple tabs are open.
  'doc.title.base': 'Aura DB',
  'doc.title.welcome': 'Aura DB',
  'doc.title.connections': 'Connections · Aura DB',
  'doc.title.conn.new': 'New connection · Aura DB',
  'doc.title.conn.detail': 'Connection · Aura DB',
  'doc.title.schema': 'Schema · Aura DB',
  'doc.title.table': 'Table · Aura DB',
  'doc.title.rows': 'Rows · Aura DB',
  'doc.title.query': 'SQL editor · Aura DB',
  'doc.title.explain': 'EXPLAIN · Aura DB',
  'doc.title.history': 'History · Aura DB',
  'doc.title.audit': 'Audit · Aura DB',
  'doc.title.account': 'Account · Aura DB',
  'doc.title.auth.gate': 'Sign in · Aura DB',

  // Skip link / landmark labels (PR #11 a11y-06, PR #14.5 A11Y-11)
  'a11y.skip.main': 'Skip to main content',
  'a11y.landmark.main': 'Main',

  // Generic
  'action.retry': 'Retry',
  'action.confirm': 'Confirm',
  'action.cancel': 'Cancel',
  'action.close': 'Close',
  'action.delete': 'Delete',
  'common.loading': 'Loading',
}

/**
 * @param {string} key
 * @param {Record<string,string|number>} [vars]
 */
export function t(key, vars) {
  const s = STRINGS[key] ?? key
  if (!vars) return s
  return s.replace(/\{(\w+)\}/g, (_, k) => String(vars[k] ?? ''))
}
