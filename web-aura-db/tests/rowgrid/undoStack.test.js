import { describe, it, expect } from 'vitest'
import { createUndoStack } from '../../src/lib/rowgrid/undoStack.js'

describe('undoStack', () => {
  it('push → undo → redo round-trip', () => {
    const s = createUndoStack()
    s.push({ kind: 'update', rowIdx: 0, colIdx: 0, pkKey: 'id=1', colName: 'x', before: 'a', after: 'b' })
    expect(s.canUndo).toBe(true)
    expect(s.canRedo).toBe(false)
    const e = s.popForUndo()
    expect(e?.kind).toBe('update')
    expect(s.canUndo).toBe(false)
    expect(s.canRedo).toBe(true)
    const e2 = s.popForRedo()
    expect(e2?.kind).toBe('update')
  })
  it('pushing after an undo truncates the redo branch', () => {
    const s = createUndoStack()
    s.push({ kind: 'update', rowIdx: 0, colIdx: 0, pkKey: 'id=1', colName: 'x', before: 'a', after: 'b' })
    s.push({ kind: 'update', rowIdx: 1, colIdx: 0, pkKey: 'id=2', colName: 'x', before: 'c', after: 'd' })
    s.popForUndo() // undo last
    // Push a new op — redo branch should be wiped
    s.push({ kind: 'update', rowIdx: 2, colIdx: 0, pkKey: 'id=3', colName: 'x', before: 'e', after: 'f' })
    expect(s.canRedo).toBe(false)
    expect(s.entries.length).toBe(2)
  })
  it('caps at 50 entries (oldest evicted)', () => {
    const s = createUndoStack()
    for (let i = 0; i < 60; i++) {
      s.push({ kind: 'update', rowIdx: i, colIdx: 0, pkKey: `id=${i}`, colName: 'x', before: 0, after: 1 })
    }
    expect(s.entries.length).toBe(50)
    // The first 10 ops should have been evicted
    expect(s.entries[0].kind === 'update' && s.entries[0].rowIdx).toBe(10)
  })
  it('rewindUndo restores the cursor after a failed reverse', () => {
    const s = createUndoStack()
    s.push({ kind: 'update', rowIdx: 0, colIdx: 0, pkKey: 'id=1', colName: 'x', before: 'a', after: 'b' })
    s.popForUndo()
    s.rewindUndo()
    expect(s.canUndo).toBe(true)
  })
})
