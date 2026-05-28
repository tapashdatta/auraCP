<script>
  import { go, openCreate } from '../lib/store.svelte.js'
  import { siteTypes } from '../lib/data.js'
  import { brandIcons } from '../lib/icons.js'
</script>

<div class="wrap fade">
  <button type="button" class="back" onclick={() => go('sites')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites
  </button>
  <div class="chooser-head">
    <h1>What kind of site would you like to create?</h1>
    <p>Node.js included · automatic HTTPS · Cloudflare DNS-01 supported</p>
  </div>
  <div class="grid6">
    {#each siteTypes as t, i}
      <button type="button" class="site-card" style="animation-delay:{i * 0.03}s" onclick={() => openCreate(t.type)} aria-label="Create {t.name} site">
        <div class="ico brand">{@html brandIcons[t.type]}</div>
        <h3>{t.name}</h3>
        <p>{t.desc}</p>
        <div class="go">Create →</div>
      </button>
    {/each}
  </div>
</div>
