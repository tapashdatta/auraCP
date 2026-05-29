// Session-local undo stack for the row grid. Capped at 50. Not persisted —
// the DB write is the source of truth, undo is a UX convenience.
//
// We keep the operation as plain data; the actual reverse-call (PATCH /
// INSERT / DELETE) is performed by the TableScreen at undo/redo time so
// this module stays free of network I/O and easy to test.

const CAP = 50

/**
 * @typedef {
 *   { kind: 'update', pkKey: string, colName: string, before: any, after: any }
 * | { kind: 'insert', pkKey: string, row: any[], columnOrder?: string[] }
 * | { kind: 'delete', pkKey: string, beforeRow: any[], columnOrder?: string[] }
 * } UndoEntry
 *
 * edit-2 / edit-10: row identity in undo entries is the PK string, not a
 * positional rowIdx — pagination, filter, sort, and column reorder all
 * change which row sits at a given index, so by-PK is the only stable
 * identifier. columnOrder snapshots are recorded on insert + delete so
 * the row[] payload can be reassembled into a {col: val} map at undo
 * time even if columns have since been reordered.
 */

export function createUndoStack() {
  /** @type {UndoEntry[]} */
  const entries = []
  let idx = 0

  return {
    /** @returns {UndoEntry[]} */ get entries() { return entries },
    /** @returns {number} */ get idx() { return idx },
    /** @returns {boolean} */ get canUndo() { return idx > 0 },
    /** @returns {boolean} */ get canRedo() { return idx < entries.length },

    /** @param {UndoEntry} e */
    push(e) {
      // Truncate any redo branch.
      if (idx < entries.length) entries.length = idx
      entries.push(e)
      if (entries.length > CAP) {
        entries.shift()
      } else {
        idx++
      }
      // After shift, idx already equals entries.length so canRedo is false.
      if (entries.length === CAP && idx > CAP) idx = CAP
    },

    /** @returns {UndoEntry | null} */
    popForUndo() {
      if (idx === 0) return null
      idx--
      return entries[idx]
    },

    /** @returns {UndoEntry | null} */
    popForRedo() {
      if (idx === entries.length) return null
      const e = entries[idx]
      idx++
      return e
    },

    /** Rewind the cursor by 1 — used when a reverse-op fails so the entry stays available. */
    rewindUndo() { if (idx < entries.length) idx++ },
    rewindRedo() { if (idx > 0) idx-- },

    clear() { entries.length = 0; idx = 0 },
  }
}
