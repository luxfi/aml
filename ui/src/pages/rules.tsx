// The rule builder.
//
// Two views of one expression. The tree composes terms the engine actually has
// — the vocabulary below is the engine's provider table, so a rule this builder
// emits cannot name evidence the deployment cannot supply — and the code view
// is the expression itself, which is what gets tested and what gets installed.
//
// The tree emits code; it does not read it back. A hand-written expression stays
// hand-written, and the builder says so rather than silently reformatting it
// into something that only looks equivalent. Writing a parser for the second
// direction would be a second definition of the language, and the engine is
// already the first.
//
// Nothing here is saved. The engine serves its rule set and replays candidates
// against retained history; installing one is a deployment's decision, not a
// console's, so this ends at the evidence a reviewer needs to make it.

import { useMemo, useState } from 'react'

import * as api from '../api'
import {
  Badge,
  Body,
  Button,
  Card,
  Code,
  Col,
  Empty,
  Fail,
  Frame,
  Field,
  Grow,
  Hint,
  Input,
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
  Tab,
  Tabs,
  TextArea,
  Tile,
  Tiles,
  pct,
  severity,
  useLoad,
} from '../ui'

// ── The language ────────────────────────────────────────────────────────────

type ArgKind = 'subject' | 'dimension' | 'window' | 'number' | 'text' | 'expr'
type Arg = { name: string; kind: ArgKind; value: string }
type Returns = 'number' | 'bool' | 'text'

type Term = {
  id: string
  label: string
  returns: Returns
  args: Arg[]
  /** Bare field references are not calls, so they take no parentheses. */
  field?: boolean
  hint?: string
}

const subjects = ['user', 'account', 'counterparty', 'device', 'address']
const dimensions = ['counterparty', 'account', 'device', 'address', 'jurisdiction', 'currency', 'symbol']
const windows = ['1h', '24h', '7d', '30d', '90d', '365d', '730d']

const arg = (name: string, kind: ArgKind, value: string): Arg => ({ name, kind, value })

const terms: Term[] = [
  { id: 'USD', label: 'Amount in USD', returns: 'number', args: [], hint: 'this transaction, converted' },
  { id: 'Day', label: 'Same-day total', returns: 'number', args: [arg('subject', 'subject', 'user')] },
  { id: 'Count', label: 'Transaction count', returns: 'number', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '24h')] },
  { id: 'Sum', label: 'Total value', returns: 'number', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '24h')] },
  { id: 'Max', label: 'Largest single value', returns: 'number', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '30d')] },
  { id: 'Distinct', label: 'Distinct values seen', returns: 'number', args: [arg('subject', 'subject', 'user'), arg('of', 'dimension', 'counterparty'), arg('window', 'window', '24h')] },
  { id: 'Dormant', label: 'Days dormant before this', returns: 'number', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '730d')] },
  { id: 'Round', label: 'Proportion of round amounts', returns: 'number', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '30d'), arg('unit', 'number', '1000.0')] },
  { id: 'Deviation', label: 'Deviation from normal', returns: 'number', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '90d'), arg('min events', 'number', '10')], hint: 'in standard deviations' },
  { id: 'Structured', label: 'Structured under a threshold', returns: 'bool', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '24h'), arg('threshold', 'number', '10000.0'), arg('at least', 'number', '3')] },
  { id: 'InOut', label: 'In and out again', returns: 'bool', args: [arg('subject', 'subject', 'user'), arg('window', 'window', '24h'), arg('min', 'number', '10000.0'), arg('residue', 'number', '0.05')] },
  { id: 'Near', label: 'Just under a limit', returns: 'bool', args: [arg('threshold', 'number', '10000.0'), arg('band', 'number', '0.1')] },
  { id: 'Screened', label: 'Screens against a list', returns: 'bool', args: [arg('name', 'expr', 'Entity.Name'), arg('list', 'text', 'sanctions')] },
  { id: 'Tier', label: 'Jurisdiction tier', returns: 'text', args: [arg('code', 'expr', 'Entity.Jurisdiction')] },
  { id: 'Tx.Notional', label: 'Transaction: notional', returns: 'number', args: [], field: true },
  { id: 'Tx.Currency', label: 'Transaction: currency', returns: 'text', args: [], field: true },
  { id: 'Tx.Side', label: 'Transaction: side', returns: 'text', args: [], field: true },
  { id: 'Tx.Symbol', label: 'Transaction: symbol', returns: 'text', args: [], field: true },
  { id: 'Tx.Counterparty', label: 'Transaction: counterparty', returns: 'text', args: [], field: true },
  { id: 'Tx.DeviceFingerprint', label: 'Transaction: device', returns: 'text', args: [], field: true },
  { id: 'Tx.CustomerJurisdiction', label: 'Transaction: jurisdiction', returns: 'text', args: [], field: true },
  { id: 'Entity.Name', label: 'Customer: name', returns: 'text', args: [], field: true },
  { id: 'Entity.Jurisdiction', label: 'Customer: jurisdiction', returns: 'text', args: [], field: true },
  { id: 'Entity.KYCLevel', label: 'Customer: KYC level', returns: 'number', args: [], field: true },
  { id: 'Entity.RiskScore', label: 'Customer: risk score', returns: 'number', args: [], field: true },
  { id: 'Entity.PEP', label: 'Customer: politically exposed', returns: 'bool', args: [], field: true },
  { id: 'Entity.SanctionsFlag', label: 'Customer: sanctions flag', returns: 'bool', args: [], field: true },
]

const byID = new Map(terms.map((t) => [t.id, t]))

const comparators: Record<Returns, string[]> = {
  number: ['>', '>=', '<', '<=', '==', '!='],
  text: ['==', '!='],
  bool: [],
}

// ── The tree ────────────────────────────────────────────────────────────────

type Cond = { at: 'cond'; key: string; term: string; args: string[]; op: string; value: string }
type Group = { at: 'group'; key: string; join: 'and' | 'or'; negate: boolean; kids: Node[] }
type Node = Cond | Group

let seq = 0
const key = () => `n${++seq}`

const condOf = (id: string): Cond => {
  const t = byID.get(id)!
  return {
    at: 'cond',
    key: key(),
    term: id,
    args: t.args.map((a) => a.value),
    op: comparators[t.returns][0] ?? '',
    value: t.returns === 'number' ? '10000.0' : t.returns === 'text' ? '' : '',
  }
}

const groupOf = (): Group => ({ at: 'group', key: key(), join: 'and', negate: false, kids: [condOf('USD')] })

const quote = (s: string) => `"${s.replace(/["\\]/g, '\\$&')}"`

function callOf(c: Cond): string {
  const t = byID.get(c.term)
  if (!t) return 'false'
  if (t.field || !t.args.length) return t.field ? t.id : `${t.id}()`
  const args = t.args.map((a, i) => {
    const v = c.args[i] ?? a.value
    return a.kind === 'number' || a.kind === 'expr' ? v : quote(v)
  })
  return `${t.id}(${args.join(', ')})`
}

/** emit turns the tree into the expression the engine compiles. */
function emit(n: Node): string {
  if (n.at === 'cond') {
    const t = byID.get(n.term)
    if (!t) return 'false'
    const call = callOf(n)
    if (t.returns === 'bool') return call
    const rhs = t.returns === 'number' ? (n.value.trim() || '0') : quote(n.value)
    return `${call} ${n.op} ${rhs}`
  }
  const inner = n.kids.map(emit).filter(Boolean)
  if (!inner.length) return ''
  const joined = inner.join(n.join === 'and' ? ' && ' : ' || ')
  const wrapped = inner.length > 1 ? `(${joined})` : joined
  return n.negate ? `!(${joined})` : wrapped
}

const replace = (n: Node, k: string, fn: (n: Node) => Node | null): Node | null => {
  if (n.key === k) return fn(n)
  if (n.at === 'group') {
    return { ...n, kids: n.kids.map((c) => replace(c, k, fn)).filter((c): c is Node => c !== null) }
  }
  return n
}

// ── The screen ──────────────────────────────────────────────────────────────

export function Rules() {
  const installed = useLoad(() => api.rules())
  const [root, setRoot] = useState<Group>(() => groupOf())
  const [handwritten, setHandwritten] = useState<string | null>(null)
  const [incumbent, setIncumbent] = useState('')
  const [report, setReport] = useState<api.Replay | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const built = useMemo(() => emit(root), [root])
  const dsl = handwritten ?? built

  const edit = (k: string, fn: (n: Node) => Node | null) =>
    setRoot((r) => (replace(r, k, fn) as Group) ?? r)

  const test = async () => {
    setBusy(true)
    setError(null)
    setReport(null)
    try {
      setReport(await api.testRule(dsl, incumbent))
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Split>
      <Stack>
        <Card
          title="Expression"
          actions={
            <Tabs>
              <Tab on={handwritten === null} onPress={() => setHandwritten(null)}>
                Builder
              </Tab>
              <Tab on={handwritten !== null} onPress={() => setHandwritten(built)}>
                Code
              </Tab>
            </Tabs>
          }
        >
          {handwritten === null ? (
            <GroupEditor node={root} depth={0} edit={edit} />
          ) : (
            <>
              <Field
                label="Expression"
                hint="Hand-written. The builder emits code; it does not read it back, so switching to Builder starts from the tree as you left it."
              >
                <TextArea rows={6} value={handwritten} onChangeText={setHandwritten} />
              </Field>
            </>
          )}
          <Code>{dsl || '// nothing to evaluate yet'}</Code>
          <Row wrap>
            <Field label="Replaces (optional)">
              <Select
                label="Replaces"
                value={incumbent}
                options={[
                  { value: '', label: 'nothing — this is a new rule' },
                  ...(installed.data ?? []).map((r) => ({ value: r.id, label: r.name })),
                ]}
                onChange={setIncumbent}
              />
            </Field>
          </Row>
          <Row wrap>
            <Button
              tone="primary"
              busy={busy}
              disabled={busy || !dsl.trim()}
              onPress={() => void test()}
            >
              Replay over history
            </Button>
            <Hint>Reads the org&apos;s retained transactions. Writes nothing.</Hint>
          </Row>
          <Fail error={error} />
        </Card>

        {report ? <Report report={report} /> : null}
      </Stack>

      <Stack>
        <Card title="Installed rules" flush>
          {installed.error ? (
            <Body>
              <Fail error={installed.error} />
            </Body>
          ) : null}
          <Scroll>
            <table className="grid">
              <thead>
                <tr>
                  <th>Rule</th>
                  <th>Typology</th>
                  <th>Severity</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody>
                {(installed.data ?? []).map((r) => (
                  <tr
                    key={r.id}
                    className={`clickable ${severity(r.severity)}`}
                    onClick={() => {
                      setHandwritten(r.dsl)
                      setIncumbent(r.id)
                    }}
                  >
                    <td>
                      <Col gap={2}>
                        <SizableText size={13}>{r.name}</SizableText>
                        <Sub>{r.dsl}</Sub>
                      </Col>
                    </td>
                    <td>{(r.typology ?? '').replace(/_/g, ' ')}</td>
                    <td>
                      <Badge state={severity(r.severity)}>{r.severity}</Badge>
                    </td>
                    <td>{r.action}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {installed.busy ? (
              <Empty>
                <Spinner />
              </Empty>
            ) : null}
            {!installed.busy && !(installed.data ?? []).length ? (
              <Empty>No rule is installed.</Empty>
            ) : null}
          </Scroll>
        </Card>
      </Stack>
    </Split>
  )
}

function GroupEditor({
  node,
  depth,
  edit,
}: {
  node: Group
  depth: number
  edit: (k: string, fn: (n: Node) => Node | null) => void
}) {
  return (
    <Frame>
      <Row wrap>
        <Tabs>
          <Tab
            on={node.join === 'and'}
            onPress={() => edit(node.key, (n) => ({ ...(n as Group), join: 'and' }))}
          >
            all of
          </Tab>
          <Tab
            on={node.join === 'or'}
            onPress={() => edit(node.key, (n) => ({ ...(n as Group), join: 'or' }))}
          >
            any of
          </Tab>
          <Tab
            on={node.negate}
            onPress={() => edit(node.key, (n) => ({ ...(n as Group), negate: !(n as Group).negate }))}
          >
            not
          </Tab>
        </Tabs>
        <Grow />
        <Button
          quiet
          onPress={() =>
            edit(node.key, (n) => ({ ...(n as Group), kids: [...(n as Group).kids, condOf('USD')] }))
          }
        >
          + condition
        </Button>
        <Button
          quiet
          onPress={() =>
            edit(node.key, (n) => ({ ...(n as Group), kids: [...(n as Group).kids, groupOf()] }))
          }
        >
          + group
        </Button>
        {depth > 0 ? (
          <Button quiet onPress={() => edit(node.key, () => null)}>
            remove
          </Button>
        ) : null}
      </Row>
      <Col gap={8}>
        {node.kids.map((k) =>
          k.at === 'group' ? (
            <GroupEditor key={k.key} node={k} depth={depth + 1} edit={edit} />
          ) : (
            <CondEditor key={k.key} node={k} edit={edit} />
          ),
        )}
        {!node.kids.length ? <Empty>Empty group.</Empty> : null}
      </Col>
    </Frame>
  )
}

function CondEditor({
  node,
  edit,
}: {
  node: Cond
  edit: (k: string, fn: (n: Node) => Node | null) => void
}) {
  const t = byID.get(node.term)!
  const set = (patch: Partial<Cond>) => edit(node.key, (n) => ({ ...(n as Cond), ...patch }))

  return (
    <Row wrap gap={6}>
      <Select
        label="Term"
        value={node.term}
        options={terms.map((x) => ({ value: x.id, label: x.label }))}
        onChange={(v) => {
          const next = condOf(v)
          set({ term: next.term, args: next.args, op: next.op, value: next.value })
        }}
      />

      {t.args.map((a, i) => (
        <ArgEditor
          key={a.name}
          arg={a}
          value={node.args[i] ?? a.value}
          onChange={(v) => set({ args: node.args.map((old, j) => (j === i ? v : old)) })}
        />
      ))}

      {t.returns !== 'bool' ? (
        <>
          <Select
            narrow
            label="Comparator"
            value={node.op}
            options={comparators[t.returns]}
            onChange={(v) => set({ op: v })}
          />
          <Input
            type="text"
            value={node.value}
            onChange={(e) => set({ value: e.target.value })}
            placeholder={t.returns === 'number' ? '10000.0' : 'value'}
          />
        </>
      ) : (
        <Hint>{t.hint ?? 'holds or does not'}</Hint>
      )}

      <Button quiet onPress={() => edit(node.key, () => null)}>
        ×
      </Button>
    </Row>
  )
}

function ArgEditor({
  arg: a,
  value,
  onChange,
}: {
  arg: Arg
  value: string
  onChange: (v: string) => void
}) {
  const options =
    a.kind === 'subject' ? subjects : a.kind === 'dimension' ? dimensions : a.kind === 'window' ? windows : null
  if (options) {
    return <Select label={a.name} value={value} options={options} onChange={onChange} />
  }
  return (
    <Input
      type="text"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      aria-label={a.name}
      placeholder={a.name}
    />
  )
}

// ── The evidence ────────────────────────────────────────────────────────────

function Report({ report }: { report: api.Replay }) {
  const c = report.candidate
  const i = report.incumbent
  return (
    <Card title="Replay">
      <Tiles>
        <Tile label="Events replayed" value={report.events.toLocaleString()} />
        <Tile label="Would alert" value={c.alerts.toLocaleString()} />
        <Tile label="Judged" value={`${c.judged.toLocaleString()} of ${c.observed.toLocaleString()}`} />
        <Tile label="Productive" value={c.productive.toLocaleString()} />
      </Tiles>

      <Row wrap>
        <Hint>false positives</Hint>
        <Meter
          value={c.false_positive_proportion ?? 0}
          state={
            c.false_positive_proportion === undefined
              ? 'plain'
              : c.false_positive_proportion > 0.8
                ? 'critical'
                : c.false_positive_proportion > 0.5
                  ? 'serious'
                  : 'good'
          }
        />
        <Mono>{pct(c.false_positive_proportion)}</Mono>
        <Hint>intelligence value</Hint>
        <Mono>{pct(c.intelligence_value)}</Mono>
      </Row>

      {i ? (
        <table className="grid">
          <thead>
            <tr>
              <th>Against the incumbent</th>
              <th className="num">Alerts</th>
              <th className="num">Productive</th>
              <th className="num">Unproductive</th>
              <th className="num">False positives</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>{i.rule}</td>
              <td className="num">{i.alerts}</td>
              <td className="num">{i.productive}</td>
              <td className="num">{i.unproductive}</td>
              <td className="num">{pct(i.false_positive_proportion)}</td>
            </tr>
            <tr>
              <td>candidate</td>
              <td className="num">{c.alerts}</td>
              <td className="num">{c.productive}</td>
              <td className="num">{c.unproductive}</td>
              <td className="num">{pct(c.false_positive_proportion)}</td>
            </tr>
          </tbody>
        </table>
      ) : null}

      {report.delta ? (
        <Row wrap>
          <Badge state="good">+{report.delta.counts.added} newly caught</Badge>
          <Badge state="critical">−{report.delta.counts.dropped} no longer caught</Badge>
          <Badge>{report.delta.counts.kept} unchanged</Badge>
        </Row>
      ) : null}

      {report.from ? (
        <Hint>
          replayed {new Date(report.from).toLocaleDateString()} to{' '}
          {report.to ? new Date(report.to).toLocaleDateString() : 'now'}
        </Hint>
      ) : null}
    </Card>
  )
}

