<script>
  /**
   * @type {{ raw: any }}
   */
  let { raw } = $props()

  const text = $derived.by(() => {
    if (raw == null) return ''
    if (typeof raw === 'string') {
      // Server may send base64-encoded json.RawMessage; try to decode +
      // pretty-print. If decoding fails, fall back to the raw string.
      try {
        const decoded = (typeof atob === 'function') ? atob(raw) : raw
        const parsed = JSON.parse(decoded)
        return JSON.stringify(parsed, null, 2)
      } catch {
        try {
          const parsed = JSON.parse(raw)
          return JSON.stringify(parsed, null, 2)
        } catch {
          return String(raw)
        }
      }
    }
    try { return JSON.stringify(raw, null, 2) } catch { return String(raw) }
  })

  let copyAck = $state(false)
  function copy() {
    try {
      navigator.clipboard?.writeText(text)
      copyAck = true
      setTimeout(() => { copyAck = false }, 1000)
    } catch { /* ignore */ }
  }
</script>

<div class="raw-plan">
  <div class="raw-plan__bar">
    <span class="raw-plan__title">Raw engine plan</span>
    <button type="button" class="raw-plan__copy" onclick={copy}>{copyAck ? 'Copied' : 'Copy'}</button>
  </div>
  {#if text}
    <pre class="code raw-plan__pre">{text}</pre>
  {:else}
    <p class="raw-plan__empty">No raw payload available.</p>
  {/if}
</div>
