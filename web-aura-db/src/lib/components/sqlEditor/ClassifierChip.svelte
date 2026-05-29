<script>
  import Pill from '../Pill.svelte'

  /** @type {{ klass?: string }} */
  let { klass = 'read' } = $props()

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
</script>

<span title="Statement class (server-classified)">
  <Pill tone={TONE[klass] || 'neutral'}>{LABEL[klass] || klass.toUpperCase()}</Pill>
</span>
