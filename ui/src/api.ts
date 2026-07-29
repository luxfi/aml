// api.ts -- typed fetch wrappers for /v1/aml/* endpoints.
// All calls use relative URLs so they work on any host.

export interface Case {
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
  created_at: string
  updated_at: string
}

export interface Rule {
  id: string
  org_id: string
  name: string
  description: string
  dsl: string
  severity: string
  weight: number
  action: string
  enabled: boolean
  jurisdiction_filter?: string[]
  asset_class_filter?: string[]
  priority: number
  created_at: string
  updated_at: string
}

export interface Alert {
  id: string
  org_id: string
  tx_id: string
  rule_id: string
  rule_name: string
  severity: string
  score: number
  score_breakdown?: Record<string, number>
  action_taken: string
  reviewed_by?: string
  reviewed_at?: string
  decision?: string
  created_at: string
  updated_at: string
}

export interface SanctionsResult {
  id: string
  list_id: string
  name: string
  aliases?: string[]
  dob?: string
  nationality?: string
  type: string
  score: number
}

// A replay of a candidate rule over history. The counts are what the rule would
// have raised; the proportions are absent rather than zero when nothing in the
// replayed period was judged.
export interface RuleOutcome {
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

export interface RuleTestResult {
  events: number
  from?: string
  to?: string
  candidate: RuleOutcome
  incumbent?: RuleOutcome
  delta?: {
    added?: string[]
    dropped?: string[]
    kept?: string[]
    counts: { added: number; dropped: number; kept: number }
  }
}

async function json<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) {
    const body = await res.text()
    throw new Error(`${res.status}: ${body}`)
  }
  return res.json()
}

export function listCases(status?: string): Promise<Case[]> {
  const q = status && status !== 'all' ? `?status=${status}` : ''
  return json(`/v1/aml/cases${q}`)
}

export function listRules(): Promise<Rule[]> {
  return json('/v1/aml/rules')
}

// testRule replays a candidate against the org's retained transactions. Omitting
// the sample is what asks for real history; incumbent names the rule this one
// would replace, so the report carries the difference.
export function testRule(dsl: string, incumbent?: string): Promise<RuleTestResult> {
  return json('/v1/aml/rules/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ dsl, incumbent }),
  })
}

export function listAlerts(txId: string): Promise<Alert[]> {
  return json(`/v1/aml/transactions/${txId}/alerts`)
}

export function searchSanctions(name: string, dob?: string): Promise<SanctionsResult[]> {
  return json('/v1/aml/sanctions/search', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, dob }),
  })
}

export function health(): Promise<{ status: string; time: string }> {
  return json('/v1/aml/health')
}

// Screening readiness, per sanctions list.
//
// A list that has stopped loading returns no matches, and no matches is what a
// clean party also returns — so the count and the date are the only way to tell
// "nobody on this payment is designated" from "the list is empty". loaded_at is
// null for a list that has never loaded, which is not the same as a list with
// nobody on it.
export interface ScreeningSource {
  source: string
  entries: number
  loaded_at: string | null
  age_hours: number
  fresh: boolean
  sha256?: string
  error?: string
  attempted_at: string | null
}

export interface Screening {
  ready: boolean
  unfit: string[]
  total_entries: number
  sources: ScreeningSource[]
}

// screeningSources answers 503 when any list is unfit, so the body is read on
// both outcomes rather than treated as an error: an unready instance is exactly
// the state this panel exists to show.
export async function screeningSources(): Promise<Screening> {
  const res = await fetch('/v1/aml/sanctions/sources')
  if (res.status !== 200 && res.status !== 503) {
    throw new Error(`${res.status}: ${await res.text()}`)
  }
  return res.json()
}
