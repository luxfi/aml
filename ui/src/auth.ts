// Sign-in against the brand's IAM, as a public client with PKCE.
//
// This bundle holds no secret and cannot: it is a static file anyone may read,
// so anything embedded in it is published. PKCE is what replaces the secret —
// the authorization code is bound to a verifier this tab generated and never
// transmitted until the exchange, so a code intercepted in the redirect cannot
// be spent by whoever intercepted it (RFC 7636).
//
// The issuer is NOT configured here. It comes from the API's own
// /v1/aml/config, which derives it from the Host the request arrived on — the
// same resolution the engine's auth path uses to decide whose signatures it
// will trust. One resolution, one answer: a console that sent users to an
// issuer the engine does not trust would obtain tokens the engine must refuse.

import { config } from './config'

/** Where the token lives. sessionStorage: per tab, gone when the tab closes. */
const store = window.sessionStorage
const keyToken = 'aml.token'
const keyVerifier = 'aml.pkce'
const keyState = 'aml.state'
const keyReturn = 'aml.return'

export type Session = {
  access: string
  refresh?: string
  /** Epoch ms this access token stops being accepted. */
  expires: number
  idToken?: string
}

/** Claims read for display only. The server decides what a token may do. */
export type Who = {
  /** IAM's `owner` claim: the org this token acts for. */
  org: string
  name: string
}

function random(bytes: number): string {
  const b = new Uint8Array(bytes)
  crypto.getRandomValues(b)
  return base64url(b)
}

function base64url(b: Uint8Array): string {
  let s = ''
  for (const byte of b) s += String.fromCharCode(byte)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

async function challenge(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))
  return base64url(new Uint8Array(digest))
}

function redirectURI(): string {
  return `${window.location.origin}/callback`
}

export function session(): Session | null {
  const raw = store.getItem(keyToken)
  if (!raw) return null
  try {
    const s = JSON.parse(raw) as Session
    return s.access ? s : null
  } catch {
    return null
  }
}

function keep(s: Session) {
  store.setItem(keyToken, JSON.stringify(s))
}

export function forget() {
  store.removeItem(keyToken)
  store.removeItem(keyVerifier)
  store.removeItem(keyState)
}

/**
 * who reads the display identity out of the access token.
 *
 * Decoding a JWT in a browser proves nothing — the signature is not checked
 * here and could not usefully be, because the party that must check it is the
 * server holding the data. This is a name in a corner of the screen. Every
 * authorisation decision in this product is the engine's.
 */
export function who(s: Session): Who {
  try {
    const [, payload] = s.access.split('.')
    const json = JSON.parse(
      decodeURIComponent(
        atob(payload.replace(/-/g, '+').replace(/_/g, '/'))
          .split('')
          .map((c) => '%' + c.charCodeAt(0).toString(16).padStart(2, '0'))
          .join(''),
      ),
    ) as Record<string, unknown>
    const name =
      (json.name as string) || (json.preferred_username as string) || (json.sub as string) || ''
    return { org: (json.owner as string) || '', name }
  } catch {
    return { org: '', name: '' }
  }
}

/** Begin sign-in. Returns only by navigating away. */
export async function login(issuer: string, returnTo?: string): Promise<never> {
  const verifier = random(48)
  const state = random(16)
  store.setItem(keyVerifier, verifier)
  store.setItem(keyState, state)
  store.setItem(keyReturn, returnTo ?? window.location.pathname + window.location.search)

  const q = new URLSearchParams({
    client_id: config().clientId,
    response_type: 'code',
    redirect_uri: redirectURI(),
    scope: 'openid profile email',
    state,
    code_challenge: await challenge(verifier),
    code_challenge_method: 'S256',
  })
  window.location.assign(`${issuer}/v1/iam/oauth/authorize?${q}`)
  return new Promise<never>(() => {})
}

/**
 * Finish sign-in from the redirect. Returns where to go next.
 *
 * The state is checked before the code is spent, and both single-use values are
 * cleared whatever the outcome, so a replayed callback URL has nothing left to
 * exchange.
 */
export async function callback(issuer: string, search: string): Promise<string> {
  const params = new URLSearchParams(search)
  const verifier = store.getItem(keyVerifier)
  const expected = store.getItem(keyState)
  const back = store.getItem(keyReturn) || '/'
  store.removeItem(keyVerifier)
  store.removeItem(keyState)
  store.removeItem(keyReturn)

  const err = params.get('error')
  if (err) throw new Error(params.get('error_description') || err)
  const code = params.get('code')
  if (!code) throw new Error('the sign-in redirect carried no code')
  if (!verifier) throw new Error('this tab did not start this sign-in')
  if (!expected || params.get('state') !== expected) throw new Error('sign-in state did not match')

  keep(
    await exchange(issuer, {
      grant_type: 'authorization_code',
      code,
      redirect_uri: redirectURI(),
      client_id: config().clientId,
      code_verifier: verifier,
    }),
  )
  return back
}

/** Trade a refresh token for a live one. Null when the session is over. */
export async function refresh(issuer: string): Promise<Session | null> {
  const s = session()
  if (!s?.refresh) return null
  try {
    const next = await exchange(issuer, {
      grant_type: 'refresh_token',
      refresh_token: s.refresh,
      client_id: config().clientId,
    })
    keep(next)
    return next
  } catch {
    forget()
    return null
  }
}

/** End the session here and at the issuer (OIDC RP-initiated logout). */
export function logout(issuer: string) {
  const s = session()
  forget()
  const q = new URLSearchParams({ post_logout_redirect_uri: window.location.origin })
  if (s?.idToken) q.set('id_token_hint', s.idToken)
  window.location.assign(`${issuer}/v1/iam/oauth/logout?${q}`)
}

type TokenResponse = {
  access_token?: string
  refresh_token?: string
  id_token?: string
  expires_in?: number
  error?: string
  error_description?: string
}

async function exchange(issuer: string, form: Record<string, string>): Promise<Session> {
  const res = await fetch(`${issuer}/v1/iam/oauth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams(form),
  })
  const body = (await res.json().catch(() => ({}))) as TokenResponse
  if (!res.ok || !body.access_token) {
    throw new Error(body.error_description || body.error || `token endpoint: ${res.status}`)
  }
  return {
    access: body.access_token,
    refresh: body.refresh_token,
    idToken: body.id_token,
    // A minute of headroom, so a request is not sent with a token that expires
    // between the check and the server reading it.
    expires: Date.now() + (body.expires_in ?? 3600) * 1000 - 60_000,
  }
}
