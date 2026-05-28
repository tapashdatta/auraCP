<script>
  import { dialog, closeDialog } from '../dialog.svelte.js'

  function onCancel() {
    // confirm → false, prompt → null, alert → undefined (treated as ok)
    closeDialog(dialog.kind === 'confirm' ? false : (dialog.kind === 'prompt' ? null : undefined))
  }
  function onConfirm() {
    closeDialog(dialog.kind === 'prompt' ? dialog.value : true)
  }
  function onKey(e) {
    if (!dialog.open) return
    if (e.key === 'Escape') { e.preventDefault(); onCancel() }
    // Enter confirms on alert + confirm; prompt's <input> handles its own
    // Enter (so multi-line paste doesn't trigger an early confirm).
    if (e.key === 'Enter' && dialog.kind !== 'prompt') { e.preventDefault(); onConfirm() }
  }

  // Render-time autofocus: when the dialog opens, focus the input (prompt)
  // or the primary action (confirm/alert). $effect runs after the DOM is
  // updated, so the refs are live.
  let inputEl = $state(null)
  let primaryEl = $state(null)
  $effect(() => {
    if (!dialog.open) return
    requestAnimationFrame(() => {
      if (dialog.kind === 'prompt' && inputEl) { inputEl.focus(); inputEl.select() }
      else if (primaryEl) primaryEl.focus()
    })
  })
</script>

<svelte:window onkeydown={onKey} />

{#if dialog.open}
  <div class="dlg-back" onclick={onCancel} role="presentation"></div>
  <div class="dlg-card" role="dialog" aria-modal="true" aria-label={dialog.title}>
    {#if dialog.danger}
      <div class="dlg-ic dlg-ic-danger" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
      </div>
    {:else if dialog.kind === 'prompt'}
      <div class="dlg-ic dlg-ic-info" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>
      </div>
    {:else}
      <div class="dlg-ic dlg-ic-info" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
      </div>
    {/if}

    <div class="dlg-body">
      <h3>{dialog.title}</h3>
      {#if dialog.message}
        <!-- Use white-space:pre-wrap via CSS so message can include newlines -->
        <p class="dlg-msg">{dialog.message}</p>
      {/if}
      {#if dialog.kind === 'prompt'}
        <input bind:this={inputEl} class="input dlg-input" bind:value={dialog.value} placeholder={dialog.placeholder}
               onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); onConfirm() } }}>
      {/if}
    </div>

    <div class="dlg-actions">
      {#if dialog.kind !== 'alert'}
        <button type="button" class="btn btn-ghost" onclick={onCancel}>{dialog.cancelText || 'Cancel'}</button>
      {/if}
      <button bind:this={primaryEl} type="button"
              class="btn {dialog.danger ? 'btn-danger' : 'btn-primary'}"
              onclick={onConfirm}>{dialog.confirmText || 'OK'}</button>
    </div>
  </div>
{/if}

<style>
  .dlg-back{position:fixed;inset:0;background:rgba(8,10,16,.55);backdrop-filter:blur(6px);z-index:200;animation:dlg-fade .14s ease}
  .dlg-card{position:fixed;top:50%;left:50%;transform:translate(-50%,-50%);width:min(440px, 92vw);background:var(--surface-0);border:1px solid var(--line-2);border-radius:16px;box-shadow:0 24px 64px rgba(0,0,0,.42), 0 1px 0 rgba(255,255,255,.04) inset;display:flex;flex-direction:column;gap:14px;padding:22px 22px 18px;z-index:201;animation:dlg-pop .16s cubic-bezier(.2,.8,.3,1.1)}

  .dlg-ic{width:38px;height:38px;border-radius:10px;display:grid;place-items:center;flex:none}
  .dlg-ic svg{width:18px;height:18px}
  .dlg-ic-info{background:color-mix(in srgb, var(--info) 16%, transparent);color:var(--info);border:1px solid color-mix(in srgb, var(--info) 32%, transparent)}
  .dlg-ic-danger{background:color-mix(in srgb, var(--down) 16%, transparent);color:var(--down);border:1px solid color-mix(in srgb, var(--down) 32%, transparent)}

  .dlg-body{display:flex;flex-direction:column;gap:8px}
  .dlg-body h3{font-size:16px;font-weight:600;line-height:1.3;margin:0;color:var(--txt);font-family:var(--fs-ui)}
  .dlg-msg{font-size:13px;color:var(--txt-2);line-height:1.55;margin:0;white-space:pre-wrap}
  .dlg-input{margin-top:6px;font-size:13.5px}

  .dlg-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:6px}

  @keyframes dlg-fade{from{opacity:0}to{opacity:1}}
  @keyframes dlg-pop{from{opacity:0;transform:translate(-50%,-50%) scale(.96)}to{opacity:1;transform:translate(-50%,-50%) scale(1)}}
</style>
