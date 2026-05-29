<script>
  import { navigate, routeState } from '../lib/router.svelte.js'
  import { api, AuraDBError } from '../lib/api.js'
  import { loadConnections } from '../lib/connections.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import TextField from '../lib/components/TextField.svelte'
  import SelectField from '../lib/components/SelectField.svelte'
  import Toggle from '../lib/components/Toggle.svelte'

  /** @type {{ editId?: string }} */
  let { editId } = $props()

  const isEdit = $derived(!!editId)
  const engines = [
    { value: 'postgres', label: t('tree.engine.postgres') },
    { value: 'mysql',    label: t('tree.engine.mysql') },
    { value: 'sqlite',   label: t('tree.engine.sqlite') },
    { value: 'mssql',    label: t('tree.engine.mssql') },
    { value: 'oracle',   label: t('tree.engine.oracle') },
  ]

  let name = $state('')
  let engine = $state('postgres')
  let host = $state('')
  let port = $state('5432')
  let database = $state('')
  let username = $state('')
  let password = $state('')
  let readOnly = $state(false)
  let saving = $state(false)
  /** @type {string|null} */
  let error = $state(null)

  $effect(() => {
    void routeState // keep reactive on route change
    if (isEdit && editId) {
      api.getConnection(editId).then((c) => {
        name = c?.name || ''
        engine = c?.engine || 'postgres'
        host = c?.host || ''
        port = String(c?.port ?? '')
        database = c?.database || ''
        username = c?.username || ''
        readOnly = !!c?.readOnly
      }).catch(() => { /* shell-stage: handled by ErrorBoundary in PR #12 */ })
    }
  })

  async function save() {
    saving = true; error = null
    try {
      const body = { name, engine, host, port: Number(port) || undefined, database, username, password, readOnly }
      if (isEdit && editId) await api.updateConnection(editId, body)
      else await api.createConnection(body)
      await loadConnections()
      navigate('/connections')
    } catch (err) {
      if (err instanceof AuraDBError) error = err.message
      else error = 'failed to save'
    } finally {
      saving = false
    }
  }
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{isEdit ? t('conn.form.title.edit') : t('conn.form.title.new')}</h1>
  </header>

  <section class="section">
    <div class="u-grid-2 u-grid-2--narrow">
      <TextField label={t('conn.form.name')} bind:value={name} placeholder="Production replica" />
      <SelectField label={t('conn.form.engine')} bind:value={engine} options={engines} />
      <TextField label={t('conn.form.host')} bind:value={host} placeholder="db.internal" mono />
      <TextField label={t('conn.form.port')} bind:value={port} mono />
      <TextField label={t('conn.form.database')} bind:value={database} mono />
      <TextField label={t('conn.form.username')} bind:value={username} mono />
      <TextField label={t('conn.form.password')} bind:value={password} type="password" />
      <div class="u-row u-row--end">
        <Toggle bind:value={readOnly} label={t('conn.form.readonly')} />
      </div>
    </div>

    {#if error}
      <div class="u-form-error" role="alert">{error}</div>
    {/if}

    <div class="u-mt-4 u-row">
      <Btn variant="primary" loading={saving} onclick={save}>{t('conn.form.save')}</Btn>
      <Btn variant="ghost" onclick={() => navigate('/connections')}>{t('conn.form.cancel')}</Btn>
    </div>
  </section>
</div>
