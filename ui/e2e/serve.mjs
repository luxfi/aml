// The console, served the way production serves it.
//
// The point of this server is the header. ghcr.io/hanzoai/static answers every
// request with the policy from HANZO_STATIC_CSP, and this reads that same
// policy out of ../csp.txt and sends it verbatim — so the browser the tests
// drive is under the deployed rules, not a relaxed copy of them. A harness that
// tests a weaker policy than the one that ships proves nothing.
//
// It also answers /v1/aml/*, on THIS origin. That is not a shortcut: the
// console derives its API from the host it is served on, and a host with no
// parent domain means same origin (see src/config.ts). So localhost gets the
// same-origin API, `connect-src 'self'` covers it, and the policy under test
// stays byte-identical to the deployed one.

import { createReadStream } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { extname, join, normalize, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = fileURLToPath(new URL('.', import.meta.url))
const root = resolve(here, '..', 'dist')
const csp = (await readFile(resolve(here, '..', 'csp.txt'), 'utf8')).trim()
const port = Number(process.env.PORT ?? 4173)

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.json': 'application/json; charset=utf-8',
  '.woff2': 'font/woff2',
  '.ico': 'image/x-icon',
}

// Enough of the engine for every screen to render. Shapes, not fixtures: what
// is being proved here is what the DOM does under the policy, not what the
// numbers are.
const CONFIG = {
  brand: 'hanzo',
  display: 'Hanzo',
  issuer: 'https://hanzo.id',
  domain: 'hanzo.ai',
  client_id: 'hanzo-aml',
}

const CASE = {
  id: 'c-1',
  org_id: 'hanzo/acme',
  number: 1,
  status: 'open',
  severity: 'high',
  alert_ids: ['al-1'],
  entity_ids: ['cust-1042'],
  opened_at: '2026-07-01T09:00:00Z',
  created_at: '2026-07-01T09:00:00Z',
  updated_at: '2026-07-01T09:00:00Z',
}

const CITATION = { authority: 'FinCEN', document: '31 CFR', locator: '1010.311' }

const RULE = {
  id: 'ctr',
  name: 'CTR Threshold',
  description: 'Reportable currency transaction',
  typology: 'threshold reporting',
  citations: [CITATION],
  dsl: 'Tx.Notional > 10000.0',
  expression: 'Tx.Notional > 10000.0',
  severity: 'high',
  weight: 0.3,
  action: 'report',
  enabled: true,
}

function api(pathname, method) {
  const at = (p) => pathname === `/v1/aml${p}`
  if (at('/config')) return CONFIG
  if (at('/health')) return { status: 'ok', records: 'ready' }
  if (at('/cases')) return method === 'GET' ? [CASE] : CASE
  if (/^\/v1\/aml\/cases\/[^/]+\/events$/.test(pathname)) {
    return [
      {
        id: 'e-1',
        case_id: 'c-1',
        author_id: 'a.mensah',
        kind: 'note',
        body: 'source of funds requested',
        created_at: '2026-07-01T10:00:00Z',
      },
    ]
  }
  if (/^\/v1\/aml\/cases\/[^/]+\/resolve$/.test(pathname)) return { ok: true }
  if (at('/rules')) return [RULE]
  if (at('/rules/test')) {
    return {
      events: 3,
      candidate: { alerts: 1, subjects: 1 },
      incumbent: { alerts: 1, subjects: 1 },
      delta: { sizes: { added: 1, dropped: 1, kept: 0 }, added: [], dropped: [], kept: [] },
    }
  }
  if (at('/catalog')) {
    return {
      typologies: ['threshold reporting', 'structuring', 'sanctions'],
      rules: [RULE],
      obligations: [CITATION],
      gaps: [
        {
          citation: { authority: 'JMLSG', document: 'Part II', locator: '5.7.18' },
          why: 'no trade data reaches this deployment',
          needs: 'a trade feed',
        },
      ],
    }
  }
  if (at('/anomaly')) {
    return {
      enabled: true,
      mode: 'shadow',
      warming: false,
      faults: 0,
      appetite: { review: 0.02, sample: 0.01 },
      realised: 0.018,
      threshold: 0.71,
      subjects: 12,
    }
  }
  if (at('/anomaly/test')) return { score: 0.42, causes: [{ feature: 'notional', without: 0.11 }] }
  if (at('/transactions')) return { action: 'review', score: 0.42, alert_ids: ['al-1'], case_id: 'c-1' }
  if (/^\/v1\/aml\/transactions\/[^/]+\/alerts$/.test(pathname)) {
    return [{ id: 'al-1', rule_id: 'ctr', severity: 'high', action: 'report', score: 0.42, created_at: '2026-07-01T09:00:00Z' }]
  }
  if (at('/sanctions/search')) return { matches: [], searched: 12345 }
  if (at('/sanctions/sources')) {
    return {
      ready: true,
      unfit: [],
      total_entries: 12345,
      sources: [
        {
          source: 'OFAC SDN',
          entries: 12345,
          loaded_at: '2026-07-30T00:00:00Z',
          attempted_at: '2026-07-30T00:00:00Z',
          age_hours: 6,
          fresh: true,
          sha256: 'aa' + '00'.repeat(31),
        },
      ],
    }
  }
  if (at('/relationships')) return { id: 'r-1', party: 'cust-1042', nature: 'retail current account' }
  if (at('/relationships/search')) return { examined: 3, records: [], party: 'cust-1042' }
  if (/^\/v1\/aml\/relationships\/[^/]+\/close$/.test(pathname)) return { ok: true }
  return null
}

createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', `http://localhost:${port}`)
  res.setHeader('Content-Security-Policy', csp)
  res.setHeader('X-Content-Type-Options', 'nosniff')
  res.setHeader('Referrer-Policy', 'no-referrer')

  if (url.pathname.startsWith('/v1/aml')) {
    const body = api(url.pathname, req.method ?? 'GET')
    res.statusCode = body === null ? 404 : 200
    res.setHeader('Content-Type', 'application/json; charset=utf-8')
    res.end(JSON.stringify(body ?? { error: 'no such route' }))
    return
  }

  // The bundle, then index.html for anything else — which is what -spa does,
  // and what makes a hard refresh on /cases resolve.
  const rel = normalize(url.pathname).replace(/^(\.\.[/\\])+/, '')
  const file = join(root, rel)
  const send = (p) => {
    res.setHeader('Content-Type', TYPES[extname(p)] ?? 'application/octet-stream')
    createReadStream(p).pipe(res)
  }
  if (rel !== '/' && extname(rel)) {
    createReadStream(file)
      .on('error', () => {
        res.statusCode = 404
        res.end('not found')
      })
      .pipe(res.writeHead(200, { 'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream' }))
    return
  }
  send(join(root, 'index.html'))
}).listen(port, () => console.log(`console on http://127.0.0.1:${port}`))
