import { describe, it, expect } from 'vitest'
import { keyToAction, clampPos } from '../../src/lib/rowgrid/keyboard.js'

describe('keyToAction — cell mode', () => {
  it('maps arrow keys to move actions', () => {
    expect(keyToAction({ key: 'ArrowUp' }, 'cell')).toBe('move.up')
    expect(keyToAction({ key: 'ArrowDown' }, 'cell')).toBe('move.down')
    expect(keyToAction({ key: 'ArrowLeft' }, 'cell')).toBe('move.left')
    expect(keyToAction({ key: 'ArrowRight' }, 'cell')).toBe('move.right')
  })
  it('Enter / F2 enters edit mode', () => {
    expect(keyToAction({ key: 'Enter' }, 'cell')).toBe('edit.start')
    expect(keyToAction({ key: 'F2' }, 'cell')).toBe('edit.start')
  })
  it('printable key starts overtype edit', () => {
    expect(keyToAction({ key: 'a' }, 'cell')).toBe('edit.startTyping')
    expect(keyToAction({ key: 'Z' }, 'cell')).toBe('edit.startTyping')
  })
  it('Cmd/Ctrl+Z undo, Shift = redo', () => {
    expect(keyToAction({ key: 'z', metaKey: true }, 'cell')).toBe('history.undo')
    expect(keyToAction({ key: 'z', ctrlKey: true }, 'cell')).toBe('history.undo')
    expect(keyToAction({ key: 'Z', metaKey: true, shiftKey: true }, 'cell')).toBe('history.redo')
  })
  it('Cmd/Ctrl+A selects all', () => {
    expect(keyToAction({ key: 'a', metaKey: true }, 'cell')).toBe('select.all')
  })
  it('Delete triggers row delete', () => {
    expect(keyToAction({ key: 'Delete' }, 'cell')).toBe('row.delete')
  })
})

describe('keyToAction — edit mode', () => {
  it('Enter commits, Escape cancels', () => {
    expect(keyToAction({ key: 'Enter' }, 'edit')).toBe('edit.commit')
    expect(keyToAction({ key: 'Escape' }, 'edit')).toBe('edit.cancel')
  })
  it('Tab in edit-mode commits the value and exits the grid (a11y-6)', () => {
    expect(keyToAction({ key: 'Tab' }, 'edit')).toBe('edit.commitAndExit')
    expect(keyToAction({ key: 'Tab', shiftKey: true }, 'edit')).toBe('edit.commitAndExit')
  })
})

describe('keyToAction — filter mode', () => {
  it('Enter commits filter, Escape clears', () => {
    expect(keyToAction({ key: 'Enter' }, 'filter')).toBe('filter.commit')
    expect(keyToAction({ key: 'Escape' }, 'filter')).toBe('filter.cancel')
    expect(keyToAction({ key: 'ArrowDown' }, 'filter')).toBe('filter.intoBody')
  })
  it('other keys are passthrough', () => {
    expect(keyToAction({ key: 'x' }, 'filter')).toBeNull()
  })
})

describe('clampPos', () => {
  it('clamps row and col to bounds', () => {
    expect(clampPos({ row: -1, col: -1 }, { rows: 5, cols: 3 })).toEqual({ row: 0, col: 0 })
    expect(clampPos({ row: 99, col: 99 }, { rows: 5, cols: 3 })).toEqual({ row: 4, col: 2 })
    expect(clampPos({ row: 2, col: 1 }, { rows: 5, cols: 3 })).toEqual({ row: 2, col: 1 })
  })
})
