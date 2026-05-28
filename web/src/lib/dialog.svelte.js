// v0.2.31: app-wide dialog system. Replaces the browser's native confirm /
// alert / prompt — those:
//   1. block the entire JS event loop
//   2. look like an OS dialog (jarring against a polished SPA)
//   3. can't take rich content (lists, action chips, monospace highlights)
//
// Pattern: a single $state object holds the currently-open dialog; helpers
// (confirmDialog / alertDialog / promptDialog) push state + return a Promise
// that resolves when the user clicks an action. DialogHost.svelte renders
// the modal once at the app root and listens on $state.open.

export const dialog = $state({
  open: false,
  kind: 'confirm',           // 'confirm' | 'alert' | 'prompt'
  title: '',
  message: '',
  confirmText: 'Confirm',
  cancelText: 'Cancel',
  danger: false,
  value: '',                 // prompt input
  placeholder: '',           // prompt placeholder
  resolve: null,             // promise resolve fn (cleared on close)
})

// confirmDialog → Promise<boolean>
export function confirmDialog({ title = 'Confirm', message = '', confirmText = 'Confirm', cancelText = 'Cancel', danger = false } = {}) {
  return new Promise((resolve) => {
    Object.assign(dialog, {
      open: true, kind: 'confirm', title, message,
      confirmText, cancelText, danger, value: '', placeholder: '',
      resolve,
    })
  })
}

// promptDialog → Promise<string | null>
export function promptDialog({ title = 'Input', message = '', defaultValue = '', confirmText = 'OK', cancelText = 'Cancel', placeholder = '', danger = false } = {}) {
  return new Promise((resolve) => {
    Object.assign(dialog, {
      open: true, kind: 'prompt', title, message,
      confirmText, cancelText, danger,
      value: defaultValue, placeholder,
      resolve,
    })
  })
}

// alertDialog → Promise<void> (resolves on OK; same as confirm but one button)
export function alertDialog({ title = 'Notice', message = '', confirmText = 'OK', danger = false } = {}) {
  return new Promise((resolve) => {
    Object.assign(dialog, {
      open: true, kind: 'alert', title, message,
      confirmText, cancelText: '', danger, value: '', placeholder: '',
      resolve,
    })
  })
}

export function closeDialog(result) {
  if (dialog.resolve) dialog.resolve(result)
  dialog.open = false
  dialog.resolve = null
}
