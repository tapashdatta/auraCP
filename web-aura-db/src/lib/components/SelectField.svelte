<script>
  /** @type {{
   *   value?: string,
   *   options?: Array<{value:string,label:string}>,
   *   label?: string,
   *   placeholder?: string,
   *   id?: string,
   * }} */
  let { value = $bindable(''), options = [], label, placeholder, id } = $props()
</script>

<div class="field">
  {#if label}<label class="field__label" for={id}>{label}</label>{/if}
  <select {id} class="select" bind:value>
    {#if placeholder}<option value="" disabled>{placeholder}</option>{/if}
    {#each options as o (o.value)}
      <option value={o.value}>{o.label}</option>
    {/each}
  </select>
</div>
