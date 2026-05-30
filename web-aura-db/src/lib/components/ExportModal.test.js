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

  // PR #16.5 additions ----------------------------------------------------

  it('uses the shared Btn / Spinner / Pill components (DC-8 / DC-13)', () => {
    expect(source).toMatch(/import Btn from '\.\/Btn\.svelte'/)
    expect(source).toMatch(/import Spinner from '\.\/Spinner\.svelte'/)
    expect(source).toMatch(/import Pill from '\.\/Pill\.svelte'/)
    // Footer CTAs use <Btn ...>, not raw <button>.
    expect(source).toMatch(/<Btn[^>]*onclick=\{submit\}/)
  })

  it('imports sanitizeExportFilename for ux-9 preview parity', () => {
    expect(source).toMatch(/sanitizeExportFilename/)
  })

  it('exposes a Retry-After countdown banner for ux-5 (409 path)', () => {
    expect(source).toMatch(/retryAfter/)
    expect(source).toMatch(/another export is already running/)
  })

  it('runs a pre-flight count probe (ux-6 / DC-5)', () => {
    expect(source).toMatch(/api\.countRowsPreflight/)
    expect(source).toMatch(/preflightTotal/)
  })

  it('renders a row-limit input clamped to the server hard cap (DC-10)', () => {
    expect(source).toMatch(/EXPORT_ROW_HARD_CAP\s*=\s*1_000_000/)
    expect(source).toMatch(/aria-label="Row limit"/)
  })

  it('renders inherited filter / sort pills (DC-4)', () => {
    expect(source).toMatch(/Filter:\s*\{inheritedFilterCount\}/)
    expect(source).toMatch(/Sort:\s*\{inheritedSortCount\}/)
  })

  it('uses an SVG-free internal Spinner via component for in-flight state (DC-8)', () => {
    // Spinner component used, not the raw text "Exporting…" with no spinner.
    expect(source).toMatch(/<Spinner\b/)
  })

  it('footer CTA shortens to "Export" (DC-12) and switches to "Done" after success (DC-1)', () => {
    expect(source).toMatch(/>Export<\/Btn>/)
    expect(source).toMatch(/>Done<\/Btn>/)
  })

  it('drops the unused onClose prop (ux-11)', () => {
    // onClose is no longer in $props().
    expect(source).not.toMatch(/onClose,?\s*\}\s*=\s*\$props\(\)/)
  })

  it('uses a column-search input for wide tables (DC-11)', () => {
    expect(source).toMatch(/colSearch/)
    expect(source).toMatch(/aria-label="Filter columns"/)
  })

  it('progress hint surfaces rows + ETA (ux-7)', () => {
    expect(source).toMatch(/progressRows/)
    expect(source).toMatch(/etaText/)
  })

  it('uses millisecond-precision timestamp in default filename (ux-10)', () => {
    // tsStamp helper pads UTCMilliseconds to 3 digits.
    expect(source).toMatch(/getUTCMilliseconds/)
  })
})
