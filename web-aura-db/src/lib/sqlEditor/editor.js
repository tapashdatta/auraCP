// CodeMirror 6 factory. Composes the minimum extension set the SQL
// editor needs:
//
//   - sql() language (engine-aware dialect + autocomplete schema seed)
//   - line numbers, bracket matching, fold gutter
//   - history, search, custom keymap (Cmd+Enter, Cmd+. etc.)
//   - autocompletion bound to the project's schema-cache backed provider
//   - a lightweight "oxidized-copper" theme that respects panel tokens
//
// INT-7 (PR #13.5): @codemirror/search is intentionally kept — it
// adds ~12-15 KB gz for a feature SQL-editor users genuinely expect
// (Ctrl+F / Cmd+G find-in-buffer is table-stakes in DataGrip,
// VSCode, Workbench). The shortcuts are surfaced via the editor's
// aria-keyshortcuts so AT users can find them. The cost is bounded
// (the editor chunk's bundle ceiling absorbs it; see tests/bundleBudget.test.js)
// and dropping it would force users into the panel-level palette for
// in-buffer find which is a workflow regression.
//
// Bundle note: CodeMirror is imported statically from this file because
// the editor is the first paint on /query — lazy-loading would force a
// spinner on every editor entry. The sql-formatter is the only thing
// that splits into its own chunk.

import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { bracketMatching, foldGutter, indentOnInput, syntaxHighlighting, defaultHighlightStyle } from '@codemirror/language'
import { closeBrackets, closeBracketsKeymap, autocompletion, completionKeymap } from '@codemirror/autocomplete'
import { searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { sql, PostgreSQL, MySQL } from '@codemirror/lang-sql'
import { lintKeymap } from '@codemirror/lint'

import { theme } from './theme.js'
import { makeCompletions } from './completions.js'

/**
 * @param {object} opts
 * @param {HTMLElement} opts.parent
 * @param {string} [opts.doc]
 * @param {'mariadb'|'postgres'} opts.engine
 * @param {string} opts.connId
 * @param {(doc:string)=>void} [opts.onChange]
 * @param {(pos:number)=>void} [opts.onCursor]            cursor / selection moved
 * @param {(view:EditorView, pos:number)=>void} [opts.onExecute]       Cmd+Enter
 * @param {(view:EditorView)=>void} [opts.onExecuteAll]    Cmd+Shift+Enter
 * @param {(view:EditorView, pos:number)=>void} [opts.onExplain]       Cmd+E
 * @param {(view:EditorView)=>void} [opts.onCancel]        Cmd+.
 * @param {(view:EditorView)=>void} [opts.onFormat]        Cmd+Shift+F
 * @param {(view:EditorView)=>void} [opts.onSave]          Cmd+S
 * @returns {EditorView}
 */
export function createEditorView(opts) {
  const dialect = opts.engine === 'postgres' ? PostgreSQL : MySQL
  const completions = makeCompletions({
    connId: opts.connId,
    engine: opts.engine,
    getSql: () => view.state.doc.toString(),
    getCursor: () => view.state.selection.main.head,
  })

  const customKeymap = [
    {
      key: 'Mod-Enter',
      preventDefault: true,
      run: (v) => {
        // EXEC-1: pass the current cursor head so the caller does NOT
        // rely on a stale prop. CM6 passes the view to keymap runners.
        const head = (v?.state?.selection?.main?.head) ?? view.state.selection.main.head
        opts.onExecute?.(v || view, head)
        return true
      },
    },
    {
      key: 'Mod-Shift-Enter',
      preventDefault: true,
      run: (v) => { opts.onExecuteAll?.(v || view); return true },
    },
    {
      key: 'Mod-e',
      preventDefault: true,
      run: (v) => {
        const head = (v?.state?.selection?.main?.head) ?? view.state.selection.main.head
        opts.onExplain?.(v || view, head)
        return true
      },
    },
    {
      key: 'Mod-.',
      preventDefault: true,
      run: (v) => { opts.onCancel?.(v || view); return true },
    },
    {
      key: 'Mod-Shift-f',
      preventDefault: true,
      run: (v) => { opts.onFormat?.(v || view); return true },
    },
    {
      key: 'Mod-s',
      preventDefault: true,
      run: (v) => { opts.onSave?.(v || view); return true },
    },
  ]

  const extensions = [
    lineNumbers(),
    highlightActiveLine(),
    highlightActiveLineGutter(),
    foldGutter(),
    history(),
    bracketMatching(),
    closeBrackets(),
    indentOnInput(),
    highlightSelectionMatches(),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    autocompletion({ override: [completions], activateOnTyping: true }),
    sql({ dialect, upperCaseKeywords: true }),
    EditorView.lineWrapping,
    // a11y-01: the CM6 contenteditable surface has no native label;
    // screen readers announce it as a bare "edit, multi-line" without
    // this facet. We also advertise the most useful shortcuts via
    // aria-keyshortcuts so AT users can discover Cmd+Enter / Cmd+. /
    // Cmd+Shift+F without spelunking the docs.
    EditorView.contentAttributes.of({
      'aria-label': `SQL editor (${opts.engine})`,
      'aria-multiline': 'true',
      // INT-7: include Meta+F (find) so the @codemirror/search panel
      // is discoverable to AT users — sighted users will discover it
      // via the keyboard help screen.
      'aria-keyshortcuts': 'Meta+Enter Meta+Shift+Enter Meta+Period Meta+Shift+F Meta+S Meta+E Meta+F',
      role: 'textbox',
    }),
    theme,
    keymap.of([
      ...customKeymap,
      ...closeBracketsKeymap,
      ...defaultKeymap,
      ...historyKeymap,
      ...searchKeymap,
      ...completionKeymap,
      ...lintKeymap,
      indentWithTab,
    ]),
    EditorView.updateListener.of((u) => {
      if (u.docChanged) opts.onChange?.(u.state.doc.toString())
      // EXEC-1: emit cursor on selection or doc change so the screen can
      // resolve the statement under the caret accurately.
      if (u.selectionSet || u.docChanged) {
        opts.onCursor?.(u.state.selection.main.head)
      }
    }),
  ]

  const state = EditorState.create({
    doc: opts.doc ?? '',
    extensions,
  })

  const view = new EditorView({ state, parent: opts.parent })
  return view
}

/**
 * Replace the doc atomically via a CM6 transaction (preserves the undo
 * stack better than re-creating the state).
 *
 * EXEC-12: cursor preservation across a Format pass used to be a pure
 * byte clamp (`Math.min(next.length, head)`), which jumped the caret
 * around in any reformat that lengthened/shortened the prefix. Pass
 * `preserveCursor: 'semantic'` to attempt a token-level anchor: we
 * count non-whitespace characters up to the prior head, walk that
 * same count in the new text, and place the cursor there. Whitespace
 * and case normalisation are the dominant changes a SQL formatter
 * makes, so a non-whitespace anchor is far closer to "the spot the
 * user was reading" than a byte offset.
 *
 * @param {EditorView} view
 * @param {string} next
 * @param {{preserveCursor?: 'byte'|'semantic'}} [opts]
 */
export function replaceDoc(view, next, opts) {
  const head = view.state.selection.main.head
  let anchor
  if (opts && opts.preserveCursor === 'semantic') {
    anchor = semanticCursorMap(view.state.doc.toString(), next, head)
  } else {
    anchor = Math.min(next.length, head)
  }
  view.dispatch({
    changes: { from: 0, to: view.state.doc.length, insert: next },
    selection: { anchor },
  })
}

/**
 * Walk `prev` up to `head` counting non-whitespace characters, then
 * advance through `next` until we have stepped the same count of
 * non-whitespace characters. The resulting index in `next` lands on
 * the same logical token boundary the caret used to sit on.
 *
 * @param {string} prev
 * @param {string} next
 * @param {number} head
 * @returns {number}
 */
function semanticCursorMap(prev, next, head) {
  if (head <= 0) return 0
  // Count non-whitespace chars up to head in prev.
  let target = 0
  for (let i = 0; i < head && i < prev.length; i++) {
    if (!/\s/.test(prev[i])) target++
  }
  if (target === 0) return Math.min(next.length, head)
  // Walk next counting non-whitespace.
  let seen = 0
  for (let j = 0; j < next.length; j++) {
    if (!/\s/.test(next[j])) {
      seen++
      if (seen === target) return j + 1
    }
  }
  return next.length
}
