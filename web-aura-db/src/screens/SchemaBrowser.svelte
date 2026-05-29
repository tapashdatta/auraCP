<script>
  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { t } from '../lib/strings.js'
  import { openTab } from '../lib/workspaces.svelte.js'
  import LoadingPane from '../lib/components/LoadingPane.svelte'

  const id = $derived(routeState.params.id)
  const schema = $derived(routeState.params.schema)

  let loading = $state(true)
  /** @type {{tables:any[], views:any[], functions:any[]}} */
  let data = $state({ tables: [], views: [], functions: [] })

  $effect(() => {
    if (!id || !schema) return
    loading = true
    api.listObjects(id, schema).then((r) => {
      data = {
        tables: r?.tables || [],
        views: r?.views || [],
        functions: r?.functions || [],
      }
    }).catch(() => { /* shell-stage */ }).finally(() => { loading = false })
  })

  // WIRE-11 (PR #12.5): a real double-click previously fired BOTH the
  // single-click and the double-click handlers in sequence (browser
  // semantics — the second mousedown raises dblclick only AFTER the
  // first click event has fired). The grid de-dups by path, so the
  // user never saw two tabs, but they did see the table-detail route
  // mount briefly before the rows route replaced it (a flash of the
  // wrong screen). Fix: schedule the single-click open on a short
  // timer; if a dblclick arrives within ~250 ms, cancel the timer and
  // route to the rows screen instead.
  /** @type {ReturnType<typeof setTimeout> | null} */
  let pendingClickTimer = null
  const DBLCLICK_WINDOW_MS = 250

  function openTable(tbl) {
    if (pendingClickTimer) clearTimeout(pendingClickTimer)
    pendingClickTimer = setTimeout(() => {
      pendingClickTimer = null
      openTab({
        title: `${schema}.${tbl.name}`,
        path: `/connections/${id}/schemas/${schema}/tables/${tbl.name}`,
        icon: 'table',
      })
      navigate(`/connections/${id}/schemas/${schema}/tables/${tbl.name}`)
    }, DBLCLICK_WINDOW_MS)
  }
  function openTableRows(tbl) {
    if (pendingClickTimer) { clearTimeout(pendingClickTimer); pendingClickTimer = null }
    openTab({
      title: `${schema}.${tbl.name}`,
      path: `/connections/${id}/schemas/${schema}/tables/${tbl.name}/rows`,
      icon: 'table',
    })
    navigate(`/connections/${id}/schemas/${schema}/tables/${tbl.name}/rows`)
  }
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('schema.title', { schema })}</h1>
  </header>

  {#if loading}
    <LoadingPane />
  {:else}
    <section class="section">
      <h2 class="section__title">{t('schema.objects.tables')}</h2>
      {#if data.tables.length === 0}
        <div class="schema__note">No tables.</div>
      {:else}
        <table class="data">
          <tbody>
            {#each data.tables as tbl (tbl.name)}
              <tr
                class="u-cursor-pointer"
                tabindex="0"
                role="link"
                aria-label={`Open ${tbl.name}`}
                onclick={() => openTable(tbl)}
                ondblclick={() => openTableRows(tbl)}
                onkeydown={(e) => {
                  // Enter bypasses the dblclick debounce — keyboard
                  // users can't double-click, so the open is immediate.
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    if (pendingClickTimer) { clearTimeout(pendingClickTimer); pendingClickTimer = null }
                    openTab({
                      title: `${schema}.${tbl.name}`,
                      path: `/connections/${id}/schemas/${schema}/tables/${tbl.name}`,
                      icon: 'table',
                    })
                    navigate(`/connections/${id}/schemas/${schema}/tables/${tbl.name}`)
                  }
                }}
                title="Double-click to open rows"
              >
                <td class="num">{tbl.name}</td>
                <td class="num u-color-mute">{tbl.rowEstimate ?? ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
    <section class="section">
      <h2 class="section__title">{t('schema.objects.views')}</h2>
      <div class="schema__note">{data.views.length} view(s)</div>
    </section>
    <section class="section">
      <h2 class="section__title">{t('schema.objects.functions')}</h2>
      <div class="schema__note">{data.functions.length} function(s)</div>
    </section>
  {/if}
</div>

<style>
  .schema__note { color: var(--text-dim); font-size: var(--fs-body); }
</style>
