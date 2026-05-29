// Command palette: open/close $state singleton + command builders.
//
// The palette is a transient overlay, not a route — `palette.open` flips
// to true on Cmd-K and the CommandPalette modal renders. Registering a
// route would be wrong (back-button would close it, deep-links to it are
// meaningless).
//
// `buildRegistry({connections, query, history, saved})` returns the flat
// list of commands for the current query string. We keep registry
// construction pure so it can be unit-tested without rendering Svelte.

import { navigate } from './router.svelte.js'
import { match } from './fuzzy.js'
import { replayInEditor, setEditorPendingBumper } from './replay.js'
import { toggleTheme } from './theme.svelte.js'

/**
 * @typedef {object} Command
 * @property {string} id
 * @property {'connection'|'table'|'history'|'saved'|'action'} kind
 * @property {string} section
 * @property {string} title
 * @property {string} [subtitle]
 * @property {string} [hint]
 * @property {string} [icon]
 * @property {number} score
 * @property {number} [recencyKey]
 * @property {number[]} [titlePositions]
 * @property {(opts?:{newTab?:boolean}) => void} run
 */

export const palette = $state({
  open: false,
  /** prefill query e.g. "/" for action filter */
  prefill: '',
})

// C1 reactive bus: a monotonically-increasing tick incremented on every
// replayInEditor() call. SqlEditor.svelte watches this via $effect to
// pick up palette → editor handoffs even when the screen is already
// mounted for the target connection (onMount only fires once). Cross-tab
// and cross-mount cases still go through onMount + consumePending.
export const editorPending = $state({ tick: 0 })

export function bumpEditorPendingTick() {
  editorPending.tick += 1
}

// Register the bumper with replay.js at module-load time so any caller
// of replayInEditor (palette, history screen, future callers) drives the
// same tick without a circular import chain.
setEditorPendingBumper(bumpEditorPendingTick)

export function openPalette(prefill = '') {
  palette.prefill = prefill
  palette.open = true
}

export function closePalette() {
  palette.open = false
  palette.prefill = ''
}

export function togglePalette() {
  if (palette.open) closePalette()
  else openPalette()
}

// ─── command builders ────────────────────────────────────────────────

/**
 * Static actions: pre-seeded entries that are always present.
 * @returns {Command[]}
 */
export function staticActions() {
  const actions = [
    {
      id: 'action.openHistory',
      kind: 'action',
      section: 'Actions',
      title: 'Open history',
      hint: '⌘G',
      run: () => navigate('/history'),
    },
    {
      id: 'action.newConnection',
      kind: 'action',
      section: 'Actions',
      title: 'New connection',
      run: () => navigate('/connections/new'),
    },
    {
      id: 'action.listConnections',
      kind: 'action',
      section: 'Actions',
      title: 'Browse connections',
      run: () => navigate('/connections'),
    },
    {
      id: 'action.openAudit',
      kind: 'action',
      section: 'Actions',
      title: 'Open audit log',
      run: () => navigate('/audit'),
    },
    {
      id: 'action.openAccount',
      kind: 'action',
      section: 'Actions',
      title: 'Open account',
      run: () => navigate('/account'),
    },
    {
      id: 'action.toggleTheme',
      kind: 'action',
      section: 'Actions',
      title: 'Toggle theme',
      run: () => toggleTheme(),
    },
    {
      id: 'action.copyUrl',
      kind: 'action',
      section: 'Actions',
      title: 'Copy current URL',
      run: () => {
        if (typeof navigator !== 'undefined' && navigator.clipboard) {
          try { navigator.clipboard.writeText(location.href) } catch { /* ignore */ }
        }
      },
    },
  ]
  return /** @type {Command[]} */ (actions.map((a) => ({ ...a, score: 0 })))
}

/**
 * Build connection commands from connections.list.
 * @param {Array<{id:string, name?:string, engine?:string, lastUsed?:string}>} list
 * @returns {Command[]}
 */
export function connectionCommands(list) {
  if (!Array.isArray(list)) return []
  return list.map((c) => ({
    id: 'conn:' + c.id,
    kind: 'connection',
    section: 'Connections',
    title: c.name || c.id,
    subtitle: c.engine || '',
    hint: c.engine || '',
    score: 0,
    recencyKey: c.lastUsed ? Date.parse(c.lastUsed) || 0 : 0,
    run: () => navigate('/connections/' + encodeURIComponent(c.id) + '/query'),
  }))
}

/**
 * Build history commands from a list of history entries.
 * Each entry's connectionId is used so Enter replays into the right editor.
 * @param {Array<{id:any, sql:string, class?:string, durationMs?:number, executed?:string, connectionId?:string, engine?:string}>} entries
 * @param {string} [activeConnFallback]  used when entry.connectionId is missing
 * @returns {Command[]}
 */
export function historyCommands(entries, activeConnFallback) {
  if (!Array.isArray(entries)) return []
  return entries.map((e) => {
    const sql = (e.sql || '').replace(/\s+/g, ' ').trim()
    const title = sql.length > 80 ? sql.slice(0, 80) + '…' : sql
    const connId = e.connectionId || activeConnFallback || ''
    const hintParts = []
    if (e.class) hintParts.push(e.class)
    if (typeof e.durationMs === 'number') hintParts.push(e.durationMs + 'ms')
    if (e.executed) hintParts.push(relTime(e.executed))
    return {
      id: 'hist:' + (e.id ?? sql.slice(0, 24)),
      kind: 'history',
      section: 'Recent history',
      title: title || '(empty statement)',
      subtitle: connId,
      hint: hintParts.join(' · '),
      score: 0,
      recencyKey: e.executed ? Date.parse(e.executed) || 0 : 0,
      run: ({ newTab } = {}) => {
        if (connId) replayInEditor(connId, e.sql || '', { newTab })
      },
    }
  })
}

/**
 * @param {Array<{id:string, name:string, statement:string}>} saved
 * @param {string} connId
 * @returns {Command[]}
 */
export function savedCommands(saved, connId) {
  if (!Array.isArray(saved)) return []
  return saved.map((s) => ({
    id: 'saved:' + s.id,
    kind: 'saved',
    section: 'Saved queries',
    title: s.name || '(unnamed)',
    subtitle: (s.statement || '').slice(0, 80),
    hint: 'saved',
    score: 0,
    run: ({ newTab } = {}) => {
      if (connId) replayInEditor(connId, s.statement || '', { newTab })
    },
  }))
}

// ─── registry assembly ───────────────────────────────────────────────

/**
 * Build the full ordered command list for the palette given current
 * inputs. Pure (no IO) — safe to unit-test.
 *
 * @param {{
 *   query: string,
 *   connections?: Array<any>,
 *   history?: Array<any>,
 *   saved?: Array<any>,
 *   activeConnId?: string,
 *   actionsOnly?: boolean,
 * }} input
 * @returns {Command[]}
 */
export function buildRegistry(input) {
  const q = (input.query || '').trim()
  const slash = q.startsWith('/')
  const effectiveQ = slash ? q.slice(1).trim() : q
  const actionsOnly = !!input.actionsOnly || slash

  /** @type {Command[]} */
  const all = []
  if (!actionsOnly) {
    all.push(...connectionCommands(input.connections || []))
  }
  all.push(...staticActions())
  if (!actionsOnly) {
    all.push(...historyCommands(input.history || [], input.activeConnId))
    all.push(...savedCommands(input.saved || [], input.activeConnId))
  }

  // Score + filter.
  const scored = []
  for (const cmd of all) {
    const r = match(effectiveQ, cmd.title)
    if (r === null) continue
    scored.push({ ...cmd, score: r.score, titlePositions: r.positions })
  }

  // Section priority for stable ordering when fuzzy ties.
  const SECTION_ORDER = {
    'Connections': 0,
    'Recent history': 1,
    'Saved queries': 2,
    'Actions': 3,
  }

  scored.sort((a, b) => {
    if (effectiveQ.length === 0) {
      // No filter: order by section, then recencyKey desc, then title.
      const ds = (SECTION_ORDER[a.section] ?? 9) - (SECTION_ORDER[b.section] ?? 9)
      if (ds !== 0) return ds
      const dr = (b.recencyKey || 0) - (a.recencyKey || 0)
      if (dr !== 0) return dr
      return a.title.localeCompare(b.title)
    }
    // With query: score-dominant, then recency, then alpha.
    const ds = b.score - a.score
    if (ds !== 0) return ds
    const dr = (b.recencyKey || 0) - (a.recencyKey || 0)
    if (dr !== 0) return dr
    return a.title.localeCompare(b.title)
  })

  return scored
}

/**
 * Group a flat command list into ordered sections for rendering.
 * @param {Command[]} cmds
 * @returns {Array<{label:string, items:Command[]}>}
 */
export function groupBySection(cmds) {
  /** @type {Map<string, Command[]>} */
  const buckets = new Map()
  for (const c of cmds) {
    const arr = buckets.get(c.section)
    if (arr) arr.push(c)
    else buckets.set(c.section, [c])
  }
  const ORDER = ['Connections', 'Recent history', 'Saved queries', 'Actions']
  const out = []
  for (const label of ORDER) {
    const items = buckets.get(label)
    if (items && items.length) out.push({ label, items })
  }
  // Any unknown sections at the end.
  for (const [label, items] of buckets) {
    if (!ORDER.includes(label)) out.push({ label, items })
  }
  return out
}

/**
 * Human-friendly relative time for hint columns.
 * @param {string|number|Date} when
 */
export function relTime(when) {
  const t = (typeof when === 'number') ? when : Date.parse(String(when))
  if (!t || Number.isNaN(t)) return ''
  const d = Date.now() - t
  if (d < 0) return 'just now'
  const sec = Math.floor(d / 1000)
  if (sec < 60) return sec + 's ago'
  const min = Math.floor(sec / 60)
  if (min < 60) return min + 'm ago'
  const hr = Math.floor(min / 60)
  if (hr < 24) return hr + 'h ago'
  const day = Math.floor(hr / 24)
  if (day < 30) return day + 'd ago'
  const mo = Math.floor(day / 30)
  if (mo < 12) return mo + 'mo ago'
  const yr = Math.floor(mo / 12)
  return yr + 'y ago'
}
