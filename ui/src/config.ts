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

/** The label this console is served under. Anything else is not this console. */
const CONSOLE = 'aml'

/**
 * apiOrigin derives the API origin from the host a console is served on: the
 * same registrable domain, under the `api` label, in place of `aml`.
 *
 *   aml.hanzo.ai    -> https://api.hanzo.ai
 *   aml.lux.network -> https://api.lux.network
 *
 * The empty string means same origin, which is what the dev server proxies.
 * Everything that is not a recognisable `aml.<domain>` gets it:
 *
 *   - a first label that is not `aml`. This console is served at aml.<domain>
 *     and nowhere else, so a host shaped like evil.aml.hanzo.ai — which is a
 *     four-label host the old rule happily read as api.aml.hanzo.ai — is not one
 *     of ours and gets no derivation. Nothing today can present that host (the
 *     certificate is single-name and the ingress rule matches one host), so this
 *     is not the barrier; it is the barrier not resting on the certificate.
 *   - an empty label, which is a trailing dot or a doubled separator.
 *   - fewer than three labels, an address literal, or an IPv6 host. 127.0.0.1
 *     has four labels but no parent domain, and api.0.0.1 is not a host; a
 *     registrable domain never ends in a numeric label.
 *
 * It takes the host rather than reading it, so the rule can be stated as a
 * table in a test instead of a claim in a comment. [origin] is the one caller
 * that reads the window.
 *
 * There is deliberately no override. An override is the setting this exists to
 * remove: a configurable API origin is a value that points every authenticated
 * request somewhere, and pointing it at a host somebody else controls hands
 * over a bearer on the next call.
 */
export function apiOrigin(protocol: string, hostname: string): string {
  if (hostname.includes(':')) return '' // IPv6 literal
  const labels = hostname.split('.')
  if (labels.length < 3) return ''
  if (labels.some((l) => l === '')) return ''
  if (labels[0] !== CONSOLE) return ''
  if (/^\d+$/.test(labels[labels.length - 1])) return '' // IPv4 literal
  return `${protocol}//api.${labels.slice(1).join('.')}`
}

/** origin is [apiOrigin] applied to the host this bundle is being served on. */
export function origin(): string {
  return apiOrigin(window.location.protocol, window.location.hostname)
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
