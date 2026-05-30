<script>
  import { navigate, routeState } from '../lib/router.svelte.js'
  import { api, AuraDBError } from '../lib/api.js'
  import { loadConnections } from '../lib/connections.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import Icon from '../lib/components/Icon.svelte'

  /** @type {{ editId?: string }} */
  let { editId } = $props()

  const isEdit = $derived(!!editId)
  const engines = [
    { value: 'postgres', label: t('tree.engine.postgres'), port: '5432' },
    { value: 'mysql',    label: t('tree.engine.mysql'),    port: '3306' },
    { value: 'sqlite',   label: t('tree.engine.sqlite'),   port: '' },
    { value: 'mssql',    label: t('tree.engine.mssql'),    port: '1433' },
    { value: 'oracle',   label: t('tree.engine.oracle'),   port: '1521' },
  ]
  // Engine chips shown in the hero (brand-level, all supported drivers).
  const heroChips = ['PostgreSQL', 'MySQL', 'SQLite', 'MS SQL', 'Oracle']

  let name = $state('')
  let engine = $state('postgres')
  let host = $state('')
  let port = $state('5432')
  let database = $state('')
  let username = $state('')
  let password = $state('')
  let readOnly = $state(false)
  let showPw = $state(false)
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

  function pickEngine(e) {
    engine = e.value
    // Prefill the conventional port when adding (don't clobber an edit).
    if (!isEdit) port = e.port
  }

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

<div class="connect">
  <!-- Brand hero -->
  <aside class="connect__hero">
    <div class="connect__brand">
      <span class="logo-mark logo-mark--lg">A</span>
      <span class="connect__brandname">{t('brand')}</span>
    </div>
    <h1 class="connect__headline">A modern control plane for your databases.</h1>
    <p class="connect__lede">
      Connect to PostgreSQL, MySQL, SQLite, MS SQL and Oracle — one polished
      workspace. Read-replica aware, audit-logged, role-scoped.
    </p>
    <div class="connect__chips">
      {#each heroChips as c (c)}<span class="pill">{c}</span>{/each}
    </div>
  </aside>

  <!-- Credential form -->
  <section class="connect__panel">
    <form class="connect__form" onsubmit={(e) => { e.preventDefault(); save() }}>
      <header class="connect__formhead">
        <h2 class="connect__title">{isEdit ? t('conn.form.title.edit') : 'Connect to a server'}</h2>
        <p class="muted">Saved credentials are encrypted at rest under the panel key.</p>
      </header>

      <div class="field">
        <label class="field__label" for="cf-name">{t('conn.form.name')}</label>
        <input id="cf-name" class="input" bind:value={name} placeholder="Production replica" />
      </div>

      <div class="field">
        <span class="field__label">{t('conn.form.engine')}</span>
        <div class="seg" role="radiogroup" aria-label={t('conn.form.engine')}>
          {#each engines as e (e.value)}
            <button
              type="button"
              role="radio"
              aria-checked={engine === e.value}
              class="seg__btn {engine === e.value ? 'seg__btn--on' : ''}"
              onclick={() => pickEngine(e)}
            >{e.label}</button>
          {/each}
        </div>
      </div>

      <div class="connect__row">
        <div class="field connect__grow">
          <label class="field__label" for="cf-host">{t('conn.form.host')}</label>
          <input id="cf-host" class="input input--mono" bind:value={host} placeholder="db.internal" />
        </div>
        <div class="field connect__port">
          <label class="field__label" for="cf-port">{t('conn.form.port')}</label>
          <input id="cf-port" class="input input--mono" bind:value={port} />
        </div>
      </div>

      <div class="connect__row">
        <div class="field connect__grow">
          <label class="field__label" for="cf-user">{t('conn.form.username')}</label>
          <input id="cf-user" class="input input--mono" bind:value={username} />
        </div>
        <div class="field connect__grow">
          <label class="field__label" for="cf-pw">{t('conn.form.password')}</label>
          <div class="input-affix">
            <input
              id="cf-pw"
              class="input"
              type={showPw ? 'text' : 'password'}
              bind:value={password}
              autocomplete="off"
            />
            <button
              type="button"
              class="input-affix__btn"
              aria-label={showPw ? 'Hide password' : 'Show password'}
              aria-pressed={showPw}
              onclick={() => (showPw = !showPw)}
            >
              <Icon name={showPw ? 'eyeOff' : 'eye'} size={16} />
            </button>
          </div>
        </div>
      </div>

      <div class="field">
        <label class="field__label" for="cf-db">{t('conn.form.database')} <span class="faint">(optional)</span></label>
        <input id="cf-db" class="input input--mono" bind:value={database} />
      </div>

      <label class="connect__check">
        <input type="checkbox" bind:checked={readOnly} />
        <span>{t('conn.form.readonly')}</span>
      </label>

      {#if error}
        <div class="u-form-error" role="alert">{error}</div>
      {/if}

      <div class="connect__actions">
        <Btn variant="primary" type="submit" loading={saving} onclick={save}>
          <Icon name="lock" size={15} /> {isEdit ? t('conn.form.save') : 'Connect'}
        </Btn>
        <Btn variant="ghost" onclick={() => navigate('/connections')}>{t('conn.form.cancel')}</Btn>
      </div>
    </form>
  </section>
</div>
