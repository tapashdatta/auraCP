// English-only string table with a t() hook in place so future i18n is a
// search-and-swap. Keys MUST be lowercase dotted; values can interpolate
// {name} placeholders via the vars parameter.

/** @type {Record<string,string>} */
const STRINGS = {
  // Brand + nav
  'brand': 'AuraDB',
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
  'tree.search.placeholder': 'Search…',
  'tree.engine.postgres': 'Postgres',
  'tree.engine.mysql': 'MySQL',
  'tree.engine.sqlite': 'SQLite',
  'tree.engine.mssql': 'SQL Server',
  'tree.engine.oracle': 'Oracle',
  'tree.readonly': 'read-only',

  // Welcome
  'welcome.title': 'Aura DB',
  'welcome.subtitle': 'Native database administration for auraCP.',
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

  // Routes — placeholders for later PRs
  'rows.title': 'Rows',
  'rows.placeholder.title': 'Grid coming in PR #12',
  'rows.placeholder.body': 'This route is reserved for the row viewer/editor.',
  'sql.title': 'SQL editor',
  'sql.placeholder.title': 'SQL editor coming in PR #13',
  'sql.placeholder.body': 'Run, explain, and stream queries — coming in the next PR.',

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
