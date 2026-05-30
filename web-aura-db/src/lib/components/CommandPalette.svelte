<script>
  // Command palette overlay. Cmd-K toggles it from anywhere via
  // shortcuts.svelte.js. The palette is a transient UI overlay — not a
  // route — so deep-links don't accidentally pop a modal and the back
  // button doesn't close it out from under the user.
  //
  // ARIA: combobox + listbox per WAI-ARIA APG. aria-activedescendant
  // points at the focused option while real DOM focus stays on the
  // input, so screen readers announce the selected row while the user
  // keeps typing.

  import { tick } from 'svelte'
  import { palette, closePalette, buildRegistry, groupBySection } from '../palette.svelte.js'
  import { connections } from '../connections.svelte.js'
  import { historyCache, primeHistoryCache } from '../recentHistory.svelte.js'
  import { routeState } from '../router.svelte.js'
  import { highlight } from '../fuzzy.js'
  import { handleFocusTrap } from '../focusTrap.js'

  let query = $state('')
  let cursor = $state(0)
  /** @type {HTMLInputElement | null} */
  let inputEl = $state(null)
  /** @type {HTMLDivElement | null} */
  let listEl = $state(null)
  /** @type {HTMLDivElement | null} */
  let paletteEl = $state(null)
  /** @type {Element | null} */
  let prevFocus = null

  // A11Y-1: focus trap — Tab/Shift+Tab cycle within the palette so
  // keyboard users can't accidentally tab out into the page behind.
  // Mirrors PR #11 Modal.svelte. The actual trap logic lives in
  // ../focusTrap.js so it can be unit-tested without rendering the
  // full Svelte component (an upstream Vite preprocessor edge case
  // in this codebase prevents jsdom from rendering palette markup —
  // see SqlEditor.test.js for the same workaround).

  const activeConnId = $derived(routeState?.params?.id || connections.selectedId || connections.list?.[0]?.id || '')

  const cmds = $derived(buildRegistry({
    query,
    connections: connections.list || [],
    history: historyCache.entries || [],
    saved: historyCache.saved || [],
    activeConnId,
  }))

  // C2: when a query is active, render flat (score-sorted) so the
  // top-ranked fuzzy match wins regardless of which section it belongs
  // to. When the query is empty, fall back to grouped browse view
  // (sections + section ordering) so first-open scanning is calm.
  const hasQuery = $derived(query.trim().length > 0)
  const groups = $derived(hasQuery ? [] : groupBySection(cmds))

  // Flat list of items in render order, used by keyboard navigation.
  // When grouped, walk groups in order; when flat, cmds is already in
  // score-sorted order from buildRegistry.
  const flat = $derived.by(() => {
    if (hasQuery) return cmds.slice()
    const out = []
    for (const g of groups) for (const it of g.items) out.push(it)
    return out
  })

  // Clamp cursor whenever the result set shrinks.
  $effect(() => {
    if (cursor >= flat.length) cursor = Math.max(0, flat.length - 1)
  })

  // Open/close lifecycle.
  let wasOpen = false
  $effect(() => {
    if (palette.open && !wasOpen) {
      wasOpen = true
      prevFocus = typeof document !== 'undefined' ? document.activeElement : null
      query = palette.prefill || ''
      cursor = 0
      // Prime the recent-history cache for the active conn.
      if (activeConnId) primeHistoryCache(activeConnId)
      tick().then(() => { inputEl?.focus() })
    } else if (!palette.open && wasOpen) {
      wasOpen = false
      // Restore focus to whatever was focused before the palette opened.
      try { /** @type {any} */(prevFocus)?.focus?.() } catch { /* ignore */ }
      prevFocus = null
    }
  })

  function activate(opts = {}) {
    const item = flat[cursor]
    if (!item) return
    closePalette()
    // Defer the run() so the modal-close animation isn't fighting an
    // immediate navigation.
    queueMicrotask(() => { try { item.run(opts) } catch { /* ignore */ } })
  }

  function onKeydown(ev) {
    if (ev.key === 'Escape') { ev.preventDefault(); closePalette(); return }
    // A11Y-1: trap Tab inside the palette (cycle first <-> last).
    if (handleFocusTrap(ev, paletteEl)) return
    if (ev.key === 'ArrowDown') {
      ev.preventDefault()
      if (flat.length === 0) return
      cursor = (cursor + 1) % flat.length
      scrollCursorIntoView()
      return
    }
    if (ev.key === 'ArrowUp') {
      ev.preventDefault()
      if (flat.length === 0) return
      cursor = (cursor - 1 + flat.length) % flat.length
      scrollCursorIntoView()
      return
    }
    if (ev.key === 'PageDown') {
      ev.preventDefault()
      cursor = Math.min(flat.length - 1, cursor + 8)
      scrollCursorIntoView()
      return
    }
    if (ev.key === 'PageUp') {
      ev.preventDefault()
      cursor = Math.max(0, cursor - 8)
      scrollCursorIntoView()
      return
    }
    if (ev.key === 'Home') {
      ev.preventDefault()
      cursor = 0
      scrollCursorIntoView()
      return
    }
    if (ev.key === 'End') {
      ev.preventDefault()
      cursor = Math.max(0, flat.length - 1)
      scrollCursorIntoView()
      return
    }
    if (ev.key === 'Enter') {
      ev.preventDefault()
      const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
      const mod = isMac ? ev.metaKey : ev.ctrlKey
      activate({ newTab: !!mod })
      return
    }
    if ((ev.metaKey || ev.ctrlKey) && (ev.key === 'Backspace')) {
      ev.preventDefault()
      query = ''
      cursor = 0
    }
  }

  function scrollCursorIntoView() {
    if (!listEl) return
    queueMicrotask(() => {
      const el = listEl?.querySelector(`[data-cursor="${cursor}"]`)
      if (el && typeof /** @type {any} */(el).scrollIntoView === 'function') {
        /** @type {any} */(el).scrollIntoView({ block: 'nearest' })
      }
    })
  }

  function onBack(ev) {
    if (ev.target === ev.currentTarget) closePalette()
  }

  function onItemClick(i) {
    cursor = i
    activate()
  }

  // Total scoring count for header chip.
  const count = $derived(flat.length)
  // A11Y-6: throttle the SR-only count announcer ~250ms so SR users
  // don't hear a flood of "N results" during typing.
  let countAnnounce = $state(0)
  /** @type {ReturnType<typeof setTimeout>|null} */
  let _cntTimer = null
  $effect(() => {
    const n = flat.length
    if (_cntTimer) clearTimeout(_cntTimer)
    _cntTimer = setTimeout(() => { countAnnounce = n }, 250)
  })
  // Pre-compute a per-item index so we can wire data-cursor / aria-activedescendant.
  // In flat mode (hasQuery) we expose a single synthetic group with no
  // section header so the template loop stays uniform.
  const indexed = $derived.by(() => {
    let i = 0
    if (hasQuery) {
      return [{
        label: '',
        items: flat.map((it) => ({ ...it, index: i++ })),
      }]
    }
    return groups.map((g) => ({
      label: g.label,
      items: g.items.map((it) => ({ ...it, index: i++ })),
    }))
  })

  function kindGlyph(kind) {
    switch (kind) {
      case 'connection': return '◇'
      case 'history':    return '↻'
      case 'saved':      return '★'
      case 'table':      return '☷'
      case 'action':     return '⌘'
      default:           return '·'
    }
  }
</script>

{#if palette.open}
  <div
    class="palette-back"
    role="presentation"
    onclick={onBack}
  >
    <!-- FIX (PR #15.5 A11Y-2 routed): wire aria-describedby so AT users
         get the kbd hints as a description on the dialog open. -->
    <div
      bind:this={paletteEl}
      class="palette"
      role="dialog"
      aria-modal="true"
      aria-labelledby="palette-title"
      aria-describedby="palette-desc"
      tabindex="-1"
      onkeydown={onKeydown}
    >
      <h2 id="palette-title" class="palette__sr">Command palette</h2>
      <span id="palette-desc" class="palette__sr">Use up and down arrows to move between results. Press Enter to select. Press Escape to close.</span>

      <div class="palette__inputRow">
        <span class="palette__glyph" aria-hidden="true">
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5">
            <circle cx="7" cy="7" r="4.5" />
            <line x1="10.5" y1="10.5" x2="14" y2="14" />
          </svg>
        </span>
        <!-- FIX (PR #15.5 A11Y-12 / A11Y-14 routed): explicit aria-label
             + mobile keyboard hints. -->
        <input
          bind:this={inputEl}
          bind:value={query}
          oninput={() => { cursor = 0 }}
          class="palette__input"
          type="text"
          role="combobox"
          aria-label="Search commands"
          aria-expanded="true"
          aria-controls="palette-listbox"
          aria-activedescendant={flat[cursor] ? `palette-opt-${flat[cursor].id}` : undefined}
          aria-autocomplete="list"
          autocomplete="off"
          autocapitalize="off"
          autocorrect="off"
          inputmode="search"
          spellcheck="false"
          placeholder="Search connections, history, commands…"
        />
        <span class="palette__count" aria-hidden="true">{count}</span>
      </div>

      <div class="palette__listWrap" bind:this={listEl}>
        {#if flat.length === 0 && historyCache.loading}
          <!-- FIX (PR #15.5 D-4): show a loading state while the first
               primeHistoryCache fanout is in flight. Without this, the
               first open against a slow API briefly shows "No matches"
               which reads as a real empty state instead of "still
               loading". -->
          <div class="palette__empty" role="status" aria-live="polite">
            <div class="palette__emptyTitle">Loading…</div>
            <div class="palette__emptyHint">Fetching recent history and saved queries.</div>
          </div>
        {:else if flat.length === 0}
          <!-- FIX (PR #15.5 A11Y-15 routed): empty-state must be a
               live region so SR users hear that the result set went
               empty after typing.
               FIX (PR #15.5 D-12): when the user typed something and
               nothing matched, suggest clearing the filter as the next
               step. When the query is empty there is genuinely nothing
               to suggest beyond the original onboarding hint. -->
          <div class="palette__empty" role="status" aria-live="polite">
            <div class="palette__emptyTitle">No matches</div>
            {#if hasQuery}
              <div class="palette__emptyHint">No commands match “{query}”. Press <kbd>⌘</kbd><kbd>⌫</kbd> to clear the filter.</div>
            {:else}
              <div class="palette__emptyHint">Try a connection name, table, SQL keyword, or “/” for actions.</div>
            {/if}
          </div>
        {:else}
          <ul
            id="palette-listbox"
            role="listbox"
            class="palette__list"
            aria-label="Palette results"
          >
            {#each indexed as g, gi (gi + ':' + g.label)}
              {#if g.label}
                <li class="palette__sectionHdr" role="presentation">{g.label}</li>
              {/if}
              {#each g.items as it (it.id)}
                {@const chunks = highlight(it.title, it.titlePositions || [])}
                <!-- FIX (PR #15.5 A11Y-11/D-11 routed): full title in
                     title= attr so truncated rows can be hovered to
                     reveal the full string. -->
                <li
                  id={`palette-opt-${it.id}`}
                  role="option"
                  aria-selected={cursor === it.index}
                  class="palette__row"
                  class:palette__row--selected={cursor === it.index}
                  data-cursor={it.index}
                  title={it.title}
                  onmousemove={() => { cursor = it.index }}
                  onclick={() => onItemClick(it.index)}
                  onkeydown={(e) => { if (e.key === 'Enter') onItemClick(it.index) }}
                  tabindex="-1"
                >
                  <span class="palette__rowKind" data-kind={it.kind}>{kindGlyph(it.kind)}</span>
                  <span class="palette__rowMain">
                    <span class="palette__rowTitle">
                      {#each chunks as c, ci (ci)}
                        {#if c.hi}<mark>{c.text}</mark>{:else}{c.text}{/if}
                      {/each}
                    </span>
                    {#if it.subtitle}
                      <span class="palette__rowSub">{it.subtitle}</span>
                    {/if}
                  </span>
                  {#if it.hint}<span class="palette__rowHint">{it.hint}</span>{/if}
                </li>
              {/each}
            {/each}
          </ul>
        {/if}
      </div>

      <div class="palette__footer" aria-hidden="true">
        <span class="palette__kbd"><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span class="palette__kbd"><kbd>↵</kbd> select</span>
        <span class="palette__kbd"><kbd>⌘</kbd><kbd>↵</kbd> new tab</span>
        <!-- FIX (PR #15.5 D-10): surface the palette's own toggle
             shortcut in its footer so first-time users learn it.
             Pressing it while the palette is open also closes the
             palette (same toggle). -->
        <span class="palette__kbd"><kbd>⌘</kbd><kbd>K</kbd> toggle</span>
        <span class="palette__kbd"><kbd>esc</kbd> close</span>
      </div>

      <!-- FIX (PR #15.5 A11Y-6 routed): pluralise and throttle. -->
      <div class="palette__sr" aria-live="polite">{countAnnounce === 1 ? '1 result' : `${countAnnounce} results`}</div>
    </div>
  </div>
{/if}

<style>
  .palette-back {
    position: fixed;
    inset: 0;
    z-index: 90;
    background: rgba(0, 0, 0, 0.24);
    /* FIX (PR #15.5 D-14): 8px blur is GPU-heavy on low-power devices
       (visible jank on Intel iGPUs / older iPads during the open
       animation). 4px is enough to push the page chrome out of focus
       without bringing weak GPUs to their knees. */
    backdrop-filter: blur(4px);
    display: flex;
    justify-content: center;
    align-items: flex-start;
    padding-top: 18vh;
  }
  .palette {
    width: 600px;
    max-width: calc(100vw - 32px);
    max-height: min(560px, 80vh);
    background: var(--surface-elev);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 12px;
    box-shadow: 0 24px 60px -16px rgba(0, 0, 0, 0.5);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .palette__sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
  .palette__inputRow {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 14px;
    border-bottom: 1px solid var(--border);
    min-height: 48px;
  }
  .palette__glyph {
    color: var(--text-mute, #888);
    display: inline-flex;
  }
  .palette__input {
    flex: 1;
    border: none;
    outline: none;
    background: transparent;
    color: var(--text);
    font: 14px/1.4 'IBM Plex Sans', sans-serif;
    min-width: 0;
  }
  .palette__input::placeholder {
    color: var(--text-mute, #888);
  }
  .palette__count {
    font: 11px/1 'IBM Plex Mono', monospace;
    color: var(--text-mute, #888);
    padding: 2px 6px;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  .palette__listWrap {
    flex: 1;
    overflow-y: auto;
    overflow-x: hidden;
    min-height: 0;
  }
  .palette__list {
    list-style: none;
    padding: 4px 0 8px;
    margin: 0;
  }
  .palette__sectionHdr {
    text-transform: uppercase;
    font: 600 10px/1 'IBM Plex Sans', sans-serif;
    letter-spacing: 0.08em;
    color: var(--text-mute, #888);
    /* FIX (PR #15.5 D-13): even the top/bottom padding so the header
       sits at a consistent visual rhythm above the row below it. 8/6
       reads as a calmer divider than the previous 10/4 asymmetry. */
    padding: 8px 14px 6px;
  }
  .palette__row {
    display: grid;
    /* FIX (PR #15.5 D-8): glyph column was 28px for a 13-14px icon —
       too much dead space on the left. Trim to 20px so the title sits
       closer to the icon and matches the tree's column rhythm. */
    grid-template-columns: 20px 1fr auto;
    align-items: center;
    gap: 10px;
    height: 44px;
    padding: 0 14px;
    cursor: pointer;
    /* FIX (PR #15.5 D-6): selection accent thickness should match the
       tree's 2px so the two surfaces feel like the same selection
       system. 3px here was reading as a separate accent token. */
    border-left: 2px solid transparent;
    background: transparent;
    user-select: none;
  }
  .palette__row--selected {
    background: var(--surface-active, var(--surface-hover));
    border-left-color: var(--accent);
  }
  .palette__rowKind {
    color: var(--text-mute, #888);
    font-size: 13px;
    text-align: center;
  }
  .palette__rowMain {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .palette__rowTitle {
    font: 14px/1.3 'IBM Plex Sans', sans-serif;
    color: var(--text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .palette__rowTitle mark {
    background: transparent;
    color: var(--accent);
    font-weight: 600;
  }
  .palette__rowSub {
    font: 11px/1.3 'IBM Plex Mono', monospace;
    color: var(--text-mute, #888);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .palette__rowHint {
    font: 11px/1 'IBM Plex Mono', monospace;
    color: var(--text-mute, #888);
    white-space: nowrap;
  }
  .palette__empty {
    padding: 24px 16px;
    text-align: center;
  }
  .palette__emptyTitle {
    font: 600 13px/1.4 'IBM Plex Sans', sans-serif;
    color: var(--text);
    margin-bottom: 4px;
  }
  .palette__emptyHint {
    font: 12px/1.4 'IBM Plex Sans', sans-serif;
    color: var(--text-mute, #888);
  }
  .palette__footer {
    display: flex;
    gap: 14px;
    padding: 8px 14px;
    border-top: 1px solid var(--border);
    background: var(--surface-sunk, var(--surface));
    font-size: 11px;
    color: var(--text-mute, #888);
  }
  .palette__kbd {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .palette__kbd kbd {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 1px 5px;
    font: 11px/1 'IBM Plex Mono', monospace;
    color: var(--text);
  }
  @media (prefers-reduced-motion: reduce) {
    .palette-back { backdrop-filter: none; }
  }
</style>
