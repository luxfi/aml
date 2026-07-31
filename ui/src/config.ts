// Where the API is, and which IAM application this bundle is.
//
// Neither is configured. There is no config.json and no build-time value: the
// API origin is derived from the host this console is served on, and the
// clientId comes from the API's own /v1/aml/config, which is the route that
// already answers "which identity does this surface have".
//
// That is not a convenience, it is the security property. A configurable API
// origin is a value that points every authenticated request somewhere — set it
// to a host somebody else controls and the console hands over a bearer on the
// next call, and the authorization code with its verifier at the next exchange.
// Derivation removes the setting, so there is nothing to point: aml.hanzo.ai can
// only ever talk to api.hanzo.ai. The CSP names the same origin a second time,
// from the server side, so the two have to agree.
//
// The same goes for the clientId. Baked into the image it would be one brand's,
// and the image serves every brand — a Lux console carrying hanzo-aml
// authenticates against the wrong application, and every token it obtains is
// refused by the engine with nothing in the failure that says why.

/**
 * origin is the API this console talks to: the same registrable domain, under
 * the `api` label.
 *
 *   aml.hanzo.ai    -> https://api.hanzo.ai
 *   aml.lux.network -> https://api.lux.network
 *
 * A host that names no parent domain gets the empty string, which means same
 * origin, which is what the dev server proxies. That covers localhost, a bare
 * two-label domain, and an address literal: 127.0.0.1 has four labels but no
 * parent domain, and deriving api.0.0.1 from it would point the console at a
 * host that does not exist. A registrable domain never ends in a numeric label.
 *
 * There is deliberately no override. An override is the setting this exists to
 * remove.
 */
export function origin(): string {
  const host = window.location.hostname
  if (host.includes(':')) return '' // IPv6 literal
  const labels = host.split('.')
  if (labels.length < 3) return ''
  if (/^\d+$/.test(labels[labels.length - 1])) return '' // IPv4 literal
  return `${window.location.protocol}//api.${labels.slice(1).join('.')}`
}

let clientID = ''

/**
 * bindClient records the IAM application the API says this surface is. It is
 * called once, from the unauthenticated /v1/aml/config read, before anything
 * offers a sign-in.
 */
export function bindClient(id: string) {
  clientID = (id ?? '').trim()
}

/**
 * client is the IAM application this bundle authenticates as.
 *
 * It throws rather than returning empty. An empty clientId produces an
 * authorization request the issuer answers with an error, or a token with no
 * audience pinned — failing here, before the redirect, is the difference
 * between a message and a mystery.
 */
export function client(): string {
  if (!clientID) {
    throw new Error('the API named no client_id, so this console has no identity to sign in as')
  }
  return clientID
}
