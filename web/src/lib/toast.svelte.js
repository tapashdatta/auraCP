// v0.2.33: lightweight toast system. Used for transient feedback that
// doesn't need a button to dismiss — "Saved", "Database created",
// "Cron job added", etc. The inline `notice` strings still exist for
// contextual errors that need to persist near a form, but anything that
// fires + forgets becomes a toast.
//
// API: toast(message, { kind, duration }) — kind ∈ 'success' | 'info' | 'warn' | 'error'.
// Default duration is 4s; kind defaults to 'info'. Same-message dedup
// (clicking the same Save button twice doesn't stack two identical toasts).

export const toasts = $state([])
let nextId = 0

export function toast(message, opts = {}) {
  if (!message) return
  // Dedup: drop if the most recent toast has the same message + kind.
  const kind = opts.kind || 'info'
  const last = toasts[toasts.length - 1]
  if (last && last.message === message && last.kind === kind) return

  const id = ++nextId
  const duration = opts.duration ?? (kind === 'error' ? 6000 : 4000)
  const t = { id, message, kind, duration }
  toasts.push(t)
  setTimeout(() => dismissToast(id), duration)
}

export function dismissToast(id) {
  const idx = toasts.findIndex(x => x.id === id)
  if (idx >= 0) toasts.splice(idx, 1)
}

// Convenience helpers — read better at call sites than toast(x, {kind:'error'}).
export const toastSuccess = (m, o) => toast(m, { ...o, kind: 'success' })
export const toastError   = (m, o) => toast(m, { ...o, kind: 'error' })
export const toastWarn    = (m, o) => toast(m, { ...o, kind: 'warn' })
