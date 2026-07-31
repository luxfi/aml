// Deploy-time configuration, read at startup from /config.json.
//
// Two values, and only two: where the API is, and which IAM application this
// bundle is. Everything else about the brand — its display name and the issuer
// a caller must get a token from — comes from the API's own /v1/aml/config,
// which derives it from the Host. One source for the brand, and it is the
// server that already had to decide.
//
// The file is templated at container start by hanzoai/static from SPA_* env
// (SPA_API -> api, SPA_CLIENT_ID -> clientId), so the same immutable bundle
// serves every brand and every environment. Nothing here is a secret: a
// clientId is public by construction, and a public PKCE client holds none.

export type Config = {
  /** API origin, e.g. https://api.hanzo.ai. Empty means same origin. */
  api: string
  /** This deployment's IAM application. The audience every token is pinned to. */
  clientId: string
}

let loaded: Config | null = null

export async function load(): Promise<Config> {
  if (loaded) return loaded
  const res = await fetch('/config.json', { cache: 'no-store' })
  if (!res.ok) throw new Error(`config.json: ${res.status}`)
  const raw = (await res.json()) as Partial<Config>
  loaded = {
    api: (raw.api ?? '').replace(/\/+$/, ''),
    clientId: raw.clientId ?? '',
  }
  if (!loaded.clientId) throw new Error('config.json names no clientId, so no token can be obtained')
  return loaded
}

/** The loaded config. Only valid after load() has resolved. */
export function config(): Config {
  if (!loaded) throw new Error('config read before it was loaded')
  return loaded
}
