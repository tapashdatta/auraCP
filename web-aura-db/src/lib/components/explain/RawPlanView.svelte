<script>
  /**
   * @type {{ raw: any }}
   */
  let { raw } = $props()

  // FIX INT-11 (PR #14.5): the previous decode tried base64 first,
  // then JSON-parse, then JSON-parse-again — three error swallowers
  // chained together. In practice the server sends raw as either:
  //   - an object (json.RawMessage delivered as a parsed JS object), or
  //   - a base64 string (Go's json.RawMessage when the JSON contains
  //     bytes the wire-codec routed through encoding/base64).
  // We collapse the path to: object → pretty-print; string → try
  // base64 once, then JSON-parse once, fall back to the raw string.
  // No double JSON.parse — that branch only fires when atob silently
  // returns non-JSON, which never happens for valid base64-of-JSON.
  function decode(input) {
    if (input == null) return ''
    if (typeof input !== 'string') {
      try { return JSON.stringify(input, null, 2) } catch { return String(input) }
    }
    // String path. Try base64 → JSON, else treat as JSON-string, else
    // return verbatim.
    if (typeof atob === 'function') {
      try {
        const decoded = atob(input)
        // Guard: only parse if the decoded payload smells like JSON
        // — otherwise atob can succeed on a string that happens to be
        // valid base64 chars and we'd parse-fail noisily.
        const trimmed = decoded.trim()
        if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
          return JSON.stringify(JSON.parse(decoded), null, 2)
        }
      } catch { /* fall through */ }
    }
    try { return JSON.stringify(JSON.parse(input), null, 2) } catch { return input }
  }

  // FIX CORR-11 (PR #14.5): defer the (potentially 1MB+) decode +
  // stringify pass until the user actually views the RAW tab. The
  // component is already lazy-mounted by ExplainInspectorScreen, but
  // a mount alone was paying the cost on every switch; we now also
  // skip the work when the input is huge by surfacing a
  // "show anyway" affordance for plans over the threshold.
  const RAW_SOFT_LIMIT = 256 * 1024  // 256 KB raw input → instant
  const inputSize = $derived.by(() => {
    if (raw == null) return 0
    if (typeof raw === 'string') return raw.length
    try { return JSON.stringify(raw).length } catch { return 0 }
  })
  let forceShow = $state(false)
  const showText = $derived(inputSize < RAW_SOFT_LIMIT || forceShow)
  const text = $derived(showText ? decode(raw) : '')

  let copyAck = $state(false)
  function copy() {
    try {
      navigator.clipboard?.writeText(text || decode(raw))
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
  {#if !showText}
    <p class="raw-plan__empty">
      Raw plan is large ({Math.round(inputSize / 1024)} KB). Rendering may freeze the tab briefly.
      <button type="button" class="raw-plan__copy" onclick={() => { forceShow = true }}>Show anyway</button>
    </p>
  {:else if text}
    <pre class="code raw-plan__pre">{text}</pre>
  {:else}
    <p class="raw-plan__empty">No raw payload available.</p>
  {/if}
</div>
