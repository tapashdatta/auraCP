<script>
  /** @type {{
   *   value?: string,
   *   label?: string,
   *   placeholder?: string,
   *   mono?: boolean,
   *   error?: string,
   *   type?: string,
   *   flat?: boolean,
   *   id?: string,
   *   readonly?: boolean,
   * }} */
  let { value = $bindable(''), label, placeholder = '', mono = false, error = '', type = 'text', flat = false, id, readonly = false } = $props()
</script>

<div class="field">
  {#if label}<label class="field__label" for={id}>{label}</label>{/if}
  <input
    {id}
    {type}
    class="input {mono ? 'input--mono' : ''} {flat ? 'input--flat' : ''}"
    {placeholder}
    {readonly}
    bind:value
  />
  {#if error}<div class="field__error">{error}</div>{/if}
</div>
