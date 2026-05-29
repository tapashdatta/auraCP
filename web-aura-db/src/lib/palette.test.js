import { describe, it, expect, beforeEach, vi } from 'vitest'

vi.mock('./router.svelte.js', () => ({
  navigate: vi.fn(),
}))
vi.mock('./theme.svelte.js', () => ({
  toggleTheme: vi.fn(),
  theme: { value: 'dark' },
  setTheme: vi.fn(),
}))

import {
  buildRegistry,
  groupBySection,
  connectionCommands,
  historyCommands,
  savedCommands,
  staticActions,
  relTime,
  openPalette,
  closePalette,
  togglePalette,
  palette,
} from './palette.svelte.js'
import { navigate } from './router.svelte.js'

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
  palette.open = false
  palette.prefill = ''
})

describe('palette open/close', () => {
  it('opens and closes', () => {
    expect(palette.open).toBe(false)
    openPalette()
    expect(palette.open).toBe(true)
    closePalette()
    expect(palette.open).toBe(false)
  })

  it('toggle flips state', () => {
    togglePalette()
    expect(palette.open).toBe(true)
    togglePalette()
    expect(palette.open).toBe(false)
  })

  it('openPalette accepts prefill (for "/" → action filter)', () => {
    openPalette('/')
    expect(palette.open).toBe(true)
    expect(palette.prefill).toBe('/')
  })
})

describe('connectionCommands', () => {
  it('maps each connection to a Command and run navigates to /query', () => {
    const cmds = connectionCommands([
      { id: 'a', name: 'Alpha', engine: 'postgres' },
      { id: 'b', name: 'Beta',  engine: 'mariadb' },
    ])
    expect(cmds).toHaveLength(2)
    expect(cmds[0].section).toBe('Connections')
    expect(cmds[0].title).toBe('Alpha')
    cmds[0].run()
    expect(navigate).toHaveBeenCalledWith('/connections/a/query')
  })

  it('url-encodes the connection id in the navigate path', () => {
    const cmds = connectionCommands([{ id: 'has space/slash', name: 'X' }])
    cmds[0].run()
    expect(navigate).toHaveBeenCalledWith('/connections/has%20space%2Fslash/query')
  })

  it('falls back to id when name is missing', () => {
    const cmds = connectionCommands([{ id: 'fallback-id' }])
    expect(cmds[0].title).toBe('fallback-id')
  })
})

describe('historyCommands', () => {
  it('shapes each entry with class+duration+relTime in the hint', () => {
    const cmds = historyCommands([
      { id: 1, sql: 'SELECT 1', class: 'read', durationMs: 12, executed: new Date(Date.now() - 30_000).toISOString(), connectionId: 'a' },
    ])
    expect(cmds).toHaveLength(1)
    expect(cmds[0].kind).toBe('history')
    expect(cmds[0].hint).toContain('read')
    expect(cmds[0].hint).toContain('12ms')
  })

  it('replays via sessionStorage on run() and navigates to the entry conn', () => {
    const cmds = historyCommands([
      { id: 1, sql: 'SELECT 1', connectionId: 'conn-a' },
    ])
    cmds[0].run()
    expect(navigate).toHaveBeenCalledWith('/connections/conn-a/query')
    const slot = JSON.parse(sessionStorage.getItem('editor:pending'))
    expect(slot.statement).toBe('SELECT 1')
    expect(slot.connId).toBe('conn-a')
  })

  it('uses activeConnFallback when entry has no connectionId', () => {
    const cmds = historyCommands([{ id: 1, sql: 'SELECT 1' }], 'fallback-conn')
    cmds[0].run()
    expect(navigate).toHaveBeenCalledWith('/connections/fallback-conn/query')
  })

  it('truncates long SQL titles with an ellipsis', () => {
    const long = 'SELECT ' + 'x'.repeat(200)
    const cmds = historyCommands([{ id: 1, sql: long, connectionId: 'a' }])
    expect(cmds[0].title.length).toBeLessThanOrEqual(81)
    expect(cmds[0].title.endsWith('…')).toBe(true)
  })
})

describe('savedCommands', () => {
  it('replays the saved statement into the active conn on run', () => {
    const cmds = savedCommands([{ id: 's1', name: 'Top users', statement: 'SELECT * FROM users' }], 'conn-a')
    cmds[0].run()
    expect(navigate).toHaveBeenCalledWith('/connections/conn-a/query')
    const slot = JSON.parse(sessionStorage.getItem('editor:pending'))
    expect(slot.statement).toBe('SELECT * FROM users')
  })
})

describe('staticActions', () => {
  it('always contains the 7+ pre-seeded entries', () => {
    const acts = staticActions()
    expect(acts.length).toBeGreaterThanOrEqual(7)
    const ids = acts.map((a) => a.id)
    expect(ids).toContain('action.openHistory')
    expect(ids).toContain('action.toggleTheme')
  })
})

describe('buildRegistry', () => {
  const conns = [
    { id: 'a', name: 'Alpha postgres', engine: 'postgres' },
    { id: 'b', name: 'Beta mysql', engine: 'mariadb' },
  ]
  const hist = [
    { id: 1, sql: 'SELECT * FROM users WHERE active = 1', class: 'read', durationMs: 12, executed: new Date(Date.now() - 60_000).toISOString(), connectionId: 'a' },
    { id: 2, sql: 'UPDATE orders SET shipped = 1', class: 'write', durationMs: 30, executed: new Date(Date.now() - 5_000).toISOString(), connectionId: 'a' },
  ]
  const saved = [{ id: 's1', name: 'Top users', statement: 'SELECT * FROM users LIMIT 10' }]

  it('returns connections + actions + history + saved when query is empty', () => {
    const out = buildRegistry({ query: '', connections: conns, history: hist, saved, activeConnId: 'a' })
    const sections = new Set(out.map((c) => c.section))
    expect(sections.has('Connections')).toBe(true)
    expect(sections.has('Actions')).toBe(true)
    expect(sections.has('Recent history')).toBe(true)
    expect(sections.has('Saved queries')).toBe(true)
  })

  it('with empty query, sorts Connections first, then Recent history, then Saved, then Actions', () => {
    const out = buildRegistry({ query: '', connections: conns, history: hist, saved, activeConnId: 'a' })
    const sections = out.map((c) => c.section)
    const idxConn = sections.indexOf('Connections')
    const idxHist = sections.indexOf('Recent history')
    const idxSaved = sections.indexOf('Saved queries')
    const idxActions = sections.indexOf('Actions')
    expect(idxConn).toBeGreaterThanOrEqual(0)
    expect(idxConn).toBeLessThan(idxHist)
    expect(idxHist).toBeLessThan(idxSaved)
    expect(idxSaved).toBeLessThan(idxActions)
  })

  it('filters by fuzzy match on title when query is set', () => {
    const out = buildRegistry({ query: 'beta', connections: conns, history: hist, saved })
    // "Beta mysql" matches; "Alpha postgres" does not.
    const titles = out.map((c) => c.title)
    expect(titles).toContain('Beta mysql')
    expect(titles).not.toContain('Alpha postgres')
  })

  it('action-only mode (slash prefix) hides connections/history/saved', () => {
    const out = buildRegistry({ query: '/', connections: conns, history: hist, saved })
    for (const c of out) expect(c.section).toBe('Actions')
  })

  it('attaches titlePositions for highlighting', () => {
    const out = buildRegistry({ query: 'beta', connections: conns, history: [], saved: [] })
    const beta = out.find((c) => c.title === 'Beta mysql')
    expect(beta).toBeDefined()
    expect(Array.isArray(beta.titlePositions)).toBe(true)
    expect(beta.titlePositions.length).toBeGreaterThan(0)
  })
})

describe('C2: score-first ordering with active query', () => {
  it('ranks an exact-name saved query above a tangentially-matching connection', () => {
    // Connection title contains "mytable" only as a fuzzy subsequence
    // (e.g. "my-cluster: table-of-contents"). The saved query is named
    // exactly "mytable" — it should beat the connection on raw score.
    const out = buildRegistry({
      query: 'mytable',
      connections: [{ id: 'c1', name: 'my-cluster: table-of-contents', engine: 'postgres' }],
      history: [],
      saved: [{ id: 's1', name: 'mytable', statement: 'SELECT * FROM mytable' }],
      activeConnId: 'c1',
    })
    expect(out.length).toBeGreaterThan(0)
    // First flat result should be the saved query, NOT the connection.
    expect(out[0].kind).toBe('saved')
    expect(out[0].title).toBe('mytable')
  })

  it('with empty query, falls back to grouped browse ordering (Connections first)', () => {
    const out = buildRegistry({
      query: '',
      connections: [{ id: 'c1', name: 'my-cluster: table-of-contents', engine: 'postgres' }],
      history: [],
      saved: [{ id: 's1', name: 'mytable', statement: 'SELECT * FROM mytable' }],
      activeConnId: 'c1',
    })
    expect(out[0].section).toBe('Connections')
  })
})

describe('groupBySection', () => {
  it('groups in priority order, omitting empty sections', () => {
    const cmds = [
      { id: '1', section: 'Actions', title: 'A', kind: 'action', score: 0, run: () => {} },
      { id: '2', section: 'Connections', title: 'C', kind: 'connection', score: 0, run: () => {} },
    ]
    const groups = groupBySection(cmds)
    expect(groups[0].label).toBe('Connections')
    expect(groups[1].label).toBe('Actions')
  })
})

describe('relTime', () => {
  it('formats seconds / minutes / hours / days', () => {
    expect(relTime(Date.now() - 5_000)).toMatch(/s ago$/)
    expect(relTime(Date.now() - 90_000)).toMatch(/m ago$/)
    expect(relTime(Date.now() - 3 * 60 * 60 * 1000)).toMatch(/h ago$/)
    expect(relTime(Date.now() - 3 * 24 * 60 * 60 * 1000)).toMatch(/d ago$/)
  })

  it('returns empty for invalid input', () => {
    expect(relTime('')).toBe('')
    expect(relTime('not a date')).toBe('')
  })
})
