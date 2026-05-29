// Hand-rolled hash router (~80 LOC). One $state singleton; pattern matcher with
// :param segments. Hash-based so the SPA needs no server-side route fallback
// beyond serving index.html for /dbadmin/* (handled by webui.go).

/** @type {Array<{name:string, raw:string, re:RegExp, keys:string[]}>} */
const routeTable = []

/**
 * Register a route pattern. `:param` segments are extracted into params.
 * Order matters — first match wins, so register most-specific patterns first.
 *
 * @param {string} name
 * @param {string} raw  e.g. "/connections/:id/schemas/:schema"
 */
export function route(name, raw) {
  /** @type {string[]} */
  const keys = []
  const reStr = raw.replace(/:[a-zA-Z_][\w]*/g, (m) => {
    keys.push(m.slice(1))
    return '([^/]+)'
  })
  routeTable.push({ name, raw, re: new RegExp('^' + reStr + '/?$'), keys })
}

/**
 * @param {string} path
 * @returns {{name:string, params:Record<string,string>} | null}
 */
function match(path) {
  for (const r of routeTable) {
    const m = r.re.exec(path)
    if (m) {
      /** @type {Record<string,string>} */
      const params = {}
      r.keys.forEach((k, i) => { params[k] = decodeURIComponent(m[i + 1]) })
      return { name: r.name, params }
    }
  }
  return null
}

function parseHash() {
  const raw = (typeof location !== 'undefined' && location.hash) ? location.hash.slice(1) : ''
  const [pathPart, queryPart = ''] = raw.split('?')
  const path = pathPart || '/'
  /** @type {Record<string,string>} */
  const query = {}
  if (queryPart) {
    for (const [k, v] of new URLSearchParams(queryPart)) query[k] = v
  }
  const m = match(path)
  return { name: m?.name || 'unknown', path, params: m?.params || {}, query }
}

// Default routes registered at module load. Order: most specific first.
route('rows',        '/connections/:id/schemas/:schema/tables/:table/rows')
route('table',       '/connections/:id/schemas/:schema/tables/:table')
route('schema',      '/connections/:id/schemas/:schema')
route('query',       '/connections/:id/query')
route('conn.detail', '/connections/:id')
route('conn.new',    '/connections/new')
route('conn.list',   '/connections')
route('history',     '/history')
route('audit',       '/audit')
route('account',     '/account')
route('auth.gate',   '/401')
route('welcome',     '/')

/** Reactive route state. Use as `routeState.name`, `routeState.params.id`, etc. */
export const routeState = $state(parseHash())

/**
 * @param {string} path
 * @param {Record<string,string>} [query]
 */
export function navigate(path, query) {
  let hash = '#' + path
  if (query && Object.keys(query).length) {
    hash += '?' + new URLSearchParams(query).toString()
  }
  if (typeof location !== 'undefined') location.hash = hash
}

if (typeof window !== 'undefined') {
  window.addEventListener('hashchange', () => {
    const next = parseHash()
    routeState.name = next.name
    routeState.path = next.path
    routeState.params = next.params
    routeState.query = next.query
  })
}
