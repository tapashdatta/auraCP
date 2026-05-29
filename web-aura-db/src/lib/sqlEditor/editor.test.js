// EXEC-1 / EXEC-2 regression coverage. Builds a real CM6 EditorView,
// moves the caret into the middle of a 3-statement buffer, dispatches
// the Mod-Enter / Mod-Shift-Enter keymap handlers via runScopeHandlers,
// and verifies:
//   - onExecute receives (view, pos) and pos is the current caret
//   - getStatementAtCursor at that pos resolves to the MIDDLE statement
//   - Mod-Shift-Enter fires onExecuteAll (not onExecute)

import { describe, it, expect, vi } from 'vitest'
import { EditorSelection } from '@codemirror/state'
import { runScopeHandlers } from '@codemirror/view'
import { createEditorView } from './editor.js'
import { splitStatements, getStatementAtCursor } from './splitStatements.js'

function makeHost() {
  const host = document.createElement('div')
  document.body.appendChild(host)
  return host
}

// Synthesize a Mod-Enter (Cmd on mac, Ctrl elsewhere) KeyboardEvent.
// jsdom's navigator.platform is empty / "Linux x86_64" so CM6's
// currentPlatform falls back to non-mac → "Mod-" normalizes to Ctrl-.
// We use ctrlKey + keyCode=13 so runScopeHandlers resolves "Ctrl-Enter".
function modEnter(shift = false) {
  return new KeyboardEvent('keydown', {
    key: 'Enter',
    code: 'Enter',
    keyCode: 13,
    ctrlKey: true,
    shiftKey: shift,
    bubbles: true,
    cancelable: true,
  })
}

describe('editor.js Cmd+Enter / Cmd+Shift+Enter', () => {
  it('EXEC-1: Mod-Enter passes the live cursor position to onExecute', () => {
    const doc = 'SELECT 1;\nSELECT 2;\nDELETE FROM users;'
    const onExecute = vi.fn()
    const view = createEditorView({
      parent: makeHost(),
      doc,
      engine: 'mariadb',
      connId: 'c1',
      onExecute,
    })
    // Position cursor inside SELECT 2.
    const pos = doc.indexOf('SELECT 2') + 2
    view.dispatch({ selection: EditorSelection.cursor(pos) })

    const handled = runScopeHandlers(view, modEnter(false), 'editor')
    expect(handled).toBe(true)

    expect(onExecute).toHaveBeenCalledTimes(1)
    const [argView, argPos] = onExecute.mock.calls[0]
    expect(argView).toBeDefined()
    expect(typeof argPos).toBe('number')
    expect(argPos).toBe(pos)

    // The statement under that cursor should be SELECT 2 (not SELECT 1).
    const stmts = splitStatements(doc)
    const stmt = getStatementAtCursor(stmts, argPos)
    expect(stmt?.trimmedText).toBe('SELECT 2')

    view.destroy()
  })

  it('EXEC-1: onCursor fires when selection changes (no doc change)', () => {
    const doc = 'SELECT 1; SELECT 2;'
    const onCursor = vi.fn()
    const view = createEditorView({
      parent: makeHost(),
      doc,
      engine: 'mariadb',
      connId: 'c1',
      onCursor,
    })
    view.dispatch({ selection: EditorSelection.cursor(12) })
    expect(onCursor).toHaveBeenCalled()
    const last = onCursor.mock.calls[onCursor.mock.calls.length - 1][0]
    expect(last).toBe(12)
    view.destroy()
  })

  it('EXEC-2: Mod-Shift-Enter fires onExecuteAll, not onExecute', () => {
    const doc = 'SELECT 1; SELECT 2; SELECT 3;'
    const onExecute = vi.fn()
    const onExecuteAll = vi.fn()
    const view = createEditorView({
      parent: makeHost(),
      doc,
      engine: 'mariadb',
      connId: 'c1',
      onExecute,
      onExecuteAll,
    })
    const handled = runScopeHandlers(view, modEnter(true), 'editor')
    expect(handled).toBe(true)
    expect(onExecuteAll).toHaveBeenCalledTimes(1)
    expect(onExecute).not.toHaveBeenCalled()
    view.destroy()
  })
})
