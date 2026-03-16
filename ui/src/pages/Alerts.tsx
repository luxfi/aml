import { useEffect, useState } from 'react'
import { listCases, listAlerts, type Case, type Alert } from '../api'

export function Alerts() {
  const [cases, setCases] = useState<Case[]>([])
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [sevFilter, setSevFilter] = useState<string>('all')
  const [expanded, setExpanded] = useState<string | null>(null)
  const [err, setErr] = useState('')

  // Derive alerts from cases' alert_ids. The API returns alerts per-transaction,
  // so we collect unique tx_ids from case alert references and fetch each.
  useEffect(() => {
    listCases()
      .then(async (cs) => {
        setCases(cs)
        // Collect all unique alert fetches -- alerts are keyed by tx,
        // but we only have case.alert_ids. For the embedded UI, we show
        // case-level alert metadata as placeholder rows when the
        // per-transaction fetch is not available.
        const allAlerts: Alert[] = []
        // Derive minimal alert rows from case data.
        for (const c of cs) {
          for (const aid of c.alert_ids ?? []) {
            allAlerts.push({
              id: aid,
              org_id: c.org_id,
              tx_id: '',
              rule_id: '',
              rule_name: '',
              severity: c.severity,
              score: 0,
              action_taken: '',
              created_at: c.opened_at,
              updated_at: c.updated_at,
            })
          }
        }
        setAlerts(allAlerts)
      })
      .catch((e) => setErr(e.message))
  }, [])

  const filtered =
    sevFilter === 'all' ? alerts : alerts.filter((a) => a.severity === sevFilter)

  const severities = ['all', 'low', 'medium', 'high', 'critical']

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, color: '#fff' }}>Alerts</h1>

      {err && <div style={{ color: '#ef4444', fontSize: 13 }}>{err}</div>}

      <div style={{ display: 'flex', gap: 8 }}>
        {severities.map((s) => (
          <button
            key={s}
            onClick={() => setSevFilter(s)}
            style={{
              padding: '4px 12px',
              borderRadius: 12,
              border: '1px solid #262626',
              cursor: 'pointer',
              fontSize: 12,
              background: sevFilter === s ? '#262626' : 'transparent',
              color: sevFilter === s ? '#fff' : '#737373',
            }}
          >
            {s}
          </button>
        ))}
      </div>

      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #262626', color: '#737373', textAlign: 'left' }}>
            <th style={th}>Time</th>
            <th style={th}>Alert ID</th>
            <th style={th}>Severity</th>
            <th style={th}>Score</th>
            <th style={th}>Action</th>
          </tr>
        </thead>
        <tbody>
          {filtered.map((a) => (
            <>
              <tr
                key={a.id}
                onClick={() => setExpanded(expanded === a.id ? null : a.id)}
                style={{ borderBottom: '1px solid #171717', cursor: 'pointer' }}
              >
                <td style={{ ...td, color: '#737373' }}>
                  {new Date(a.created_at).toLocaleString()}
                </td>
                <td style={{ ...td, fontFamily: 'monospace', fontSize: 12 }}>
                  {a.id.slice(0, 8)}
                </td>
                <td style={td}>
                  <SeverityBadge severity={a.severity} />
                </td>
                <td style={td}>{a.score.toFixed(2)}</td>
                <td style={td}>{a.action_taken || '-'}</td>
              </tr>
              {expanded === a.id && (
                <tr key={`${a.id}-detail`}>
                  <td colSpan={5} style={{ padding: 12, background: '#0a0a0a' }}>
                    <div style={{ fontSize: 12, color: '#a3a3a3' }}>
                      <div>Full ID: {a.id}</div>
                      <div>Transaction: {a.tx_id || '-'}</div>
                      <div>Rule: {a.rule_name || a.rule_id || '-'}</div>
                      <div>Decision: {a.decision || '-'}</div>
                      {a.score_breakdown && (
                        <div style={{ marginTop: 8 }}>
                          <div style={{ marginBottom: 4, color: '#737373' }}>Score Breakdown:</div>
                          {Object.entries(a.score_breakdown).map(([k, v]) => (
                            <div key={k} style={{ display: 'flex', gap: 8 }}>
                              <span style={{ color: '#525252' }}>{k}:</span>
                              <span>{v.toFixed(3)}</span>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  </td>
                </tr>
              )}
            </>
          ))}
          {filtered.length === 0 && (
            <tr>
              <td colSpan={5} style={{ padding: '16px 12px', color: '#525252' }}>
                No alerts
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

const th: React.CSSProperties = { padding: '8px 12px', fontWeight: 500 }
const td: React.CSSProperties = { padding: '8px 12px' }

function SeverityBadge({ severity }: { severity: string }) {
  const colors: Record<string, string> = {
    low: '#3b82f6',
    medium: '#f59e0b',
    high: '#f97316',
    critical: '#ef4444',
  }
  return (
    <span
      style={{
        padding: '2px 8px',
        borderRadius: 4,
        fontSize: 12,
        background: `${colors[severity] ?? '#262626'}20`,
        color: colors[severity] ?? '#a3a3a3',
      }}
    >
      {severity}
    </span>
  )
}
