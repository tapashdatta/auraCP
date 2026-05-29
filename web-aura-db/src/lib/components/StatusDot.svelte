<script>
  /** @type {{ state?: 'ok'|'warn'|'down'|'idle', pulse?: boolean, title?: string }} */
  let { state = 'idle', pulse = false, title = '' } = $props()
</script>

<span class="dot dot--{state} {pulse ? 'dot--pulse' : ''}" {title}></span>
