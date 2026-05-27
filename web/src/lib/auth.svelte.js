// Auth state + actions (Svelte 5 runes). Talks to the auracpd /api/auth/* endpoints.
import { apiFetch } from './api.js'

export const session = $state({
  user: null,
  loading: true,
  mfaRequired: false,
  error: '',
})

async function readJSON(r) {
  try { return await r.json() } catch { return {} }
}

export async function checkAuth() {
  session.loading = true
  try {
    const r = await apiFetch('/api/auth/me')
    session.user = r.ok ? (await readJSON(r)).user : null
  } catch {
    session.user = null
  } finally {
    session.loading = false
  }
}

export async function login(email, password) {
  session.error = ''
  const r = await apiFetch('/api/auth/login', {
    method: 'POST', body: JSON.stringify({ email, password }),
  })
  const d = await readJSON(r)
  if (!r.ok) { session.error = d.error || 'Login failed'; return false }
  if (d.mfaRequired) { session.mfaRequired = true; return 'mfa' }
  session.user = d.user; session.mfaRequired = false; return true
}

export async function verifyMfa(code) {
  session.error = ''
  const r = await apiFetch('/api/auth/mfa/verify', {
    method: 'POST', body: JSON.stringify({ code }),
  })
  const d = await readJSON(r)
  if (!r.ok) { session.error = d.error || 'Invalid code'; return false }
  session.user = d.user; session.mfaRequired = false; return true
}

export async function logout() {
  await apiFetch('/api/auth/logout', { method: 'POST' })
  session.user = null
  session.mfaRequired = false
}

// MFA enrollment helpers (used by the Account screen)
export async function mfaSetup() {
  const r = await apiFetch('/api/auth/mfa/setup', { method: 'POST' })
  return readJSON(r)
}
export async function mfaEnable(secret, code) {
  const r = await apiFetch('/api/auth/mfa/enable', {
    method: 'POST', body: JSON.stringify({ secret, code }),
  })
  if (r.ok) session.user = { ...session.user, mfaEnabled: true }
  return r.ok
}
export async function mfaDisable(code) {
  const r = await apiFetch('/api/auth/mfa/disable', {
    method: 'POST', body: JSON.stringify({ code }),
  })
  if (r.ok) session.user = { ...session.user, mfaEnabled: false }
  return r.ok
}
