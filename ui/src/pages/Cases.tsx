import { useEffect, useState } from 'react'
import { listCases, type Case } from '../api'

const statuses = ['all', 'open', 'in_review', 'escalated', 'closed'] as const

export function Cases() {
  const [cases, setCases] = useState<Case[]>([])
  const [filter, setFilter] = useState<string>('all')
  const [expanded, setExpanded] = useState<string | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    listCases(filter)
      .then(setCases)
      .catch((e) => setErr(e.message))
  }, [filter])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, color: '#fff' }}>Cases</h1>

      {err && <div style={{ color: '#ef4444', fontSize: 13 }}>{err}</div>}

      <div style={{ display: 'flex', gap: 8 }}>
        {statuses.map((s) => (
          <button
            key={s}
            onClick={() => setFilter(s)}
            style={{
              padding: '6px 14px',
              borderRadius: 6,
              border: 'none',
              cursor: 'pointer',
              fontSize: 13,
              background: filter === s ? '#262626' : 'transparent',
              color: filter === s ? '#fff' : '#737373',
            }}
          >
            {s.replace('_', ' ')}
          </button>
        ))}
      </div>

      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #262626', color: '#737373', textAlign: 'left' }}>
            <th style={th}>#</th>
            <th style={th}>Status</th>
            <th style={th}>Severity</th>
            <th style={th}>Assignee</th>
            <th style={th}>Opened</th>
            <th style={th}>Resolution</th>
          </tr>
        </thead>
        <tbody>
          {cases.map((c) => (
            <>
              <tr
                key={c.id}
                onClick={() => setExpanded(expanded === c.id ? null : c.id)}
                style={{ borderBottom: '1px solid #171717', cursor: 'pointer' }}
              >
                <td style={td}>{c.number}</td>
                <td style={td}>
                  <StatusBadge status={c.status} />
                </td>
                <td style={td}>
                  <SeverityBadge severity={c.severity} />
                </td>
                <td style={td}>{c.assignee_id || '-'}</td>
                <td style={{ ...td, color: '#737373' }}>
                  {new Date(c.opened_at).toLocaleDateString()}
                </td>
                <td style={td}>{c.resolution || '-'}</td>
              </tr>
              {expanded === c.id && (
                <tr key={`${c.id}-detail`}>
                  <td colSpan={6} style={{ padding: '12px', background: '#0a0a0a' }}>
                    <div style={{ fontSize: 12, color: '#a3a3a3' }}>
                      <div>Case ID: {c.id}</div>
                      <div>Alerts: {c.alert_ids?.join(', ') || 'none'}</div>
                      <div>Entities: {c.entity_ids?.join(', ') || 'none'}</div>
                      {c.closed_at && <div>Closed: {new Date(c.closed_at).toLocaleString()}</div>}
                    </div>
                  </td>
                </tr>
              )}
            </>
          ))}
          {cases.length === 0 && (
            <tr>
              <td colSpan={6} style={{ padding: '16px 12px', color: '#525252' }}>
                No cases
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

function StatusBadge({ status }: { status: string }) {
  const colors: Record<string, string> = {
    open: '#3b82f6',
    in_review: '#f59e0b',
    escalated: '#f97316',
    closed: '#525252',
  }
  return (
    <span
      style={{
        padding: '2px 8px',
        borderRadius: 4,
        fontSize: 12,
        background: `${colors[status] ?? '#262626'}20`,
        color: colors[status] ?? '#a3a3a3',
      }}
    >
      {status.replace('_', ' ')}
    </span>
  )
}

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
