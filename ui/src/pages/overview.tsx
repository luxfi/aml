// What this instance is doing, and what it is not.
//
// The gaps panel is deliberate. A coverage screen that shows only what is
// covered invites the reader to assume the rest is — so the engine publishes
// both and this shows both, in the same place, at the same size.

import { Fragment } from 'react'
import { Link } from 'wouter'

import * as api from '../api'
import { Badge, Card, Empty, Fail, Meter, Spinner, Tile, useLoad, when } from '../ui'

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
      <div className="tiles">
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
      </div>

      <div className="split">
        <Card
          title="Screening readiness"
          actions={
            <button className="btn ghost small" onClick={() => void screening.reload()}>
              Refresh
            </button>
          }
          flush
        >
          <div className="scroll">
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
              <div className="body">
                <Fail error={screening.error} />
              </div>
            ) : null}
            {!screening.busy && !lists.length ? <Empty>No list has been loaded.</Empty> : null}
          </div>
        </Card>

        <Card
          title="Open cases"
          actions={
            <Link className="btn ghost small" href="/cases">
              All cases
            </Link>
          }
          flush
        >
          <div className="scroll">
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
              <div className="body">
                <Fail error={cases.error} />
              </div>
            ) : null}
            {!cases.busy && !open.length ? <Empty>Nothing open.</Empty> : null}
          </div>
        </Card>
      </div>

      <Card title="Coverage" flush>
        <div className="body">
          <div className="row">
            {(catalog.data?.typologies ?? []).map((t) => (
              <Badge key={t}>{t.replace(/_/g, ' ')}</Badge>
            ))}
          </div>
          <span className="hint">
            {catalog.data?.obligations.length ?? 0} obligations claimed ·{' '}
            {catalog.data?.gaps.length ?? 0} gaps published
          </span>
          <Fail error={catalog.error} />
        </div>
        <div className="scroll">
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
        </div>
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
        <div className="row">
          <span className="hint">alert rate against appetite</span>
          <Meter value={appetite ? rate / appetite : 0} state={rate > appetite ? 'serious' : 'good'} />
          <span className="mono">
            {(rate * 100).toFixed(2)}% of {(appetite * 100).toFixed(2)}%
          </span>
        </div>
      ) : null}
      <dl className="kv">
        {Object.entries(state)
          .filter(([k]) => k !== 'enabled' && k !== 'reason')
          .map(([k, v]) => (
            <Fragment key={k}>
              <dt>{k.replace(/_/g, ' ')}</dt>
              <dd className="mono">{show(v)}</dd>
            </Fragment>
          ))}
      </dl>
    </Card>
  )
}

const number = (v: unknown): number | null =>
  typeof v === 'number' && Number.isFinite(v) ? v : null

const show = (v: unknown): string =>
  v === null || v === undefined ? '—' : typeof v === 'object' ? JSON.stringify(v) : String(v)
