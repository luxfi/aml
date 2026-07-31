// The shell: who the brand is, who the caller is, and which screen is showing.
//
// Boot order is the whole security posture in four steps. The bundle learns
// where the API is (config.json), asks the API which brand it is and which
// issuer to trust (/v1/aml/config, the one route that needs no token), and only
// then offers a sign-in — so the console can never send a user to an issuer the
// engine would refuse a token from.

import { useEffect, useState } from 'react'
import { Link, Route, Router, Switch, useLocation } from 'wouter'

import * as api from './api'
import * as auth from './auth'
import { load as loadConfig } from './config'
import { Card, Fail, Icon, Spinner } from './ui'

import { Overview } from './pages/overview'
import { Cases } from './pages/cases'
import { Rules } from './pages/rules'
import { Flow } from './pages/flow'
import { Sanctions } from './pages/sanctions'
import { Relationships } from './pages/relationships'

type Boot =
  | { at: 'loading' }
  | { at: 'failed'; error: unknown }
  | { at: 'ready'; brand: api.Brand; session: auth.Session | null }

export function App() {
  const [boot, setBoot] = useState<Boot>({ at: 'loading' })

  useEffect(() => {
    void (async () => {
      try {
        await loadConfig()
        const brand = await api.brand()
        api.bind(brand.issuer)

        if (window.location.pathname === '/callback') {
          const back = await auth.callback(brand.issuer, window.location.search)
          // Replace, so the code never stays in history or in a shared URL.
          window.history.replaceState(null, '', back)
        }
        setBoot({ at: 'ready', brand, session: auth.session() })
      } catch (error) {
        setBoot({ at: 'failed', error })
      }
    })()
  }, [])

  if (boot.at === 'loading') {
    return (
      <div className="gate">
        <Spinner />
      </div>
    )
  }

  if (boot.at === 'failed') {
    return (
      <div className="gate">
        <Card title="Cannot start">
          <Fail error={boot.error} />
          <button className="btn" onClick={() => window.location.assign('/')}>
            Try again
          </button>
        </Card>
      </div>
    )
  }

  if (!boot.session) return <Gate brand={boot.brand} />

  return (
    <Router>
      <Shell brand={boot.brand} session={boot.session} />
    </Router>
  )
}

function Gate({ brand }: { brand: api.Brand }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  return (
    <div className="gate">
      <Card>
        <div className="body">
          <span className="glyph">
            <Icon name="shield" />
          </span>
          <h1>{brand.display} AML</h1>
          <p>
            Transaction monitoring, case management and screening. Sign in with your{' '}
            {brand.display} account to continue.
          </p>
          <Fail error={error} />
          <button
            className="btn primary"
            disabled={busy}
            onClick={() => {
              setBusy(true)
              auth.login(brand.issuer).catch((e) => {
                setError(e)
                setBusy(false)
              })
            }}
          >
            {busy ? <Spinner /> : null}
            Sign in
          </button>
        </div>
      </Card>
    </div>
  )
}

const screens = [
  { path: '/', label: 'Overview', icon: 'overview' as const, page: Overview },
  { path: '/cases', label: 'Cases', icon: 'cases' as const, page: Cases },
  { path: '/rules', label: 'Rules', icon: 'rules' as const, page: Rules },
  { path: '/flow', label: 'Transactions', icon: 'flow' as const, page: Flow },
  { path: '/sanctions', label: 'Sanctions', icon: 'screen' as const, page: Sanctions },
  { path: '/relationships', label: 'Relationships', icon: 'graph' as const, page: Relationships },
]

function Shell({ brand, session }: { brand: api.Brand; session: auth.Session }) {
  const [path] = useLocation()
  const who = auth.who(session)
  const active = screens.find((s) => (s.path === '/' ? path === '/' : path.startsWith(s.path)))

  return (
    <div className="shell">
      <nav className="rail">
        <div className="mark">
          <span className="glyph">
            <Icon name="shield" />
          </span>
          <div>
            <b>{brand.display}</b> <span>AML</span>
          </div>
        </div>
        <div className="nav">
          {screens.map((s) => (
            <Link key={s.path} href={s.path} aria-current={s === active ? 'page' : undefined}>
              <Icon name={s.icon} />
              {s.label}
            </Link>
          ))}
        </div>
        <div className="rail-foot">
          <div className="who org">{who.org || '—'}</div>
          <div className="who">{who.name}</div>
          <button className="btn ghost small" onClick={() => auth.logout(brand.issuer)}>
            <Icon name="out" />
            Sign out
          </button>
        </div>
      </nav>

      <div className="main">
        <header className="bar">
          <h1>{active?.label ?? 'Not found'}</h1>
          <div className="grow" />
          <Health />
        </header>
        <main className="view">
          <Switch>
            {screens.map((s) => (
              <Route key={s.path} path={s.path} component={s.page} />
            ))}
            <Route path="/cases/:id" component={Cases} />
            <Route>
              <Card title="Not found">
                <p>No screen answers that address.</p>
              </Card>
            </Route>
          </Switch>
        </main>
      </div>
    </div>
  )
}

/** The instance's own fitness, polled. Degraded is worth seeing everywhere. */
function Health() {
  const [state, setState] = useState<api.Health | null>(null)
  useEffect(() => {
    let live = true
    const beat = () =>
      api
        .health()
        .then((h) => live && setState(h))
        .catch(() => live && setState(null))
    void beat()
    const t = setInterval(beat, 30_000)
    return () => {
      live = false
      clearInterval(t)
    }
  }, [])
  const ok = state?.status === 'ok'
  return (
    <span className={`badge ${state ? (ok ? 'good' : 'critical') : 'plain'}`} title={state?.records ?? ''}>
      <i className="dot" aria-hidden="true" />
      {state ? (ok ? 'Healthy' : `Degraded: ${state.records}`) : 'Unreachable'}
    </span>
  )
}
