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
import {
  Badge,
  Body,
  Button,
  Card,
  Col,
  Cols,
  Empty,
  Fail,
  Field,
  Hint,
  Input,
  Meter,
  Row,
  Scroll,
  Sub,
  SizableText,
  Spinner,
  useLoad,
  when,
} from '../ui'

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
        <Cols>
          <Field label="Name">
            <Input
              type="text"
              value={name}
              placeholder="As it appears on the instruction"
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && void search()}
            />
          </Field>
          <Field label="Date of birth" hint="Optional. Corroborates or contradicts a name match.">
            <Input type="text" value={dob} placeholder="1974-03-02" onChange={(e) => setDOB(e.target.value)} />
          </Field>
        </Cols>
        <Row wrap>
          <Button
            tone="primary"
            busy={busy}
            disabled={busy || !name.trim()}
            onPress={() => void search()}
          >
            Search
          </Button>
          {screening.data && !screening.data.ready ? (
            <Badge state="critical">
              screening is unfit: {screening.data.unfit.join(', ')}
            </Badge>
          ) : null}
        </Row>
        <Fail error={error} />
      </Card>

      {hits ? (
        <Card title={`${hits.length} match${hits.length === 1 ? '' : 'es'}`} flush>
          <Scroll>
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
                      <Col gap={2}>
                        <SizableText size={13}>{h.name}</SizableText>
                        <Sub>
                          {h.kind} · {h.ref_id} · {h.reason}
                        </Sub>
                      </Col>
                    </td>
                    <td>{h.list}</td>
                    <td className="num">
                      <Meter value={h.score} state={h.score > 0.9 ? 'critical' : 'serious'} width={48} />
                      <Sub>{h.score.toFixed(3)}</Sub>
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
                  <Hint>
                    Screened against {screening.data.total_entries.toLocaleString()} designations across{' '}
                    {screening.data.sources.length} lists.
                  </Hint>
                ) : null}
              </Empty>
            ) : null}
          </Scroll>
        </Card>
      ) : null}

      <Card
        title="Lists"
        actions={
          <Button quiet onPress={() => void screening.reload()}>
            Refresh
          </Button>
        }
        flush
      >
        {screening.error ? (
          <Body>
            <Fail error={screening.error} />
          </Body>
        ) : null}
        <Scroll>
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
                    {s.error ? <Sub>{s.error}</Sub> : null}
                  </td>
                  <td className="id">{s.sha256 ? s.sha256.slice(0, 12) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {screening.busy ? (
            <Empty>
              <Spinner />
            </Empty>
          ) : null}
        </Scroll>
      </Card>
    </>
  )
}
