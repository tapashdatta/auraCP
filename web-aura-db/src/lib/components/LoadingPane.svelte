<script>
  import Spinner from './Spinner.svelte'
  import { t } from '../strings.js'

  /** @type {{ label?: string }} */
  let { label = t('common.loading') } = $props()
</script>

<div class="loading-pane">
  <Spinner size={16} />
  <span>{label}</span>
</div>
