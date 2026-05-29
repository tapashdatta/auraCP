<script>
  // ToastRegion — single global live-region renderer for the toast bus.
  // Mounted once at the app root so any module can pushToast() without
  // owning DOM. role=alert on danger toasts (assertive) so AT interrupts
  // immediately; role=status (polite) elsewhere.
  import { toasts, dismissToast } from '../toastBus.svelte.js'
</script>

<div class="toast-region" aria-label="Notifications" role="region">
  {#each toasts.list as t (t.id)}
    <div
      class="toast toast--{t.tone}"
      role={t.tone === 'danger' ? 'alert' : 'status'}
      aria-live={t.tone === 'danger' ? 'assertive' : 'polite'}
      aria-atomic="true"
    >
      <span class="toast__msg">{t.message}</span>
      <button
        class="toast__close"
        type="button"
        aria-label="Dismiss notification"
        onclick={() => dismissToast(t.id)}
      >×</button>
    </div>
  {/each}
</div>

<style>
  .toast-region {
    position: fixed;
    right: 16px;
    bottom: 36px;
    z-index: 80;
    display: flex;
    flex-direction: column;
    gap: 6px;
    pointer-events: none;
    max-width: min(420px, calc(100vw - 32px));
  }
  .toast {
    pointer-events: auto;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--border-strong);
    background: var(--surface-elev);
    color: var(--text);
    font-size: var(--fs-body);
    border-radius: 4px;
    box-shadow: 0 8px 24px rgba(0,0,0,.3);
  }
  .toast__msg { flex: 1; min-width: 0; }
  .toast--info    { border-color: var(--info);    }
  .toast--success { border-color: var(--success); }
  .toast--warning { border-color: var(--warning); }
  .toast--danger  { border-color: var(--danger);  }
  .toast__close {
    color: var(--text-mute);
    padding: 0 4px;
    font-size: 16px;
    line-height: 1;
    background: transparent;
    border: 0;
    cursor: pointer;
  }
  .toast__close:hover { color: var(--text); }
</style>
