<script>
  import { toasts, dismissToast } from '../toast.svelte.js'
</script>

{#if toasts.length > 0}
  <!-- Top-center stack, mirrors macOS notification flow and stays out of the
       way of bottom-of-page action bars. Clicking a toast dismisses early. -->
  <div class="toast-stack" role="status" aria-live="polite" aria-atomic="true">
    {#each toasts as t (t.id)}
      <button type="button" class="toast toast-{t.kind}" onclick={() => dismissToast(t.id)} aria-label="Dismiss {t.message}">
        <span class="toast-ic" aria-hidden="true">
          {#if t.kind === 'success'}
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
          {:else if t.kind === 'error'}
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>
          {:else if t.kind === 'warn'}
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
          {:else}
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
          {/if}
        </span>
        <span class="toast-msg">{t.message}</span>
      </button>
    {/each}
  </div>
{/if}

<style>
  .toast-stack{position:fixed;top:74px;left:50%;transform:translateX(-50%);display:flex;flex-direction:column;gap:10px;z-index:300;pointer-events:none}
  .toast{pointer-events:auto;display:flex;align-items:center;gap:10px;min-width:240px;max-width:480px;padding:11px 16px 11px 14px;border-radius:12px;background:var(--surface-0);border:1px solid var(--line-2);color:var(--txt);font-size:13px;line-height:1.4;box-shadow:0 14px 28px -10px rgba(0,0,0,.3), 0 1px 0 rgba(255,255,255,.04) inset;cursor:pointer;text-align:left;animation:toast-slide-in .2s cubic-bezier(.2,.8,.2,1)}
  .toast:hover{transform:translateY(-1px)}
  .toast-ic{flex:none;display:grid;place-items:center;width:22px;height:22px;border-radius:50%}
  .toast-ic svg{width:14px;height:14px}
  .toast-msg{flex:1;word-break:break-word}

  .toast-success .toast-ic{background:color-mix(in srgb, var(--up, #18a86b) 18%, transparent);color:var(--up, #18a86b)}
  .toast-success{border-color:color-mix(in srgb, var(--up, #18a86b) 32%, var(--line-2))}

  .toast-error .toast-ic{background:color-mix(in srgb, var(--down) 18%, transparent);color:var(--down)}
  .toast-error{border-color:color-mix(in srgb, var(--down) 38%, var(--line-2))}

  .toast-warn .toast-ic{background:color-mix(in srgb, var(--warn) 18%, transparent);color:var(--warn)}
  .toast-warn{border-color:color-mix(in srgb, var(--warn) 38%, var(--line-2))}

  .toast-info .toast-ic{background:color-mix(in srgb, var(--info) 18%, transparent);color:var(--info)}

  @keyframes toast-slide-in{
    from{opacity:0;transform:translateY(-10px) scale(.96)}
    to{opacity:1;transform:translateY(0) scale(1)}
  }

  @media (max-width:520px){
    .toast-stack{top:64px;left:14px;right:14px;transform:none}
    .toast{min-width:0;max-width:none;font-size:12.5px}
  }
</style>
