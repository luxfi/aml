import { useState } from 'react'
import { searchSanctions, type SanctionsResult } from '../api'

export function Sanctions() {
  const [name, setName] = useState('')
  const [dob, setDob] = useState('')
  const [results, setResults] = useState<SanctionsResult[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  const handleSearch = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setErr('')
    setLoading(true)
    try {
      const r = await searchSanctions(name.trim(), dob.trim() || undefined)
      setResults(r)
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, color: '#fff' }}>Sanctions Search</h1>

      <form onSubmit={handleSearch} style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <label style={{ fontSize: 12, color: '#737373' }}>Name</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Full name"
            style={inputStyle}
          />
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <label style={{ fontSize: 12, color: '#737373' }}>Date of Birth</label>
          <input
            type="text"
            value={dob}
            onChange={(e) => setDob(e.target.value)}
            placeholder="YYYY-MM-DD (optional)"
            style={inputStyle}
          />
        </div>
        <button type="submit" disabled={loading || !name.trim()} style={btnStyle}>
          {loading ? 'Searching...' : 'Search'}
        </button>
      </form>

      {err && <div style={{ color: '#ef4444', fontSize: 13 }}>{err}</div>}

      {results !== null && (
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #262626', color: '#737373', textAlign: 'left' }}>
              <th style={th}>Name</th>
              <th style={th}>Aliases</th>
              <th style={th}>Source</th>
              <th style={th}>Nationality</th>
              <th style={th}>Type</th>
              <th style={th}>Score</th>
            </tr>
          </thead>
          <tbody>
            {results.map((r, i) => (
              <tr key={`${r.id}-${i}`} style={{ borderBottom: '1px solid #171717' }}>
                <td style={td}>{r.name}</td>
                <td style={{ ...td, fontSize: 12, color: '#a3a3a3' }}>
                  {r.aliases?.join(', ') || '-'}
                </td>
                <td style={td}>
                  <span
                    style={{
                      padding: '2px 8px',
                      borderRadius: 4,
                      fontSize: 11,
                      background: '#262626',
                      color: '#d4d4d4',
                      textTransform: 'uppercase',
                    }}
                  >
                    {r.list_id}
                  </span>
                </td>
                <td style={td}>{r.nationality || '-'}</td>
                <td style={td}>{r.type}</td>
                <td style={td}>
                  <span style={{ color: r.score >= 0.9 ? '#ef4444' : r.score >= 0.7 ? '#f59e0b' : '#a3a3a3' }}>
                    {r.score.toFixed(3)}
                  </span>
                </td>
              </tr>
            ))}
            {results.length === 0 && (
              <tr>
                <td colSpan={6} style={{ padding: '16px 12px', color: '#525252' }}>
                  No matches found
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}
    </div>
  )
}

const th: React.CSSProperties = { padding: '8px 12px', fontWeight: 500 }
const td: React.CSSProperties = { padding: '8px 12px' }

const inputStyle: React.CSSProperties = {
  padding: '8px 12px',
  borderRadius: 6,
  border: '1px solid #262626',
  background: '#0a0a0a',
  color: '#e5e5e5',
  fontSize: 14,
  outline: 'none',
  minWidth: 200,
}

const btnStyle: React.CSSProperties = {
  padding: '8px 20px',
  borderRadius: 6,
  border: 'none',
  cursor: 'pointer',
  fontSize: 14,
  fontWeight: 500,
  background: '#262626',
  color: '#fff',
}
