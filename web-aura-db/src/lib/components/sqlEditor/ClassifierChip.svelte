<script>
  import Pill from '../Pill.svelte'

  /** @type {{ klass?: string }} */
  let { klass = 'read' } = $props()

  // a11y-13 (early): FORBIDDEN and DANGEROUS both render on the danger
  // tone (red) so AT users + colour-blind users can't tell them apart
  // by hue. Prepend a 🔒 glyph (lock) to FORBIDDEN — it changes the
  // pill body, not the tone, so the semantic distinction shows up in
  // both visual and SR readouts (the `lock` word is announced).
  const TONE = {
    read: 'success',
    'write-row': 'warning',
    'write-row-mass': 'warning',
    ddl: 'info',
    dangerous: 'danger',
    forbidden: 'danger',
    unknown: 'neutral',
  }
  const LABEL = {
    read: 'READ',
    'write-row': 'WRITE',
    'write-row-mass': 'WRITE-MASS',
    ddl: 'DDL',
    dangerous: 'DANGEROUS',
    forbidden: 'FORBIDDEN',
    unknown: 'EMPTY',
  }
  const TITLE = {
    forbidden: 'Forbidden — refused before reaching the database',
    dangerous: 'Dangerous — runs but is destructive (no WHERE on UPDATE/DELETE, etc.)',
  }
</script>

<!-- Inline styles avoid the deeply-nested style-block edge case the
     vite-svelte preprocessor hits when SqlEditor imports a chip with
     a <style> block (see SqlEditor.test.js header note). -->
<span title={TITLE[klass] || 'Statement class (server-classified)'} data-klass={klass}>
  <Pill tone={TONE[klass] || 'neutral'}>
    {#if klass === 'forbidden'}<span aria-hidden="true" style="margin-right:2px;">🔒</span><span style="position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0;">lock </span>{/if}{LABEL[klass] || klass.toUpperCase()}
  </Pill>
</span>
