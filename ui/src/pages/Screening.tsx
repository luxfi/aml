import { useEffect, useState } from 'react'
import { screeningSources, type Screening, type ScreeningSource } from '../api'

// The panel this product exists to have. Screening failure is silent by nature:
// an empty list matches nobody, and so does a clean party. Showing the entry
// count and the load date per list is what separates those two states, and it is
// the state that goes wrong — a retired endpoint, a changed schema, a token that
// starts answering 403 — without anything else noticing.
//
// It reads unready as information rather than as an error, because an instance
// that cannot screen is precisely what an operator needs to see.

const ok = '#22c55e'
const bad = '#ef4444'
const dim = '#737373'
const line = '#262626'

const label: React.CSSProperties = {
  fontSize: 12,
  color: dim,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
}

function age(s: ScreeningSource): string {
  if (!s.loaded_at) return 'never'
  const h = s.age_hours
  if (h < 1) return `${Math.round(h * 60)}m ago`
  if (h < 48) return `${Math.round(h)}h ago`
  return `${Math.round(h / 24)}d ago`
}

export function ScreeningPanel() {
  const [data, setData] = useState<Screening | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    screeningSources()
      .then(setData)
      .catch((e) => setErr(e.message))
  }, [])

  if (err) return <div style={{ color: bad, fontSize: 13 }}>Screening readiness unavailable: {err}</div>
  if (!data) return <div style={{ color: dim, fontSize: 13 }}>Loading screening readiness…</div>

  return (
    <section>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12 }}>
        <div style={label}>Sanctions screening</div>
        <span style={{ fontSize: 12, color: data.ready ? ok : bad, fontWeight: 600 }}>
          {data.ready ? 'fit to screen' : `NOT fit to screen — ${data.unfit.join(', ')}`}
        </span>
        <span style={{ fontSize: 12, color: dim, marginLeft: 'auto' }}>
          {data.total_entries.toLocaleString()} designations loaded
        </span>
      </div>

      {/* Stated rather than implied: a partial list set is not partial coverage. */}
      {!data.ready && (
        <div style={{ marginTop: 8, fontSize: 12, color: bad, lineHeight: 1.5 }}>
          A party designated only on an unfit list will not match. A screening result
          against these lists cannot be read as clean.
        </div>
      )}

      <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: 12, fontSize: 13 }}>
          <thead>
            <tr style={{ borderBottom: `1px solid ${line}`, color: dim, textAlign: 'left' }}>
              <th style={{ padding: '8px 12px' }}>List</th>
              <th style={{ padding: '8px 12px', textAlign: 'right' }}>Designations</th>
              <th style={{ padding: '8px 12px' }}>Last loaded</th>
              <th style={{ padding: '8px 12px' }}>State</th>
            </tr>
          </thead>
          <tbody>
            {data.sources.map((s) => (
              <tr key={s.source} style={{ borderBottom: '1px solid #171717' }}>
                <td style={{ padding: '8px 12px', fontFamily: 'monospace' }}>{s.source}</td>
                <td
                  style={{
                    padding: '8px 12px',
                    textAlign: 'right',
                    fontFamily: 'monospace',
                    color: s.entries === 0 ? bad : undefined,
                  }}
                >
                  {s.entries.toLocaleString()}
                </td>
                <td style={{ padding: '8px 12px', color: s.loaded_at ? dim : bad }}>{age(s)}</td>
                <td style={{ padding: '8px 12px' }}>
                  <span style={{ color: s.fresh ? ok : bad, fontSize: 12 }}>
                    {s.fresh ? 'fresh' : s.loaded_at ? 'stale' : 'never loaded'}
                  </span>
                  {s.error && (
                    <div style={{ color: bad, fontSize: 11, marginTop: 2, fontFamily: 'monospace' }}>
                      {s.error}
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}
