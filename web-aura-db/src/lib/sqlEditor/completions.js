// CodeMirror 6 completion provider backed by schemaCache. Yields
// dialect-aware keyword seeds + schema / table / column suggestions
// when the editor context (token left of cursor) implies them.
//
// Ranking:
//   - keywords get boost 0
//   - schemas get boost 5 when caret is at start of identifier
//   - tables get boost 10 when caret follows "FROM"/"JOIN"/"UPDATE"/"INTO"
//   - columns get boost 20 when caret follows "schema.table." or after "SELECT"

import { getSchemas, peekObjects, peekTable, loadObjects, loadTable } from './schemaCache.svelte.js'

const ANSI_KEYWORDS = [
  'SELECT', 'FROM', 'WHERE', 'JOIN', 'LEFT JOIN', 'RIGHT JOIN', 'INNER JOIN',
  'OUTER JOIN', 'GROUP BY', 'ORDER BY', 'HAVING', 'LIMIT', 'OFFSET',
  'INSERT INTO', 'VALUES', 'UPDATE', 'SET', 'DELETE FROM', 'TRUNCATE',
  'CREATE TABLE', 'CREATE INDEX', 'CREATE VIEW', 'ALTER TABLE', 'DROP TABLE',
  'WITH', 'AS', 'ON', 'AND', 'OR', 'NOT', 'NULL', 'IS NULL', 'IS NOT NULL',
  'IN', 'BETWEEN', 'LIKE', 'CASE', 'WHEN', 'THEN', 'ELSE', 'END',
  'DISTINCT', 'COUNT', 'SUM', 'AVG', 'MIN', 'MAX', 'COALESCE', 'CAST',
  'EXPLAIN', 'SHOW', 'DESCRIBE',
]

const PG_KEYWORDS = ['RETURNING', 'ILIKE', 'OVERLAPS', 'EXCEPT', 'INTERSECT']
const MARIADB_KEYWORDS = ['REPLACE INTO', 'IGNORE', 'STRAIGHT_JOIN']

/**
 * @param {{connId: string, engine: 'mariadb'|'postgres', getSql: ()=>string, getCursor: ()=>number}} ctx
 */
export function makeCompletions(ctx) {
  /**
   * @param {{state: any, pos: number, matchBefore: (re:RegExp)=>{from:number,to:number,text:string}|null}} cmCtx
   */
  return function customSqlCompletions(cmCtx) {
    const word = cmCtx.matchBefore(/[\w."`]+/)
    if (!word && !cmCtx.explicit) return null
    const from = word ? word.from : cmCtx.pos
    const tokenText = word ? word.text : ''
    const sql = ctx.getSql()
    const before = sql.slice(0, from).toUpperCase()

    const options = []
    const seenLabels = new Set()
    const push = (label, type, boost, detail) => {
      if (seenLabels.has(label)) return
      seenLabels.add(label)
      options.push({ label, type, boost, detail })
    }

    // Determine context: what keyword precedes the caret on the same line?
    const lastNonWs = before.match(/(\bFROM|\bJOIN|\bINTO|\bUPDATE|\bTABLE|\bSELECT)\s*$/)
    const ctxKw = lastNonWs ? lastNonWs[1] : ''

    // schema.table.column → column suggestions
    const dotMatch = /([A-Za-z_][\w]*)\.([A-Za-z_][\w]*)\.$/.exec(before)
    if (dotMatch) {
      const [, sch, tbl] = dotMatch
      const t = peekTable(ctx.connId, sch.toLowerCase(), tbl.toLowerCase())
      if (!t) loadTable(ctx.connId, sch.toLowerCase(), tbl.toLowerCase()).catch(() => {})
      if (t && Array.isArray(t.columns)) {
        for (const c of t.columns) {
          push(c.name, 'property', 30, c.dataType || 'column')
        }
      }
      return { from, options }
    }

    // schema.table → either columns (if we know them) or just stop here
    const oneDot = /([A-Za-z_][\w]*)\.$/.exec(before)
    if (oneDot) {
      const sch = oneDot[1].toLowerCase()
      const objs = peekObjects(ctx.connId, sch)
      if (!objs) loadObjects(ctx.connId, sch).catch(() => {})
      if (objs && Array.isArray(objs.tables)) {
        for (const t of objs.tables) {
          push(t.name, 'class', 25, 'table')
        }
      }
      if (objs && Array.isArray(objs.views)) {
        for (const v of objs.views) {
          push(v.name, 'class', 22, 'view')
        }
      }
      return { from, options }
    }

    // Bare identifier — offer keywords + schemas + (if FROM/JOIN ctx) tables
    const tokenU = tokenText.toUpperCase()

    // Keywords
    const kwSeed = ctx.engine === 'postgres'
      ? ANSI_KEYWORDS.concat(PG_KEYWORDS)
      : ANSI_KEYWORDS.concat(MARIADB_KEYWORDS)
    for (const kw of kwSeed) {
      if (!tokenU || kw.startsWith(tokenU)) {
        push(kw, 'keyword', 0, 'keyword')
      }
    }

    // Schemas
    const schemas = getSchemas(ctx.connId)
    for (const s of schemas) {
      push(s, 'namespace', 5, 'schema')
    }

    // If FROM/JOIN/INTO/UPDATE → flatten known tables across all loaded schemas
    if (ctxKw === 'FROM' || ctxKw === 'JOIN' || ctxKw === 'INTO' || ctxKw === 'UPDATE' || ctxKw === 'TABLE') {
      for (const s of schemas) {
        const objs = peekObjects(ctx.connId, s)
        if (objs && Array.isArray(objs.tables)) {
          for (const t of objs.tables) {
            push(t.name, 'class', 10, s + '.' + t.name)
          }
        }
      }
    }

    return { from, options }
  }
}
