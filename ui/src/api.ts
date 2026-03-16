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

export interface RuleTestResult {
  match: boolean
  dsl: string
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

export function testRule(dsl: string): Promise<RuleTestResult> {
  return json('/v1/aml/rules/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      dsl,
      tx: { id: 'test', notional: 15000, currency: 'USD', qty: 1 },
      entity: { id: 'test', name: 'Test User', entity_type: 'user' },
    }),
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
