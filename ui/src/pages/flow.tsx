// Transactions: put one through, or read back what a rule said about one.
//
// Two buttons on one form, and the difference between them is the whole point.
// Scoring asks the behavioural model what it makes of a transaction and touches
// nothing — no aggregate moves, no record is written, nothing is learned from
// it. Submitting is the live path: it converts the amount, records the
// transaction in history BEFORE judging it against history, retains a sealed
// record, and opens a case if the decision warrants one. A console that made
// those look alike would let somebody write to the record plane while they
// thought they were experimenting.

import { useState } from 'react'

import * as api from '../api'
import { Badge, Card, Empty, Fail, Field, Meter, Spinner, action, severity, useLoad, when } from '../ui'

const blank: api.Transaction = {
  user_id: '',
  account_id: '',
  notional: 0,
  currency: 'USD',
  counterparty: '',
  side: 'in',
  customer_jurisdiction: '',
  device_fingerprint: '',
}

export function Flow() {
  const [tx, setTx] = useState<api.Transaction>(blank)
  const [entity, setEntity] = useState<api.Entity>({ name: '', jurisdiction: '' })
  const [outcome, setOutcome] = useState<api.Evaluation | null>(null)
  const [score, setScore] = useState<Record<string, unknown> | null>(null)
  const [busy, setBusy] = useState<'' | 'score' | 'send'>('')
  const [error, setError] = useState<unknown>(null)

  const field = (k: keyof api.Transaction) => (v: string) =>
    setTx((t) => ({ ...t, [k]: k === 'notional' ? Number(v) : v }))

  const run = async (what: 'score' | 'send') => {
    setBusy(what)
    setError(null)
    setOutcome(null)
    setScore(null)
    try {
      if (what === 'score') setScore(await api.scoreTest(tx, entity))
      else setOutcome(await api.ingest(tx, entity))
    } catch (e) {
      setError(e)
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="split">
      <div className="stack">
        <Card title="Transaction">
          <div className="cols">
            <Field label="Customer id">
              <input type="text" value={tx.user_id} onChange={(e) => field('user_id')(e.target.value)} placeholder="cust-1042" />
            </Field>
            <Field label="Account id">
              <input type="text" value={tx.account_id ?? ''} onChange={(e) => field('account_id')(e.target.value)} />
            </Field>
            <Field label="Amount">
              <input type="number" value={tx.notional || ''} onChange={(e) => field('notional')(e.target.value)} />
            </Field>
            <Field label="Currency" hint="Converted server-side. An unconvertible currency is refused, not passed through.">
              <input type="text" value={tx.currency} onChange={(e) => field('currency')(e.target.value.toUpperCase())} />
            </Field>
            <Field label="Direction">
              <select value={tx.side ?? 'in'} onChange={(e) => field('side')(e.target.value)}>
                <option value="in">in</option>
                <option value="out">out</option>
              </select>
            </Field>
            <Field label="Counterparty">
              <input type="text" value={tx.counterparty ?? ''} onChange={(e) => field('counterparty')(e.target.value)} />
            </Field>
            <Field label="Device fingerprint">
              <input type="text" value={tx.device_fingerprint ?? ''} onChange={(e) => field('device_fingerprint')(e.target.value)} />
            </Field>
            <Field label="Transaction jurisdiction">
              <input type="text" value={tx.customer_jurisdiction ?? ''} onChange={(e) => field('customer_jurisdiction')(e.target.value)} placeholder="GB" />
            </Field>
          </div>
        </Card>

        <Card title="Customer">
          <div className="cols">
            <Field
              label="Name"
              hint="Screening has no input without it. A customer id is not a name, and the engine will not treat one as though it were."
            >
              <input type="text" value={entity.name ?? ''} onChange={(e) => setEntity((x) => ({ ...x, name: e.target.value }))} />
            </Field>
            <Field label="Jurisdiction">
              <input type="text" value={entity.jurisdiction ?? ''} onChange={(e) => setEntity((x) => ({ ...x, jurisdiction: e.target.value }))} placeholder="IR" />
            </Field>
          </div>
          <div className="row">
            <button className="btn" disabled={!!busy} onClick={() => void run('score')}>
              {busy === 'score' ? <Spinner /> : null}
              Score without recording
            </button>
            <button className="btn primary" disabled={!!busy || !tx.user_id || !tx.notional} onClick={() => void run('send')}>
              {busy === 'send' ? <Spinner /> : null}
              Submit and record
            </button>
          </div>
          <Fail error={error} />
        </Card>

        {outcome ? <Outcome outcome={outcome} /> : null}
        {score ? <Score score={score} /> : null}
      </div>

      <div className="stack">
        <Alerts />
      </div>
    </div>
  )
}

function Outcome({ outcome }: { outcome: api.Evaluation }) {
  return (
    <Card title="Decision">
      <div className="row">
        <Badge state={action(outcome.action)}>{outcome.action}</Badge>
        <Meter value={outcome.score} state={action(outcome.action)} />
        <span className="mono">{outcome.score.toFixed(3)}</span>
      </div>
      <dl className="kv">
        <dt>Alerts</dt>
        <dd className="mono">{(outcome.alert_ids ?? []).join(', ') || 'none'}</dd>
        <dt>Case</dt>
        <dd className="mono">{outcome.case_id || 'none opened'}</dd>
        <dt>Record</dt>
        <dd className="mono">{outcome.record}</dd>
      </dl>
    </Card>
  )
}

function Score({ score }: { score: Record<string, unknown> }) {
  return (
    <Card title="Model inspection">
      <p className="hint">
        Every coordinate the model read, including the ones that contributed nothing. What it
        looked at is as much part of the account as what it concluded.
      </p>
      <pre className="code">{JSON.stringify(score, null, 2)}</pre>
    </Card>
  )
}

function Alerts() {
  const [id, setID] = useState('')
  const [asked, setAsked] = useState('')
  const found = useLoad(async () => (asked ? api.alerts(asked) : []), [asked])

  return (
    <Card
      title="Alerts by transaction"
      actions={
        <>
          <input
            type="text"
            value={id}
            placeholder="Transaction id"
            onChange={(e) => setID(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && setAsked(id.trim())}
          />
          <button className="btn small" disabled={!id.trim()} onClick={() => setAsked(id.trim())}>
            Look up
          </button>
        </>
      }
      flush
    >
      {found.error ? (
        <div className="body">
          <Fail error={found.error} />
        </div>
      ) : null}
      <div className="scroll">
        <table className="grid">
          <thead>
            <tr>
              <th>Rule</th>
              <th>Severity</th>
              <th className="num">Score</th>
              <th>Action</th>
              <th>Raised</th>
            </tr>
          </thead>
          <tbody>
            {(found.data ?? []).map((a) => (
              <tr key={a.id} className={severity(a.severity)}>
                <td>
                  <div>{a.rule_name}</div>
                  <div className="id">{a.typology?.replace(/_/g, ' ')}</div>
                  {a.eval_error ? <div className="id">error: {a.eval_error}</div> : null}
                </td>
                <td>
                  <Badge state={severity(a.severity)}>{a.severity}</Badge>
                </td>
                <td className="num">
                  <Meter value={a.score} state={severity(a.severity)} width={48} />
                </td>
                <td>{a.action_taken}</td>
                <td>{when(a.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {found.busy ? (
          <div className="empty">
            <Spinner />
          </div>
        ) : null}
        {!found.busy && asked && !(found.data ?? []).length ? (
          <Empty>No alert was raised on {asked}.</Empty>
        ) : null}
        {!asked ? <Empty>Enter a transaction id to read its alerts.</Empty> : null}
      </div>
    </Card>
  )
}
