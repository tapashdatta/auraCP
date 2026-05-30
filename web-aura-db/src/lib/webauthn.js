// v0.3.2-D: WebAuthn / FIDO2 client wrappers.
//
// Browsers expose two ceremonies:
//
//   navigator.credentials.create() — registration (a fresh credential
//                                    is bound to the RP for the user).
//   navigator.credentials.get()    — assertion (the user proves they
//                                    possess a previously-registered
//                                    credential).
//
// The server (cmd/aura-db/webauthn.go) returns the WebAuthn options as
// JSON in our own shape: the Base64URL-encoded fields the spec calls
// for are emitted by the go-webauthn library as already-encoded
// strings (because of EncodeUserIDAsString=true for user.id). For the
// rest (challenge, credential id, authenticator response fields) the
// browser API expects ArrayBuffer — we convert via b64urlToBuf /
// bufToB64url at the boundary.

// ---------- base64url helpers ----------

/** @param {string} s */
function b64urlToBuf(s) {
  // Pad so atob is happy.
  const pad = '='.repeat((4 - (s.length % 4)) % 4)
  const b64 = (s + pad).replaceAll('-', '+').replaceAll('_', '/')
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

/** @param {ArrayBuffer|Uint8Array} buf */
function bufToB64url(buf) {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  let bin = ''
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
  return btoa(bin).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '')
}

// ---------- options coercion ----------
// The server hands us the PublicKeyCredentialCreationOptions /
// PublicKeyCredentialRequestOptions verbatim from the library. We
// coerce the few fields that must be ArrayBuffers per spec.

function coerceCreateOptions(opts) {
  const out = { ...opts }
  out.challenge = b64urlToBuf(opts.challenge)
  // user.id is encoded as a UTF-8 string on the server side
  // (EncodeUserIDAsString=true) — wrap it in a buffer the browser can
  // hand back as the user handle.
  if (out.user && typeof out.user.id === 'string') {
    const enc = new TextEncoder().encode(out.user.id)
    out.user = { ...out.user, id: enc.buffer }
  }
  if (Array.isArray(opts.excludeCredentials)) {
    out.excludeCredentials = opts.excludeCredentials.map((c) => ({
      ...c,
      id: b64urlToBuf(c.id),
    }))
  }
  return out
}

function coerceRequestOptions(opts) {
  const out = { ...opts }
  out.challenge = b64urlToBuf(opts.challenge)
  if (Array.isArray(opts.allowCredentials)) {
    out.allowCredentials = opts.allowCredentials.map((c) => ({
      ...c,
      id: b64urlToBuf(c.id),
    }))
  }
  return out
}

// ---------- ceremonies ----------

/**
 * supported reports whether the browser exposes the WebAuthn API.
 * Callers use this to decide whether to render the security-key
 * affordance at all.
 */
export function supported() {
  return typeof window !== 'undefined'
    && typeof window.PublicKeyCredential !== 'undefined'
    && typeof navigator !== 'undefined'
    && typeof navigator.credentials !== 'undefined'
    && typeof navigator.credentials.create === 'function'
    && typeof navigator.credentials.get === 'function'
}

/**
 * register asks the browser to mint a fresh credential, then returns
 * the serialized response shape the server expects in /register/finish.
 *
 * @param {object} publicKey  PublicKeyCredentialCreationOptions from
 *                            the server's /register/begin response.
 */
export async function register(publicKey) {
  const cred = await navigator.credentials.create({ publicKey: coerceCreateOptions(publicKey) })
  if (!cred) throw new Error('navigator.credentials.create returned null')
  const att = cred.response
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      attestationObject: bufToB64url(att.attestationObject),
      clientDataJSON: bufToB64url(att.clientDataJSON),
      transports: typeof att.getTransports === 'function' ? att.getTransports() : [],
    },
    clientExtensionResults: typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
    authenticatorAttachment: cred.authenticatorAttachment ?? null,
  }
}

/**
 * authenticate asks the browser to prove possession of an existing
 * credential, returning the serialized shape the server expects in
 * /login/finish or in the {webauthn:{...}} branch of /step-up/verify.
 *
 * @param {object} publicKey  PublicKeyCredentialRequestOptions from
 *                            the server's /login/begin response.
 */
export async function authenticate(publicKey) {
  const cred = await navigator.credentials.get({ publicKey: coerceRequestOptions(publicKey) })
  if (!cred) throw new Error('navigator.credentials.get returned null')
  const a = cred.response
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: bufToB64url(a.authenticatorData),
      clientDataJSON: bufToB64url(a.clientDataJSON),
      signature: bufToB64url(a.signature),
      userHandle: a.userHandle ? bufToB64url(a.userHandle) : null,
    },
    clientExtensionResults: typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
    authenticatorAttachment: cred.authenticatorAttachment ?? null,
  }
}

// ---------- high-level helpers ----------
// These chain begin/finish against a thin fetch helper so callers in
// AccountScreen / step-up modal do not have to redo the boilerplate.
// They take a `request` function with the same shape as
// lib/api.js#request so tests can swap in a mock.

/**
 * registerCredential runs the full /webauthn/register/{begin,finish}
 * ceremony. Returns { credentialId, name } on success.
 *
 * @param {(path: string, init?: object)=>Promise<any>} request
 * @param {string} name human-readable label the user typed.
 */
export async function registerCredential(request, name) {
  const begin = await request('/webauthn/register/begin', { method: 'POST', body: {} })
  const opts = begin.publicKey || begin.publicKeyCredentialCreationOptions?.publicKey
              || begin.publicKeyCredentialCreationOptions
  if (!opts) throw new Error('register/begin: missing publicKey')
  const cred = await register(opts)
  return await request('/webauthn/register/finish', {
    method: 'POST',
    body: { challenge_id: begin.challenge_id, name, credential: cred },
  })
}

/**
 * loginWithWebAuthn runs the full /webauthn/login/{begin,finish}
 * ceremony plus the password the server still requires. Returns the
 * /login/finish response on success.
 *
 * @param {(path: string, init?: object)=>Promise<any>} request
 * @param {{username: string, password: string}} creds
 */
export async function loginWithWebAuthn(request, creds) {
  const begin = await request('/webauthn/login/begin', {
    method: 'POST',
    body: { username: creds.username },
  })
  const opts = begin.publicKey || begin.publicKeyCredentialRequestOptions?.publicKey
              || begin.publicKeyCredentialRequestOptions
  if (!opts) throw new Error('login/begin: missing publicKey')
  const assertion = await authenticate(opts)
  return await request('/webauthn/login/finish', {
    method: 'POST',
    body: {
      username: creds.username,
      password: creds.password,
      challenge_id: begin.challenge_id,
      assertion,
    },
  })
}

/**
 * stepUpWithWebAuthn runs an assertion ceremony and posts the result
 * to /step-up/verify using the {webauthn:{...}} discriminator. The
 * action argument is the dbadmin.Action class the caller wants to
 * unlock (e.g., "conn.delete").
 *
 * @param {(path: string, init?: object)=>Promise<any>} request
 * @param {string} username   currently-signed-in username, for the
 *                            login/begin call's authenticator
 *                            allowlist.
 * @param {string} action     dbadmin Action class to step-up for.
 */
export async function stepUpWithWebAuthn(request, username, action) {
  const begin = await request('/webauthn/login/begin', {
    method: 'POST',
    body: { username },
  })
  const opts = begin.publicKey || begin.publicKeyCredentialRequestOptions?.publicKey
              || begin.publicKeyCredentialRequestOptions
  if (!opts) throw new Error('login/begin: missing publicKey')
  const assertion = await authenticate(opts)
  return await request('/step-up/verify', {
    method: 'POST',
    body: {
      jti: begin.challenge_id,
      assertion: 'webauthn',
      action,
      webauthn: { challenge_id: begin.challenge_id, assertion },
    },
  })
}
