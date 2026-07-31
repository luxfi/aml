// Cases: the queue, and the one case in front of you.
//
// The detail opens beside the queue rather than replacing it, because triage is
// a sequence — read, note, dispose, next — and a page that reloads between each
// one turns a queue into navigation.
//
// Closing a case demands a rationale and what was considered, because that is
// what the resolution has to carry: a dismissed alert is a retained decision
// with its reasoning (AMLR Art. 77(1)(b)), not a deleted row. The engine
// refuses a resolution without one; this asks for it up front rather than
// letting somebody discover the refusal after they have decided.

import { useState } from 'react'

import * as api from '../api'
import { Badge, Card, Empty, Fail, Field, Panel, Spinner, severity, useLoad, when } from '../ui'

const statuses = ['all', 'open', 'in_review', 'escalated', 'closed'] as const

export function Cases() {
  const [status, setStatus] = useState<string>('open')
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState<api.Case | null>(null)
  const list = useLoad(() => api.cases(status), [status])

  const rows = (list.data ?? []).filter((c) => {
    if (!query.trim()) return true
    const q = query.trim().toLowerCase()
    return (
      String(c.number).includes(q) ||
      c.id.toLowerCase().includes(q) ||
      (c.entity_ids ?? []).some((e) => e.toLowerCase().includes(q)) ||
      (c.alert_ids ?? []).some((a) => a.toLowerCase().includes(q))
    )
  })

  return (
    <>
      <Card
        title={`${rows.length} case${rows.length === 1 ? '' : 's'}`}
        actions={
          <>
            <div className="tabs">
              {statuses.map((s) => (
                <button key={s} aria-pressed={status === s} onClick={() => setStatus(s)}>
                  {s.replace(/_/g, ' ')}
                </button>
              ))}
            </div>
            <input
              type="text"
              placeholder="Find a case, entity or alert"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <button className="btn ghost small" onClick={() => void list.reload()}>
              Refresh
            </button>
          </>
        }
        flush
      >
        {list.error ? (
          <div className="body">
            <Fail error={list.error} />
          </div>
        ) : null}
        <div className="scroll">
          <table className="grid">
            <thead>
              <tr>
                <th>Case</th>
                <th>Severity</th>
                <th>Status</th>
                <th className="num">Alerts</th>
                <th>Entities</th>
                <th>Opened</th>
                <th>Resolution</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr
                  key={c.id}
                  className={`clickable ${severity(c.severity)}`}
                  aria-selected={open?.id === c.id}
                  onClick={() => setOpen(c)}
                >
                  <td className="id">#{c.number}</td>
                  <td>
                    <Badge state={severity(c.severity)}>{c.severity}</Badge>
                  </td>
                  <td>{c.status.replace(/_/g, ' ')}</td>
                  <td className="num">{c.alert_ids?.length ?? 0}</td>
                  <td className="id">{(c.entity_ids ?? []).join(', ') || '—'}</td>
                  <td>{when(c.opened_at)}</td>
                  <td>{c.resolution ? c.resolution.replace(/_/g, ' ') : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {list.busy ? (
            <div className="empty">
              <Spinner />
            </div>
          ) : null}
          {!list.busy && !rows.length ? <Empty>No case matches.</Empty> : null}
        </div>
      </Card>

      {open ? (
        <Detail
          c={open}
          onClose={() => setOpen(null)}
          onChanged={() => {
            void list.reload()
            setOpen(null)
          }}
        />
      ) : null}
    </>
  )
}

function Detail({
  c,
  onClose,
  onChanged,
}: {
  c: api.Case
  onClose: () => void
  onChanged: () => void
}) {
  const timeline = useLoad(() => api.caseEvents(c.id), [c.id])
  const [closing, setClosing] = useState(false)

  return (
    <Panel
      title={`Case #${c.number}`}
      onClose={onClose}
      actions={
        c.status !== 'closed' ? (
          <button className="btn small" onClick={() => setClosing((v) => !v)}>
            {closing ? 'Cancel' : 'Resolve'}
          </button>
        ) : null
      }
    >
      <div className="row">
        <Badge state={severity(c.severity)}>{c.severity}</Badge>
        <Badge state={c.status === 'closed' ? 'plain' : 'warning'}>
          {c.status.replace(/_/g, ' ')}
        </Badge>
      </div>

      <dl className="kv">
        <dt>Case</dt>
        <dd className="mono">{c.id}</dd>
        <dt>Opened</dt>
        <dd>{when(c.opened_at)}</dd>
        {c.closed_at ? (
          <>
            <dt>Closed</dt>
            <dd>{when(c.closed_at)}</dd>
          </>
        ) : null}
        <dt>Entities</dt>
        <dd className="mono">{(c.entity_ids ?? []).join(', ') || '—'}</dd>
        <dt>Alerts</dt>
        <dd className="mono">{(c.alert_ids ?? []).join(', ') || '—'}</dd>
        {c.resolution ? (
          <>
            <dt>Resolution</dt>
            <dd>{c.resolution.replace(/_/g, ' ')}</dd>
          </>
        ) : null}
        {c.assessment ? (
          <>
            <dt>Assessment</dt>
            <dd className="mono">{c.assessment}</dd>
          </>
        ) : null}
      </dl>

      {closing ? <Resolve c={c} onDone={onChanged} /> : null}

      <Card title="Timeline" flush>
        <div className="body">
          <Fail error={timeline.error} />
          {timeline.busy ? <Spinner /> : null}
          {!timeline.busy && !(timeline.data ?? []).length ? (
            <Empty>Nothing has been recorded on this case yet.</Empty>
          ) : null}
          <div className="timeline">
            {(timeline.data ?? []).map((e) => (
              <div className="event" key={e.id}>
                <div className="track">
                  <i className="pip" />
                  <i className="line" />
                </div>
                <div className="content">
                  <div className="meta">
                    <span className="kind">{e.kind.replace(/_/g, ' ')}</span>
                    <span>{when(e.created_at)}</span>
                    {e.author_id ? <span className="mono">{e.author_id}</span> : null}
                  </div>
                  {e.body ? <div>{e.body}</div> : null}
                </div>
              </div>
            ))}
          </div>
          <AddNote id={c.id} onAdded={() => void timeline.reload()} />
        </div>
      </Card>
    </Panel>
  )
}

function AddNote({ id, onAdded }: { id: string; onAdded: () => void }) {
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const submit = async () => {
    if (!body.trim()) return
    setBusy(true)
    setError(null)
    try {
      await api.addEvent(id, { kind: 'note', body: body.trim() })
      setBody('')
      onAdded()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Fail error={error} />
      <Field label="Add a note">
        <textarea
          rows={3}
          value={body}
          placeholder="What was checked, what was found, what happens next"
          onChange={(e) => setBody(e.target.value)}
        />
      </Field>
      <div className="row">
        <button className="btn" disabled={busy || !body.trim()} onClick={() => void submit()}>
          {busy ? <Spinner /> : null}
          Record note
        </button>
      </div>
    </>
  )
}

const resolutions = [
  { id: 'cleared', label: 'Cleared' },
  { id: 'false_positive', label: 'False positive' },
  { id: 'sar_filed', label: 'Report filed' },
  { id: 'account_frozen', label: 'Account frozen' },
]

function Resolve({ c, onDone }: { c: api.Case; onDone: () => void }) {
  const [resolution, setResolution] = useState(resolutions[0].id)
  const [rationale, setRationale] = useState('')
  const [considered, setConsidered] = useState('')
  const [by, setBy] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.resolve(c.id, {
        resolution,
        rationale: rationale.trim(),
        considered: considered
          .split(/[\n,]/)
          .map((s) => s.trim())
          .filter(Boolean),
        by: by.trim(),
      })
      onDone()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="Resolve">
      <Fail error={error} />
      <Field label="Resolution">
        <select value={resolution} onChange={(e) => setResolution(e.target.value)}>
          {resolutions.map((r) => (
            <option key={r.id} value={r.id}>
              {r.label}
            </option>
          ))}
        </select>
      </Field>
      <Field
        label="Rationale"
        hint="Retained with the decision. This is the record that the alert was considered."
      >
        <textarea
          rows={3}
          value={rationale}
          onChange={(e) => setRationale(e.target.value)}
          placeholder="Why this disposal, on what evidence"
        />
      </Field>
      <Field label="Considered" hint="What was examined, one per line.">
        <textarea
          rows={2}
          value={considered}
          onChange={(e) => setConsidered(e.target.value)}
          placeholder={'account history\nsource of funds document'}
        />
      </Field>
      <Field label="Decided by">
        <input type="text" value={by} onChange={(e) => setBy(e.target.value)} placeholder="Who is accountable for this decision" />
      </Field>
      <div className="row">
        <button
          className="btn primary"
          disabled={busy || !rationale.trim() || !by.trim()}
          onClick={() => void submit()}
        >
          {busy ? <Spinner /> : null}
          Close case
        </button>
      </div>
    </Card>
  )
}
