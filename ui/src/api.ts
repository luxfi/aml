// The AML API, typed. One function per route the engine actually serves — see
// pkg/api/routes.go, which is the list this mirrors and the only list there is.
//
// Every authenticated call carries the IAM access token as a bearer. The engine
// verifies it itself (signature under the brand of the Host, audience pinned to
// this application, access token not id token, org within the caller's
// membership), so the console never asserts a tenant: it presents a credential
// and the engine decides whose data that is.

import { config } from './config'
import * as auth from './auth'

/** Raised when the session is over. The shell answers by signing in again. */
export class Unauthenticated extends Error {}

/** Raised with the engine's own message, which is safe to show. */
export class Refused extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
  }
}

function base(): string {
  return `${config().api}/v1/aml`
}

/** The brand this API answers as. Unauthenticated: it names the issuer a caller needs. */
export type Brand = { brand: string; display: string; issuer: string; domain: string }

export async function brand(): Promise<Brand> {
  const res = await fetch(`${base()}/config`)
  if (!res.ok) throw new Refused(res.status, `no brand serves this host (${res.status})`)
  return res.json() as Promise<Brand>
}

let issuer = ''

/** Bind the issuer the session refreshes against. Called once, after brand(). */
export function bind(iss: string) {
  issuer = iss
}

async function bearer(): Promise<string> {
  let s = auth.session()
  if (s && s.expires <= Date.now()) s = await auth.refresh(issuer)
  if (!s) throw new Unauthenticated('not signed in')
  return s.access
}

type Options = { method?: string; body?: unknown; accept?: number[] }

async function call<T>(path: string, o: Options = {}): Promise<T> {
  const send = async (token: string) =>
    fetch(`${base()}${path}`, {
      method: o.method ?? 'GET',
      headers: {
        Authorization: `Bearer ${token}`,
        ...(o.body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: o.body === undefined ? undefined : JSON.stringify(o.body),
    })

  let res = await send(await bearer())
  if (res.status === 401) {
    // One retry on a fresh token: an access token can expire between the check
    // and the read. A second 401 is an answer, not a race.
    const next = await auth.refresh(issuer)
    if (!next) throw new Unauthenticated('session expired')
    res = await send(next.access)
    if (res.status === 401) throw new Unauthenticated('session expired')
  }
  if (!res.ok && !(o.accept ?? []).includes(res.status)) {
    const body = (await res.json().catch(() => ({}))) as { error?: string }
    throw new Refused(res.status, body.error || `${res.status}`)
  }
  return res.json() as Promise<T>
}

// ── Health ───────────────────────────────────────────────────────────────────

export type Health = { status: string; records: string; time: string }

/** 503 carries the same body as 200 and is the state worth showing, not an error. */
export const health = () => call<Health>('/health', { accept: [503] })

// ── Cases ────────────────────────────────────────────────────────────────────

export type Case = {
  id: string
  org_id: string
  number: number
  status: string
  severity: string
  entity_ids?: string[]
  alert_ids?: string[]
  assignee_id?: string
  opened_at: string
  closed_at?: string
  resolution?: string
  assessment?: string
  events?: CaseEvent[]
  created_at: string
  updated_at: string
}

export type CaseEvent = {
  id: string
  case_id: string
  author_id: string
  kind: string
  body?: string
  file_path?: string
  created_at: string
}

export const cases = (status?: string) =>
  call<Case[]>(`/cases${status && status !== 'all' ? `?status=${encodeURIComponent(status)}` : ''}`)

export const caseEvents = (id: string) =>
  call<CaseEvent[]>(`/cases/${encodeURIComponent(id)}/events`)

export const addEvent = (id: string, e: { kind: string; body: string; author_id?: string }) =>
  call<{ status: string }>(`/cases/${encodeURIComponent(id)}/events`, { method: 'POST', body: e })

/** A resolution is a retained decision with its rationale, not a deleted row. */
export type Resolution = {
  resolution: string
  considered: string[]
  rationale: string
  by: string
}

export const resolve = (id: string, r: Resolution) =>
  call<{ case: string; resolution: string; assessment: string }>(
    `/cases/${encodeURIComponent(id)}/resolve`,
    { method: 'POST', body: r },
  )

// ── Rules ────────────────────────────────────────────────────────────────────

export type Citation = { authority?: string; document?: string; locator?: string; url?: string }

export type Rule = {
  id: string
  name: string
  description: string
  typology?: string
  citations?: Citation[]
  dsl: string
  severity: string
  weight: number
  action: string
  enabled: boolean
  priority: number
}

export const rules = () => call<Rule[]>('/rules')

export type Outcome = {
  rule: string
  alerts: number
  txs?: string[]
  observed: number
  judged: number
  productive: number
  unproductive: number
  false_positive_proportion?: number
  intelligence_value?: number
}

export type Replay = {
  events: number
  from?: string
  to?: string
  candidate: Outcome
  incumbent?: Outcome
  delta?: {
    added?: string[]
    dropped?: string[]
    kept?: string[]
    counts: { added: number; dropped: number; kept: number }
  }
}

/** Replay a candidate expression over the org's retained history. Writes nothing. */
export const testRule = (dsl: string, incumbent?: string) =>
  call<Replay>('/rules/test', { method: 'POST', body: { dsl, incumbent: incumbent || undefined } })

// ── Catalog ──────────────────────────────────────────────────────────────────

export type Gap = { citation: Citation; why: string; needs: string }

export type Catalog = {
  typologies: string[]
  rules: Array<{
    id: string
    name: string
    typology?: string
    description: string
    expression: string
    severity: string
    action: string
    enabled: boolean
    citations?: Citation[]
  }>
  obligations: Citation[]
  gaps: Gap[]
}

export const catalog = () => call<Catalog>('/catalog')

// ── Transactions and alerts ──────────────────────────────────────────────────

export type Transaction = {
  id?: string
  source?: string
  user_id: string
  account_id?: string
  symbol?: string
  asset_class?: string
  side?: string
  qty?: number
  notional: number
  currency: string
  counterparty?: string
  customer_jurisdiction?: string
  ip_address?: string
  device_fingerprint?: string
  timestamp?: string
}

export type Entity = {
  id?: string
  entity_type?: string
  name?: string
  jurisdiction?: string
  kyc_level?: number
  pep?: boolean
  risk_score?: number
}

export type Alert = {
  id: string
  tx_id: string
  rule_id: string
  rule_name: string
  typology?: string
  citations?: Citation[]
  severity: string
  score: number
  score_breakdown?: Record<string, number>
  action_taken: string
  eval_error?: string
  created_at: string
}

export type Evaluation = {
  action: string
  score: number
  alert_ids?: string[]
  case_id?: string
  record: string
}

/** Ingest is an evaluation AND a retained record. It is the only write path. */
export const ingest = (tx: Transaction, entity: Entity, relationship?: string) =>
  call<Evaluation>('/transactions', { method: 'POST', body: { ...tx, entity, relationship } })

export const alerts = (txID: string) =>
  call<Alert[]>(`/transactions/${encodeURIComponent(txID)}/alerts`)

// ── Model ────────────────────────────────────────────────────────────────────

export type Model = Record<string, unknown> & { enabled: boolean; reason?: string; faults?: number }

export const model = () => call<Model>('/anomaly')

export const scoreTest = (tx: Transaction, entity: Entity) =>
  call<Record<string, unknown>>('/anomaly/test', { method: 'POST', body: { tx, entity } })

// ── Sanctions ────────────────────────────────────────────────────────────────

export type Hit = {
  list: string
  ref_id: string
  name: string
  kind: string
  score: number
  reason: string
  agree?: string[]
  conflict?: string[]
  programs?: string[]
}

export const screen = (name: string, dob?: string) =>
  call<Hit[]>('/sanctions/search', { method: 'POST', body: { name, dob: dob || undefined } })

export type Source = {
  source: string
  entries: number
  loaded_at: string | null
  age_hours: number
  fresh: boolean
  sha256?: string
  error?: string
  attempted_at: string | null
}

export type Screening = { ready: boolean; unfit: string[]; total_entries: number; sources: Source[] }

/** Unfit lists answer 503 with the body that says so. That is the reading. */
export const sources = () => call<Screening>('/sanctions/sources', { accept: [503] })

// ── Relationships ────────────────────────────────────────────────────────────

/** The domains a party may be named under when searching the index. */
export const domains = ['name', 'subject', 'account', 'wallet', 'device', 'network'] as const
export type Domain = (typeof domains)[number]

export type Lookback = {
  maintained: boolean
  current: boolean
  natures?: string[]
  records?: string[]
  from: string
  to: string
  examined: number
}

export const findRelationships = (party: string, domain: Domain) =>
  call<Lookback>('/relationships/search', { method: 'POST', body: { party, domain } })

export type Opening = {
  ref: string
  nature: string
  opened?: string
  user_id?: string
  name?: string
  account_id?: string
  wallet?: string
}

export const openRelationship = (o: Opening) =>
  call<{ relationship: string }>('/relationships', { method: 'POST', body: o })

export const closeRelationship = (id: string, ended?: string) =>
  call<{ clocks_started: number }>(`/relationships/${encodeURIComponent(id)}/close`, {
    method: 'POST',
    body: { ended: ended || undefined },
  })
