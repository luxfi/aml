// Screening: is this the same person?
//
// A score cannot be cleared. The question an analyst has to answer is whether
// the party in front of them is the designation, and that is decided on the
// identifiers that agree and the ones that disagree — so every hit shows both,
// beside the reason it matched, and the score is the least of it.
//
// The readiness panel is not decoration. A list that has stopped loading
// returns no matches, and no matches is exactly what a clean party returns:
// without the count and the date, "nobody on this payment is designated" and
// "the list is empty" are the same screen.

import { useState } from 'react'

import * as api from '../api'
import { Badge, Card, Empty, Fail, Field, Meter, Spinner, useLoad, when } from '../ui'

export function Sanctions() {
  const [name, setName] = useState('')
  const [dob, setDOB] = useState('')
  const [hits, setHits] = useState<api.Hit[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const screening = useLoad(() => api.sources())

  const search = async () => {
    if (!name.trim()) return
    setBusy(true)
    setError(null)
    setHits(null)
    try {
      setHits(await api.screen(name.trim(), dob.trim()))
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Card title="Screen a party">
        <div className="cols">
          <Field label="Name">
            <input
              type="text"
              value={name}
              placeholder="As it appears on the instruction"
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && void search()}
            />
          </Field>
          <Field label="Date of birth" hint="Optional. Corroborates or contradicts a name match.">
            <input type="text" value={dob} placeholder="1974-03-02" onChange={(e) => setDOB(e.target.value)} />
          </Field>
        </div>
        <div className="row">
          <button className="btn primary" disabled={busy || !name.trim()} onClick={() => void search()}>
            {busy ? <Spinner /> : null}
            Search
          </button>
          {screening.data && !screening.data.ready ? (
            <Badge state="critical">
              screening is unfit: {screening.data.unfit.join(', ')}
            </Badge>
          ) : null}
        </div>
        <Fail error={error} />
      </Card>

      {hits ? (
        <Card title={`${hits.length} match${hits.length === 1 ? '' : 'es'}`} flush>
          <div className="scroll">
            <table className="grid">
              <thead>
                <tr>
                  <th>Designation</th>
                  <th>List</th>
                  <th className="num">Score</th>
                  <th>Agrees</th>
                  <th>Conflicts</th>
                  <th>Programs</th>
                </tr>
              </thead>
              <tbody>
                {hits.map((h) => (
                  <tr key={`${h.list}:${h.ref_id}`} className={h.conflict?.length ? 'warning' : 'critical'}>
                    <td>
                      <div>{h.name}</div>
                      <div className="id">
                        {h.kind} · {h.ref_id} · {h.reason}
                      </div>
                    </td>
                    <td>{h.list}</td>
                    <td className="num">
                      <Meter value={h.score} state={h.score > 0.9 ? 'critical' : 'serious'} width={48} />
                      <div className="id">{h.score.toFixed(3)}</div>
                    </td>
                    <td>{(h.agree ?? []).join(', ') || '—'}</td>
                    <td>{(h.conflict ?? []).join(', ') || '—'}</td>
                    <td>{(h.programs ?? []).join(', ') || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!hits.length ? (
              <Empty>
                No designation matched.
                {screening.data ? (
                  <span className="hint">
                    Screened against {screening.data.total_entries.toLocaleString()} designations across{' '}
                    {screening.data.sources.length} lists.
                  </span>
                ) : null}
              </Empty>
            ) : null}
          </div>
        </Card>
      ) : null}

      <Card
        title="Lists"
        actions={
          <button className="btn ghost small" onClick={() => void screening.reload()}>
            Refresh
          </button>
        }
        flush
      >
        {screening.error ? (
          <div className="body">
            <Fail error={screening.error} />
          </div>
        ) : null}
        <div className="scroll">
          <table className="grid">
            <thead>
              <tr>
                <th>Source</th>
                <th className="num">Designations</th>
                <th>Loaded</th>
                <th className="num">Age</th>
                <th>State</th>
                <th>Digest</th>
              </tr>
            </thead>
            <tbody>
              {(screening.data?.sources ?? []).map((s) => (
                <tr key={s.source} className={s.fresh ? 'good' : 'critical'}>
                  <td>{s.source}</td>
                  <td className="num">{s.entries.toLocaleString()}</td>
                  <td>{when(s.loaded_at)}</td>
                  <td className="num">{s.loaded_at ? `${s.age_hours.toFixed(1)}h` : '—'}</td>
                  <td>
                    <Badge state={s.fresh ? 'good' : 'critical'}>
                      {s.fresh ? 'fresh' : s.error ? 'failing' : 'stale'}
                    </Badge>
                    {s.error ? <div className="id">{s.error}</div> : null}
                  </td>
                  <td className="id">{s.sha256 ? s.sha256.slice(0, 12) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {screening.busy ? (
            <div className="empty">
              <Spinner />
            </div>
          ) : null}
        </div>
      </Card>
    </>
  )
}
