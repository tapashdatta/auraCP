<script>
  import { inferWarningSeverity, maxWarningSeverity } from '../../sqlEditor/explainFormat.js'

  /**
   * @type {{
   *   warnings?: string[],
   *   onDismiss?: () => void,
   * }}
   */
  let { warnings = [], onDismiss } = $props()

  // FIX DC-7 (PR #14.5): the dismiss model is per-MOUNT — dismissing
  // hides the banner for the lifetime of this inspector mount only. A
  // Re-run that produces fresh warnings, a route revisit, or any other
  // remount surfaces the banner again. This is intentional: warnings
  // are tied to the current plan, so per-session persistence would
  // hide the banner for a NEW plan that happened to carry the same
  // strings. The earlier ambiguity ("is dismissed remembered across
  // navigation?") is now resolved by this comment + the fact that a
  // route remount drops local state.
  let dismissed = $state(false)

  // FIX CORR-7 / A11Y-12 (PR #14.5): split warnings by inferred
  // severity (see explainFormat.inferWarningSeverity) so critical
  // entries render in an `alert` role and visually distinct
  // surface, while informational entries stay in the polite `status`
  // role. The classification is a client-side shim today — a future
  // server-side `severity` field will replace the call but the UI
  // contract stays the same.
  const classified = $derived.by(() => {
    const out = { critical: [], warning: [], info: [] }
    for (const w of warnings) out[inferWarningSeverity(w)].push(w)
    return out
  })
  const topSeverity = $derived(maxWarningSeverity(warnings))
  // role=alert is for content that needs immediate AT announcement;
  // role=status announces politely on next idle. Critical warnings
  // (missing data, truncated tree) MUST interrupt; informational
  // notes can wait.
  const bannerRole = $derived(topSeverity === 'critical' ? 'alert' : 'status')
</script>

{#if warnings.length > 0 && !dismissed}
  <div
    class="warning-banner warning-banner--{topSeverity}"
    data-severity={topSeverity}
    role={bannerRole}
    aria-live={topSeverity === 'critical' ? 'assertive' : 'polite'}
  >
    <div class="warning-banner__body">
      {#if classified.critical.length > 0}
        <ul class="warning-banner__list warning-banner__list--critical" aria-label="Critical warnings">
          {#each classified.critical as w (w)}
            <li class="warning-banner__item warning-banner__item--critical">
              <span class="warning-banner__sev" aria-hidden="true">!</span>
              <span class="warning-banner__msg">{w}</span>
            </li>
          {/each}
        </ul>
      {/if}
      {#if classified.warning.length > 0}
        <ul class="warning-banner__list warning-banner__list--warning" aria-label="Warnings">
          {#each classified.warning as w (w)}
            <li class="warning-banner__item warning-banner__item--warning">
              <span class="warning-banner__sev" aria-hidden="true">⚠</span>
              <span class="warning-banner__msg">{w}</span>
            </li>
          {/each}
        </ul>
      {/if}
      {#if classified.info.length > 0}
        <ul class="warning-banner__list warning-banner__list--info" aria-label="Notes">
          {#each classified.info as w (w)}
            <li class="warning-banner__item warning-banner__item--info">
              <span class="warning-banner__sev" aria-hidden="true">i</span>
              <span class="warning-banner__msg">{w}</span>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
    <button
      class="warning-banner__close"
      type="button"
      onclick={() => { dismissed = true; onDismiss?.() }}
      aria-label="Dismiss warnings (per-mount; new plans re-surface the banner)"
      title="Dismiss"
    >×</button>
  </div>
{/if}
