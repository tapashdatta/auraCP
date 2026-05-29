<script>
  /** @type {{ rowsAffected?: number, lastInsertId?: number, durationMs?: number, message?: string }} */
  let { rowsAffected = 0, lastInsertId = 0, durationMs = 0, message = '' } = $props()
</script>

<div class="msg">
  {#if message}<div class="msg__line">{message}</div>{/if}
  <div class="msg__line"><span class="num">{rowsAffected}</span> row(s) affected</div>
  {#if lastInsertId > 0}
    <div class="msg__line">last insert id: <span class="num">{lastInsertId}</span></div>
  {/if}
  <div class="msg__line msg__line--meta">{durationMs}ms</div>
</div>
