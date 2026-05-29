// Oxidized-copper editor theme. Pulls colors from the panel's CSS
// custom properties so light/dark mode flips with the rest of the UI.
// Keeps the font + size aligned with the project's monospace strings.

import { EditorView } from '@codemirror/view'

export const theme = EditorView.theme({
  '&': {
    height: '100%',
    fontFamily: 'IBM Plex Mono, ui-monospace, SFMono-Regular, Menlo, monospace',
    fontSize: '13px',
    color: 'var(--text-1, #e6e6e6)',
    background: 'var(--surface-1, #14110f)',
  },
  '.cm-scroller': {
    overflow: 'auto',
    fontFamily: 'inherit',
  },
  '.cm-content': {
    caretColor: 'var(--accent-copper, #c46a3a)',
    padding: '8px 0',
  },
  '.cm-gutters': {
    backgroundColor: 'var(--surface-2, #1a1614)',
    color: 'var(--text-mute, #6a6560)',
    border: 'none',
    borderRight: '1px solid var(--border-1, #2a2521)',
  },
  '.cm-activeLine': {
    backgroundColor: 'var(--surface-active, rgba(196,106,58,0.07))',
  },
  '.cm-activeLineGutter': {
    backgroundColor: 'var(--surface-active, rgba(196,106,58,0.10))',
    color: 'var(--accent-copper, #c46a3a)',
  },
  '.cm-selectionMatch': {
    backgroundColor: 'rgba(196,106,58,0.20)',
  },
  '&.cm-focused .cm-selectionBackground, .cm-line ::selection': {
    backgroundColor: 'rgba(196,106,58,0.30) !important',
  },
  '.cm-tooltip': {
    background: 'var(--surface-3, #211c19)',
    border: '1px solid var(--border-1, #2a2521)',
    color: 'var(--text-1, #e6e6e6)',
    boxShadow: '0 8px 24px rgba(0,0,0,0.3)',
  },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': {
    background: 'var(--accent-copper, #c46a3a)',
    color: 'var(--surface-1, #14110f)',
  },
}, { dark: true })
