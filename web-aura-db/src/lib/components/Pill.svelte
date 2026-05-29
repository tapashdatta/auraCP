<script>
  /** @type {{ tone?: 'neutral'|'accent'|'success'|'warning'|'danger'|'info', children?: any }} */
  let { tone = 'neutral', children } = $props()
</script>

<span class="pill {tone === 'neutral' ? '' : 'pill--' + tone}">{@render children?.()}</span>
