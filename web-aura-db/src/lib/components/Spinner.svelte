<script>
  /** @type {{ size?: number }} */
  let { size = 10 } = $props()
</script>

<span class="spinner" style="width:{size}px;height:{size}px"></span>
