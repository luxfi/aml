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
import {
  Badge,
  Body,
  Button,
  Card,
  Code,
  Col,
  Cols,
  Empty,
  Fail,
  Field,
  Hint,
  Input,
  Kv,
  Meter,
  Mono,
  Row,
  Scroll,
  Select,
  SizableText,
  Spinner,
  Split,
  Stack,
  Sub,
  action,
  severity,
  useLoad,
  when,
} from '../ui'

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
    <Split>
      <Stack>
        <Card title="Transaction">
          <Cols>
            <Field label="Customer id">
              <Input type="text" value={tx.user_id} onChange={(e) => field('user_id')(e.target.value)} placeholder="cust-1042" />
            </Field>
            <Field label="Account id">
              <Input type="text" value={tx.account_id ?? ''} onChange={(e) => field('account_id')(e.target.value)} />
            </Field>
            <Field label="Amount">
              <Input type="number" value={tx.notional || ''} onChange={(e) => field('notional')(e.target.value)} />
            </Field>
            <Field label="Currency" hint="Converted server-side. An unconvertible currency is refused, not passed through.">
              <Input type="text" value={tx.currency} onChange={(e) => field('currency')(e.target.value.toUpperCase())} />
            </Field>
            <Field label="Direction">
              <Select label="Direction" value={tx.side ?? 'in'} options={['in', 'out']} onChange={field('side')} />
            </Field>
            <Field label="Counterparty">
              <Input type="text" value={tx.counterparty ?? ''} onChange={(e) => field('counterparty')(e.target.value)} />
            </Field>
            <Field label="Device fingerprint">
              <Input type="text" value={tx.device_fingerprint ?? ''} onChange={(e) => field('device_fingerprint')(e.target.value)} />
            </Field>
            <Field label="Transaction jurisdiction">
              <Input type="text" value={tx.customer_jurisdiction ?? ''} onChange={(e) => field('customer_jurisdiction')(e.target.value)} placeholder="GB" />
            </Field>
          </Cols>
        </Card>

        <Card title="Customer">
          <Cols>
            <Field
              label="Name"
              hint="Screening has no input without it. A customer id is not a name, and the engine will not treat one as though it were."
            >
              <Input type="text" value={entity.name ?? ''} onChange={(e) => setEntity((x) => ({ ...x, name: e.target.value }))} />
            </Field>
            <Field label="Jurisdiction">
              <Input type="text" value={entity.jurisdiction ?? ''} onChange={(e) => setEntity((x) => ({ ...x, jurisdiction: e.target.value }))} placeholder="IR" />
            </Field>
          </Cols>
          <Row wrap>
            <Button
              busy={busy === 'score'}
              disabled={!!busy}
              onPress={() => void run('score')}
            >
              Score without recording
            </Button>
            <Button
              tone="primary"
              busy={busy === 'send'}
              disabled={!!busy || !tx.user_id || !tx.notional}
              onPress={() => void run('send')}
            >
              Submit and record
            </Button>
          </Row>
          <Fail error={error} />
        </Card>

        {outcome ? <Outcome outcome={outcome} /> : null}
        {score ? <Score score={score} /> : null}
      </Stack>

      <Stack>
        <Alerts />
      </Stack>
    </Split>
  )
}

function Outcome({ outcome }: { outcome: api.Evaluation }) {
  return (
    <Card title="Decision">
      <Row wrap>
        <Badge state={action(outcome.action)}>{outcome.action}</Badge>
        <Meter value={outcome.score} state={action(outcome.action)} />
        <Mono>{outcome.score.toFixed(3)}</Mono>
      </Row>
      <Kv>
        <dt>Alerts</dt>
        <dd className="mono">{(outcome.alert_ids ?? []).join(', ') || 'none'}</dd>
        <dt>Case</dt>
        <dd className="mono">{outcome.case_id || 'none opened'}</dd>
        <dt>Record</dt>
        <dd className="mono">{outcome.record}</dd>
      </Kv>
    </Card>
  )
}

function Score({ score }: { score: Record<string, unknown> }) {
  return (
    <Card title="Model inspection">
      <Hint>
        Every coordinate the model read, including the ones that contributed nothing. What it
        looked at is as much part of the account as what it concluded.
      </Hint>
      <Code>{JSON.stringify(score, null, 2)}</Code>
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
          <Input
            type="text"
            value={id}
            placeholder="Transaction id"
            onChange={(e) => setID(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && setAsked(id.trim())}
          />
          <Button disabled={!id.trim()} onPress={() => setAsked(id.trim())}>
            Look up
          </Button>
        </>
      }
      flush
    >
      {found.error ? (
        <Body>
          <Fail error={found.error} />
        </Body>
      ) : null}
      <Scroll>
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
                  <Col gap={2}>
                    <SizableText size={13}>{a.rule_name}</SizableText>
                    <Sub>{a.typology?.replace(/_/g, ' ')}</Sub>
                    {a.eval_error ? <Sub>error: {a.eval_error}</Sub> : null}
                  </Col>
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
          <Empty>
            <Spinner />
          </Empty>
        ) : null}
        {!found.busy && asked && !(found.data ?? []).length ? (
          <Empty>No alert was raised on {asked}.</Empty>
        ) : null}
        {!asked ? <Empty>Enter a transaction id to read its alerts.</Empty> : null}
      </Scroll>
    </Card>
  )
}
