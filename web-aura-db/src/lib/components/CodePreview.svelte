<script>
  // Language prop is reserved for highlighting in PR #13. Accepted but unused.
  /** @type {{ code?: string, language?: string }} */
  // eslint-disable-next-line no-unused-vars
  let { code = '', language: _language = 'sql' } = $props()
</script>

<pre class="code">{code}</pre>
