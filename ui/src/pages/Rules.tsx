import { useEffect, useState } from 'react'
import { listRules, testRule, type Rule, type RuleTestResult } from '../api'

export function Rules() {
  const [rules, setRules] = useState<Rule[]>([])
  const [err, setErr] = useState('')
  const [editId, setEditId] = useState<string | null>(null)
  const [testDsl, setTestDsl] = useState('')
  const [testResult, setTestResult] = useState<RuleTestResult | null>(null)
  const [testErr, setTestErr] = useState('')

  useEffect(() => {
    listRules()
      .then(setRules)
      .catch((e) => setErr(e.message))
  }, [])

  const handleTest = async () => {
    setTestErr('')
    setTestResult(null)
    try {
      const r = await testRule(testDsl)
      setTestResult(r)
    } catch (e: unknown) {
      setTestErr(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, color: '#fff' }}>Rules</h1>

      {err && <div style={{ color: '#ef4444', fontSize: 13 }}>{err}</div>}

      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #262626', color: '#737373', textAlign: 'left' }}>
            <th style={th}>Name</th>
            <th style={th}>DSL</th>
            <th style={th}>Severity</th>
            <th style={th}>Action</th>
            <th style={th}>Enabled</th>
          </tr>
        </thead>
        <tbody>
          {rules.map((r) => (
            <>
              <tr
                key={r.id}
                onClick={() => {
                  setEditId(editId === r.id ? null : r.id)
                  setTestDsl(r.dsl)
                  setTestResult(null)
                  setTestErr('')
                }}
                style={{ borderBottom: '1px solid #171717', cursor: 'pointer' }}
              >
                <td style={td}>{r.name}</td>
                <td style={{ ...td, fontFamily: 'monospace', fontSize: 12, maxWidth: 300, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {r.dsl}
                </td>
                <td style={td}>
                  <SeverityBadge severity={r.severity} />
                </td>
                <td style={td}>{r.action}</td>
                <td style={td}>
                  <span style={{ color: r.enabled ? '#22c55e' : '#525252' }}>
                    {r.enabled ? 'on' : 'off'}
                  </span>
                </td>
              </tr>
              {editId === r.id && (
                <tr key={`${r.id}-detail`}>
                  <td colSpan={5} style={{ padding: 16, background: '#0a0a0a' }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                      <div style={{ fontSize: 12, color: '#a3a3a3' }}>
                        <div>ID: {r.id}</div>
                        <div>Description: {r.description || '-'}</div>
                        <div>Weight: {r.weight}</div>
                        <div>Priority: {r.priority}</div>
                        {r.jurisdiction_filter?.length ? (
                          <div>Jurisdictions: {r.jurisdiction_filter.join(', ')}</div>
                        ) : null}
                        {r.asset_class_filter?.length ? (
                          <div>Asset classes: {r.asset_class_filter.join(', ')}</div>
                        ) : null}
                      </div>
                      <div>
                        <label style={{ fontSize: 12, color: '#737373', display: 'block', marginBottom: 4 }}>
                          Test DSL
                        </label>
                        <textarea
                          value={testDsl}
                          onChange={(e) => setTestDsl(e.target.value)}
                          style={{
                            width: '100%',
                            minHeight: 60,
                            background: '#171717',
                            border: '1px solid #262626',
                            borderRadius: 6,
                            color: '#e5e5e5',
                            fontFamily: 'monospace',
                            fontSize: 13,
                            padding: 8,
                            resize: 'vertical',
                          }}
                        />
                      </div>
                      <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                        <button onClick={handleTest} style={btnStyle}>
                          Test Rule
                        </button>
                        {testResult && (
                          <span style={{ fontSize: 13, color: testResult.match ? '#22c55e' : '#737373' }}>
                            {testResult.match ? 'Matched' : 'No match'}
                          </span>
                        )}
                        {testErr && (
                          <span style={{ fontSize: 13, color: '#ef4444' }}>{testErr}</span>
                        )}
                      </div>
                    </div>
                  </td>
                </tr>
              )}
            </>
          ))}
          {rules.length === 0 && (
            <tr>
              <td colSpan={5} style={{ padding: '16px 12px', color: '#525252' }}>
                No rules loaded
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

const btnStyle: React.CSSProperties = {
  padding: '6px 16px',
  borderRadius: 6,
  border: 'none',
  cursor: 'pointer',
  fontSize: 13,
  fontWeight: 500,
  background: '#262626',
  color: '#fff',
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
