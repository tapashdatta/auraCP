// Pure key→action dispatch for the row grid. Keeping this as a pure
// function makes the keyboard contract easy to unit-test without spinning
// up the full Svelte tree. The TableScreen wires each action name to a
// runtime handler.
//
// Modes:
//   'cell'   — body focused, not editing, no filter input focused
//   'edit'   — currently editing a cell
//   'filter' — filter-bar input has focus

/**
 * @typedef {{
 *   key: string,
 *   metaKey?: boolean,
 *   ctrlKey?: boolean,
 *   shiftKey?: boolean,
 *   altKey?: boolean,
 * }} KeyLike
 *
 * @typedef {
 *   'move.up'|'move.down'|'move.left'|'move.right'|
 *   'move.tab'|'move.tabBack'|
 *   'move.home'|'move.end'|
 *   'move.firstRow'|'move.lastRow'|
 *   'page.next'|'page.prev'|'page.first'|'page.last'|
 *   'view.pageDown'|'view.pageUp'|
 *   'edit.start'|'edit.commit'|'edit.commitAndExit'|'edit.cancel'|'edit.startTyping'|'edit.clear'|
 *   'select.toggle'|'select.all'|'select.clear'|
 *   'row.delete'|'row.insert'|
 *   'history.undo'|'history.redo'|
 *   'view.refresh'|'view.findFocus'|
 *   'filter.commit'|'filter.cancel'|'filter.intoBody'
 * } ActionName
 */

/**
 * @param {KeyLike} e
 * @param {'cell'|'edit'|'filter'} mode
 * @returns {ActionName | null}
 */
export function keyToAction(e, mode) {
  const cmd = !!(e.metaKey || e.ctrlKey)
  const shift = !!e.shiftKey
  if (mode === 'filter') {
    if (e.key === 'Enter') return 'filter.commit'
    if (e.key === 'Escape') return 'filter.cancel'
    if (e.key === 'ArrowDown') return 'filter.intoBody'
    return null
  }
  if (mode === 'edit') {
    if (e.key === 'Enter') return 'edit.commit'
    if (e.key === 'Escape') return 'edit.cancel'
    // a11y-6: Tab in cell-edit mode commits the value and lets the browser
    // perform its normal focus advance (out of the grid root). The WAI-ARIA
    // grid pattern uses F2/Enter for in-grid navigation; Tab is reserved
    // for moving between widgets so the user is never trapped inside the
    // grid by a stuck edit cell.
    if (e.key === 'Tab') return 'edit.commitAndExit'
    return null
  }
  // mode === 'cell'
  if (cmd) {
    if (e.key === 'a' || e.key === 'A') return 'select.all'
    if (e.key === 'z' || e.key === 'Z') return shift ? 'history.redo' : 'history.undo'
    if (e.key === 'y' || e.key === 'Y') return 'history.redo'
    if (e.key === 'r' || e.key === 'R') return 'view.refresh'
    if (e.key === 'n' || e.key === 'N') return 'row.insert'
    if (e.key === 'f' || e.key === 'F') return 'view.findFocus'
    if (e.key === 'PageDown') return 'page.next'
    if (e.key === 'PageUp') return 'page.prev'
    if (e.key === 'Home') return 'page.first'
    if (e.key === 'End') return 'page.last'
  }
  switch (e.key) {
    case 'ArrowUp':    return 'move.up'
    case 'ArrowDown':  return 'move.down'
    case 'ArrowLeft':  return 'move.left'
    case 'ArrowRight': return 'move.right'
    case 'Tab':        return shift ? 'move.tabBack' : 'move.tab'
    case 'Home':       return 'move.home'
    case 'End':        return 'move.end'
    case 'PageDown':   return 'view.pageDown'
    case 'PageUp':     return 'view.pageUp'
    case 'Enter':      return 'edit.start'
    case 'F2':         return 'edit.start'
    case 'Escape':     return 'select.clear'
    case ' ':          return 'select.toggle'
    case 'Delete':     return 'row.delete'
    case 'Backspace':  return 'edit.clear'
    case '/':          return 'view.findFocus'
    default:
      if (e.key && e.key.length === 1 && !cmd) return 'edit.startTyping'
      return null
  }
}

/**
 * Clamp a (row, col) target to grid bounds.
 *
 * @param {{row:number, col:number}} pos
 * @param {{rows:number, cols:number}} bounds
 */
export function clampPos(pos, bounds) {
  return {
    row: Math.max(0, Math.min(bounds.rows - 1, pos.row | 0)),
    col: Math.max(0, Math.min(bounds.cols - 1, pos.col | 0)),
  }
}
