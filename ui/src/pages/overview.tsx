// What this instance is doing, and what it is not.
//
// The gaps panel is deliberate. A coverage screen that shows only what is
// covered invites the reader to assume the rest is — so the engine publishes
// both and this shows both, in the same place, at the same size.

import { Fragment } from 'react'

import * as api from '../api'
import {
  Badge,
  Body,
  Button,
  Card,
  Empty,
  Fail,
  Go,
  Hint,
  Kv,
  Meter,
  Mono,
  Row,
  Scroll,
  Spinner,
  Split,
  Tile,
  Tiles,
  useLoad,
  when,
} from '../ui'

export function Overview() {
  const cases = useLoad(() => api.cases())
  const screening = useLoad(() => api.sources())
  const model = useLoad(() => api.model())
  const catalog = useLoad(() => api.catalog())

  const open = cases.data?.filter((c) => c.status !== 'closed') ?? []
  const critical = open.filter((c) => c.severity === 'critical')
  const lists = screening.data?.sources ?? []
  const fit = lists.filter((s) => s.fresh)
  const rules = catalog.data?.rules ?? []
  const enabled = rules.filter((r) => r.enabled)

  return (
    <>
      <Tiles>
        <Tile
          label="Open cases"
          value={cases.busy ? <Spinner /> : open.length}
          note={`${critical.length} critical`}
          state={critical.length ? 'critical' : 'good'}
        />
        <Tile
          label="Screening lists"
          value={screening.busy ? <Spinner /> : `${fit.length}/${lists.length}`}
          note={screening.data?.ready ? 'fit to screen' : 'unfit'}
          state={screening.data?.ready ? 'good' : 'critical'}
        />
        <Tile
          label="Designations"
          value={
            screening.busy ? <Spinner /> : (screening.data?.total_entries ?? 0).toLocaleString()
          }
          note="loaded across all lists"
        />
        <Tile
          label="Rules installed"
          value={catalog.busy ? <Spinner /> : rules.length}
          note={`${enabled.length} enabled`}
        />
        <Tile
          label="Behavioural model"
          value={model.busy ? <Spinner /> : model.data?.enabled ? 'On' : 'Off'}
          note={model.data?.enabled ? `${model.data.faults ?? 0} faults` : 'rules alone'}
          state={model.data?.enabled ? 'good' : 'warning'}
        />
      </Tiles>

      <Split>
        <Card
          title="Screening readiness"
          actions={
            <Button quiet onPress={() => void screening.reload()}>
              Refresh
            </Button>
          }
          flush
        >
          <Scroll>
            <table className="grid">
              <thead>
                <tr>
                  <th>List</th>
                  <th className="num">Designations</th>
                  <th>Loaded</th>
                  <th>State</th>
                </tr>
              </thead>
              <tbody>
                {lists.map((s) => (
                  <tr key={s.source} className={s.fresh ? 'good' : 'critical'}>
                    <td>{s.source}</td>
                    <td className="num">{s.entries.toLocaleString()}</td>
                    <td>{when(s.loaded_at)}</td>
                    <td>
                      <Badge state={s.fresh ? 'good' : 'critical'}>
                        {s.fresh ? 'fresh' : s.error ? 'failing' : 'stale'}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {screening.error ? (
              <Body>
                <Fail error={screening.error} />
              </Body>
            ) : null}
            {!screening.busy && !lists.length ? <Empty>No list has been loaded.</Empty> : null}
          </Scroll>
        </Card>

        <Card
          title="Open cases"
          actions={
            <Go href="/cases">All cases</Go>
          }
          flush
        >
          <Scroll>
            <table className="grid">
              <thead>
                <tr>
                  <th>Case</th>
                  <th>Severity</th>
                  <th>Status</th>
                  <th>Opened</th>
                </tr>
              </thead>
              <tbody>
                {open.slice(0, 8).map((c) => (
                  <tr key={c.id}>
                    <td className="id">#{c.number}</td>
                    <td>
                      <Badge state={c.severity === 'critical' ? 'critical' : 'warning'}>
                        {c.severity}
                      </Badge>
                    </td>
                    <td>{c.status.replace(/_/g, ' ')}</td>
                    <td>{when(c.opened_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {cases.error ? (
              <Body>
                <Fail error={cases.error} />
              </Body>
            ) : null}
            {!cases.busy && !open.length ? <Empty>Nothing open.</Empty> : null}
          </Scroll>
        </Card>
      </Split>

      <Card title="Coverage" flush>
        <Body>
          <Row wrap>
            {(catalog.data?.typologies ?? []).map((t) => (
              <Badge key={t}>{t.replace(/_/g, ' ')}</Badge>
            ))}
          </Row>
          <Hint>
            {catalog.data?.obligations.length ?? 0} obligations claimed ·{' '}
            {catalog.data?.gaps.length ?? 0} gaps published
          </Hint>
          <Fail error={catalog.error} />
        </Body>
        <Scroll>
          <table className="grid">
            <thead>
              <tr>
                <th>Not covered</th>
                <th>Why</th>
                <th>What it needs</th>
              </tr>
            </thead>
            <tbody>
              {(catalog.data?.gaps ?? []).map((g, i) => (
                <tr key={i} className="warning">
                  <td className="id">
                    {g.citation.document} {g.citation.locator}
                  </td>
                  <td>{g.why}</td>
                  <td>{g.needs}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Scroll>
      </Card>

      {model.data?.enabled ? <ModelState state={model.data} /> : null}
    </>
  )
}

function ModelState({ state }: { state: api.Model }) {
  const rate = number(state.rate)
  const appetite = number(state.appetite)
  return (
    <Card title="Behavioural model">
      {rate !== null && appetite !== null ? (
        <Row wrap>
          <Hint>alert rate against appetite</Hint>
          <Meter value={appetite ? rate / appetite : 0} state={rate > appetite ? 'serious' : 'good'} />
          <Mono>
            {(rate * 100).toFixed(2)}% of {(appetite * 100).toFixed(2)}%
          </Mono>
        </Row>
      ) : null}
      <Kv>
        {Object.entries(state)
          .filter(([k]) => k !== 'enabled' && k !== 'reason')
          .map(([k, v]) => (
            <Fragment key={k}>
              <dt>{k.replace(/_/g, ' ')}</dt>
              <dd className="mono">{show(v)}</dd>
            </Fragment>
          ))}
      </Kv>
    </Card>
  )
}

const number = (v: unknown): number | null =>
  typeof v === 'number' && Number.isFinite(v) ? v : null

const show = (v: unknown): string =>
  v === null || v === undefined ? '—' : typeof v === 'object' ? JSON.stringify(v) : String(v)
