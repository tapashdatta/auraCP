<script>
  // FIX (PR #11 dc-1): added `connecting` state. The pulse animation was
  // dead CSS until something actually toggled --pulse — `connecting` now
  // turns it on by default. Callers can still pass pulse={true} to force
  // the pulse on other states (e.g. brief ok→pulse on reconnect).
  /** @type {{ state?: 'ok'|'warn'|'down'|'idle'|'connecting', pulse?: boolean, title?: string }} */
  let { state = 'idle', pulse = false, title = '' } = $props()
  const dotClass = $derived(`dot dot--${state} ${(pulse || state === 'connecting') ? 'dot--pulse' : ''}`)
</script>

<span class={dotClass} {title} aria-label={title || undefined}></span>
