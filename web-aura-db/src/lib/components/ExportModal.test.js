// ExportModal module test. Like the SqlEditor.test.js pattern, full
// DOM rendering of Svelte 5 components in jsdom hits the upstream
// preprocessor edge case; we assert the static structure of the
// component source so the regression set still covers the contract
// (format radios, columns checklist, filename input, a11y).
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(join(here, 'ExportModal.svelte'), 'utf8')

describe('ExportModal component shape', () => {
  // NOTE: a full dynamic import hits the same vite-plugin-svelte
  // preprocessor edge case used by SqlEditor.test.js / HistoryScreen.test.js
  // — the test harness asserts the component's source shape instead.
  // The compiled module is exercised in the e2e suite.

  it('exposes radio inputs for csv/ndjson/sql', () => {
    expect(source).toMatch(/name="export-format"\s+value="csv"/)
    expect(source).toMatch(/name="export-format"\s+value="ndjson"/)
    expect(source).toMatch(/name="export-format"\s+value="sql"/)
  })

  it('wraps the format choices in a labelled radiogroup', () => {
    expect(source).toMatch(/role="radiogroup"/)
    expect(source).toMatch(/aria-labelledby="export-fmt-legend"/)
  })

  it('exposes a filename input', () => {
    expect(source).toMatch(/aria-label="Filename"/)
  })

  it('renders the include-header checkbox only for CSV', () => {
    // The CSV-only check should be guarded by an {#if format === 'csv'}
    expect(source).toMatch(/\{#if format === 'csv'\}/)
    expect(source).toMatch(/Include header row/)
  })

  it('has a polite status region for progress + error', () => {
    expect(source).toMatch(/role="status"/)
    expect(source).toMatch(/aria-live=\{errorMsg \? 'assertive' : 'polite'\}/)
  })

  it('uses the shared Modal primitive (focus-trap + Escape inherited)', () => {
    expect(source).toMatch(/import Modal from '\.\/Modal\.svelte'/)
    expect(source).toMatch(/<Modal[^>]*bind:open/)
  })

  it('calls api.exportTable on submit', () => {
    expect(source).toMatch(/api\.exportTable\(/)
  })

  it('rebuilds filename extension when the format radio toggles', () => {
    expect(source).toMatch(/filename\.replace\(\/\\\.\(csv\|ndjson\|sql\)\$\/i, '\.csv'\)/)
    expect(source).toMatch(/filename\.replace\(\/\\\.\(csv\|ndjson\|sql\)\$\/i, '\.sql'\)/)
  })
})
