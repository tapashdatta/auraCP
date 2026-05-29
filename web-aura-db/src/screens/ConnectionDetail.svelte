<script>
  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api, AuraDBError } from '../lib/api.js'
  import { connections, loadConnections } from '../lib/connections.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import StatusDot from '../lib/components/StatusDot.svelte'
  import Pill from '../lib/components/Pill.svelte'
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte'

  const id = $derived(routeState.params.id)
  const conn = $derived(connections.list.find((c) => c.id === id) || null)

  let testing = $state(false)
  /** @type {string|null} */
  let testResult = $state(null)
  let confirmOpen = $state(false)

  async function testConn() {
    if (!id) return
    testing = true; testResult = null
    try {
      const r = await api.testConnection(id)
      testResult = `OK ${r?.latencyMs ?? ''}ms`
    } catch (err) {
      if (err instanceof AuraDBError) testResult = `ERR ${err.code}: ${err.message}`
      else testResult = 'ERR'
    } finally {
      testing = false
    }
  }

  async function doDelete() {
    if (!id) return
    await api.deleteConnection(id)
    await loadConnections()
    navigate('/connections')
  }
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{conn?.name || id}</h1>
    {#if conn}
      <StatusDot state={conn.status || 'idle'} />
      <Pill tone="info">{conn.engine}</Pill>
      {#if conn.readOnly}<Pill tone="warning">{t('tree.readonly')}</Pill>{/if}
    {/if}
  </header>

  <section class="section">
    <h2 class="section__title">Connection</h2>
    {#if conn}
      <table class="data u-max-w-520">
        <tbody>
          <tr><td class="u-detail-row-label">{t('conn.form.host')}</td><td class="num">{conn.host}{conn.port ? ':' + conn.port : ''}</td></tr>
          <tr><td class="u-color-dim">{t('conn.form.database')}</td><td class="num">{conn.database || '—'}</td></tr>
          <tr><td class="u-color-dim">{t('conn.form.username')}</td><td class="num">{conn.username || '—'}</td></tr>
        </tbody>
      </table>
    {/if}
    <div class="u-mt-4 u-row u-row--wrap">
      <Btn variant="primary" loading={testing} onclick={testConn}>{t('conn.detail.test')}</Btn>
      <Btn variant="ghost" onclick={() => navigate(`/connections/${id}`)}>{t('conn.detail.reveal')}</Btn>
      <Btn variant="ghost" onclick={() => navigate('/audit')}>{t('conn.detail.audit')}</Btn>
      <Btn variant="danger" onclick={() => confirmOpen = true}>{t('conn.detail.delete')}</Btn>
    </div>
    {#if testResult}
      <div class="u-test-result" role="status" aria-live="polite">{testResult}</div>
    {/if}
  </section>
</div>

<ConfirmDialog
  bind:open={confirmOpen}
  title={t('conn.detail.delete')}
  message="Delete this connection? Saved queries are kept."
  confirmLabel={t('action.delete')}
  tone="danger"
  onConfirm={doDelete}
/>
