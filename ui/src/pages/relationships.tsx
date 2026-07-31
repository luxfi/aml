// Relationships: whether a business relationship with this party is or was
// maintained, and its nature (AMLR Art. 78).
//
// The name never leaves as a name. It is tokenised into the pseudonym the
// records were indexed under, so the answer comes from an index lookup rather
// than a scan of the ledger — which is what "fully and speedily" has to mean —
// and the ledger never held the name in the clear to begin with. `examined` is
// the evidence of that: it counts this party's records, and does not grow with
// the size of the ledger.
//
// The graph draws the answer, not a guess at a network. Every node on it is a
// record the lookback returned; nothing is inferred, and no edge exists that the
// index did not answer with.

import { useState } from 'react'

import * as api from '../api'
import { Badge, Card, Empty, Fail, Field, Spinner, day } from '../ui'

export function Relationships() {
  const [party, setParty] = useState('')
  const [domain, setDomain] = useState<api.Domain>('name')
  const [answer, setAnswer] = useState<api.Lookback | null>(null)
  const [asked, setAsked] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [selected, setSelected] = useState<string | null>(null)

  const search = async () => {
    if (!party.trim()) return
    setBusy(true)
    setError(null)
    setAnswer(null)
    setSelected(null)
    try {
      setAnswer(await api.findRelationships(party.trim(), domain))
      setAsked(party.trim())
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Card title="Find a party">
        <div className="cols">
          <Field label="Party">
            <input
              type="text"
              value={party}
              placeholder="Name, customer id, account or wallet"
              onChange={(e) => setParty(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && void search()}
            />
          </Field>
          <Field label="Named as" hint="Which index the value is looked up in.">
            <select value={domain} onChange={(e) => setDomain(e.target.value as api.Domain)}>
              {api.domains.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <div className="row">
          <button className="btn primary" disabled={busy || !party.trim()} onClick={() => void search()}>
            {busy ? <Spinner /> : null}
            Search
          </button>
        </div>
        <Fail error={error} />
      </Card>

      {answer ? (
        <>
          <div className="tiles">
            <div className="tile">
              <div className="label">Maintained</div>
              <div className="value">{answer.maintained ? 'Yes' : 'No'}</div>
              <div className="cap">
                <Badge state={answer.maintained ? (answer.current ? 'warning' : 'plain') : 'good'}>
                  {answer.maintained ? (answer.current ? 'current' : 'within the window') : 'no relationship found'}
                </Badge>
              </div>
            </div>
            <div className="tile">
              <div className="label">Records</div>
              <div className="value">{(answer.records ?? []).length}</div>
              <div className="cap">{answer.examined} examined</div>
            </div>
            <div className="tile">
              <div className="label">Window</div>
              <div className="value">{day(answer.from)}</div>
              <div className="cap">to {day(answer.to)}</div>
            </div>
            <div className="tile">
              <div className="label">Nature</div>
              <div className="value">{(answer.natures ?? []).length}</div>
              <div className="cap">{(answer.natures ?? []).join(', ') || '—'}</div>
            </div>
          </div>

          <Card title="Relationships">
            <Graph
              party={asked}
              records={answer.records ?? []}
              natures={answer.natures ?? []}
              selected={selected}
              onSelect={setSelected}
            />
            {selected ? <Close id={selected} onClosed={() => void search()} /> : null}
            {!(answer.records ?? []).length ? (
              <Empty>The index holds no relationship for this party in the window.</Empty>
            ) : null}
          </Card>
        </>
      ) : null}

      <Open onOpened={() => void search()} />
    </>
  )
}

/**
 * A deterministic radial layout. Node kind is carried by shape and label, never
 * by hue: the graph reads the same to somebody who cannot separate colours.
 */
function Graph({
  party,
  records,
  natures,
  selected,
  onSelect,
}: {
  party: string
  records: string[]
  natures: string[]
  selected: string | null
  onSelect: (id: string) => void
}) {
  const w = 880
  const h = 420
  const cx = w / 2
  const cy = h / 2
  const r = Math.min(150, 60 + records.length * 12)

  const at = (i: number) => {
    const a = (i / Math.max(1, records.length)) * Math.PI * 2 - Math.PI / 2
    return { x: cx + Math.cos(a) * r, y: cy + Math.sin(a) * r }
  }

  return (
    <svg className="graph" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="Relationship graph">
      {records.map((id, i) => {
        const p = at(i)
        return <line key={`e${id}`} className="edge" x1={cx} y1={cy} x2={p.x} y2={p.y} />
      })}

      {records.map((id, i) => {
        const p = at(i)
        const on = selected === id
        return (
          <g key={id} onClick={() => onSelect(id)} tabIndex={0} role="button" aria-label={`Relationship ${id}`}>
            <rect
              className="node"
              x={p.x - 62}
              y={p.y - 16}
              width={124}
              height={32}
              rx={8}
              strokeWidth={on ? 2 : 1}
            />
            <text x={p.x} y={p.y - 1} textAnchor="middle">
              {natures[i] ?? 'relationship'}
            </text>
            <text x={p.x} y={p.y + 11} textAnchor="middle">
              {id.slice(0, 14)}
            </text>
          </g>
        )
      })}

      <circle className="node root" cx={cx} cy={cy} r={34} />
      <text className="root" x={cx} y={cy + 4} textAnchor="middle">
        {party.length > 12 ? `${party.slice(0, 11)}…` : party}
      </text>
    </svg>
  )
}

function Close({ id, onClosed }: { id: string; onClosed: () => void }) {
  const [ended, setEnded] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [started, setStarted] = useState<number | null>(null)

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const out = await api.closeRelationship(id, ended ? new Date(ended).toISOString() : undefined)
      setStarted(out.clocks_started)
      onClosed()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="End this relationship">
      <p className="hint">
        Ending it starts the retention clock on the relationship and on everything retained inside
        it. <span className="mono">{id}</span>
      </p>
      <div className="cols">
        <Field label="Ended">
          <input type="date" value={ended} onChange={(e) => setEnded(e.target.value)} />
        </Field>
      </div>
      <div className="row">
        <button className="btn danger" disabled={busy} onClick={() => void submit()}>
          {busy ? <Spinner /> : null}
          End relationship
        </button>
        {started !== null ? <Badge state="good">{started} retention clocks started</Badge> : null}
      </div>
      <Fail error={error} />
    </Card>
  )
}

function Open({ onOpened }: { onOpened: () => void }) {
  const [form, setForm] = useState<api.Opening>({ ref: '', nature: '', name: '', user_id: '', account_id: '', wallet: '' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [id, setID] = useState('')

  const set = (k: keyof api.Opening) => (v: string) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const out = await api.openRelationship(form)
      setID(out.relationship)
      onOpened()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="Open a relationship">
      <div className="cols">
        <Field label="Your reference" hint="Retained in the clear, so it must be synthetic — never a direct identifier.">
          <input type="text" value={form.ref} onChange={(e) => set('ref')(e.target.value)} placeholder="rel-2026-0181" />
        </Field>
        <Field label="Nature">
          <input type="text" value={form.nature} onChange={(e) => set('nature')(e.target.value)} placeholder="correspondent banking" />
        </Field>
        <Field label="Customer name">
          <input type="text" value={form.name ?? ''} onChange={(e) => set('name')(e.target.value)} />
        </Field>
        <Field label="Customer id">
          <input type="text" value={form.user_id ?? ''} onChange={(e) => set('user_id')(e.target.value)} />
        </Field>
        <Field label="Account">
          <input type="text" value={form.account_id ?? ''} onChange={(e) => set('account_id')(e.target.value)} />
        </Field>
        <Field label="Wallet">
          <input type="text" value={form.wallet ?? ''} onChange={(e) => set('wallet')(e.target.value)} />
        </Field>
      </div>
      <div className="row">
        <button className="btn" disabled={busy || !form.ref.trim() || !form.nature.trim()} onClick={() => void submit()}>
          {busy ? <Spinner /> : null}
          Open
        </button>
        {id ? <Badge state="good">opened {id}</Badge> : null}
      </div>
      <Fail error={error} />
    </Card>
  )
}
