<script>
  let { on = false } = $props()
  let state = $state(on)
</script>

<div class="toggle" class:on={state} onclick={() => state = !state} role="switch" aria-checked={state} tabindex="0"></div>
