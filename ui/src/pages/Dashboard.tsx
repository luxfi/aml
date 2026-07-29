import { useEffect, useState } from 'react'
import { listCases, health, type Case } from '../api'
import { ScreeningPanel } from './Screening'

const cardStyle: React.CSSProperties = {
  background: '#0a0a0a',
  border: '1px solid #262626',
  borderRadius: 8,
  padding: 16,
  minWidth: 160,
}

const labelStyle: React.CSSProperties = {
  fontSize: 12,
  color: '#737373',
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
}

const valueStyle: React.CSSProperties = {
  fontSize: 28,
  fontWeight: 600,
  color: '#fff',
  marginTop: 4,
}

export function Dashboard() {
  const [cases, setCases] = useState<Case[]>([])
  const [status, setStatus] = useState<string>('')
  const [err, setErr] = useState('')

  useEffect(() => {
    listCases()
      .then(setCases)
      .catch((e) => setErr(e.message))
    health()
      .then((h) => setStatus(h.status))
      .catch(() => setStatus('unreachable'))
  }, [])

  const open = cases.filter((c) => c.status === 'open').length
  const inReview = cases.filter((c) => c.status === 'in_review').length
  const escalated = cases.filter((c) => c.status === 'escalated').length
  const closed = cases.filter((c) => c.status === 'closed').length
  const recent = cases.slice(0, 5)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between' }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, color: '#fff' }}>Dashboard</h1>
        <span style={{ fontSize: 12, color: status === 'ok' ? '#22c55e' : '#ef4444' }}>
          Engine: {status || 'loading'}
        </span>
      </div>

      {err && (
        <div style={{ color: '#ef4444', fontSize: 13 }}>{err}</div>
      )}

      <ScreeningPanel />

      <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
        <div style={cardStyle}>
          <div style={labelStyle}>Open Cases</div>
          <div style={valueStyle}>{open}</div>
        </div>
        <div style={cardStyle}>
          <div style={labelStyle}>In Review</div>
          <div style={valueStyle}>{inReview}</div>
        </div>
        <div style={cardStyle}>
          <div style={labelStyle}>Escalated</div>
          <div style={valueStyle}>{escalated}</div>
        </div>
        <div style={cardStyle}>
          <div style={labelStyle}>Closed</div>
          <div style={valueStyle}>{closed}</div>
        </div>
        <div style={cardStyle}>
          <div style={labelStyle}>Total Cases</div>
          <div style={valueStyle}>{cases.length}</div>
        </div>
      </div>

      <section>
        <div style={labelStyle}>Recent Cases</div>
        <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: 8, fontSize: 13 }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #262626', color: '#737373', textAlign: 'left' }}>
              <th style={{ padding: '8px 12px' }}>#</th>
              <th style={{ padding: '8px 12px' }}>Status</th>
              <th style={{ padding: '8px 12px' }}>Severity</th>
              <th style={{ padding: '8px 12px' }}>Opened</th>
            </tr>
          </thead>
          <tbody>
            {recent.map((c) => (
              <tr key={c.id} style={{ borderBottom: '1px solid #171717' }}>
                <td style={{ padding: '8px 12px', fontFamily: 'monospace' }}>
                  {c.number}
                </td>
                <td style={{ padding: '8px 12px' }}>
                  <Badge text={c.status} />
                </td>
                <td style={{ padding: '8px 12px' }}>
                  <SeverityBadge severity={c.severity} />
                </td>
                <td style={{ padding: '8px 12px', color: '#737373' }}>
                  {new Date(c.opened_at).toLocaleDateString()}
                </td>
              </tr>
            ))}
            {recent.length === 0 && (
              <tr>
                <td colSpan={4} style={{ padding: '16px 12px', color: '#525252' }}>
                  No cases yet
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </section>
    </div>
  )
}

function Badge({ text }: { text: string }) {
  return (
    <span
      style={{
        display: 'inline-block',
        padding: '2px 8px',
        borderRadius: 4,
        fontSize: 12,
        background: '#262626',
        color: '#d4d4d4',
      }}
    >
      {text}
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
        display: 'inline-block',
        padding: '2px 8px',
        borderRadius: 4,
        fontSize: 12,
        background: `${colors[severity] ?? '#525252'}20`,
        color: colors[severity] ?? '#a3a3a3',
      }}
    >
      {severity}
    </span>
  )
}
