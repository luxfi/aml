// The shell: who the brand is, who the caller is, and which screen is showing.
//
// Boot order is the whole security posture in four steps. The bundle learns
// where the API is (config.json), asks the API which brand it is and which
// issuer to trust (/v1/aml/config, the one route that needs no token), and only
// then offers a sign-in — so the console can never send a user to an issuer the
// engine would refuse a token from.

import { useEffect, useState } from 'react'
import { Link, Route, Router, Switch, useLocation } from 'wouter'

import { configureIam, getSession, getUser, handleCallback, logout, startLogin } from '@hanzo/iam/browser'
import type { IAMUser } from '@hanzo/iam/browser'

import * as api from './api'
import { Badge, Body, Button, Card, Fail, Grow, Icon, Spinner } from './ui'

import { Overview } from './pages/overview'
import { Cases } from './pages/cases'
import { Rules } from './pages/rules'
import { Flow } from './pages/flow'
import { Sanctions } from './pages/sanctions'
import { Relationships } from './pages/relationships'

type Boot =
  | { at: 'loading' }
  | { at: 'failed'; error: unknown }
  | { at: 'ready'; brand: api.Brand; signedIn: boolean }

export function App() {
  const [boot, setBoot] = useState<Boot>({ at: 'loading' })

  useEffect(() => {
    void (async () => {
      try {
        const brand = await api.brand()

        // One session model for every Hanzo app. The issuer and the clientId
        // both come from the API that enforces them, so this configures the
        // SDK with what the engine will actually accept — nothing is stated
        // twice and nothing can drift.
        configureIam({
          issuer: brand.issuer,
          clientId: brand.client_id,
          redirect: `${window.location.origin}/callback`,
        })

        if (window.location.pathname === '/callback') {
          const { redirect } = await handleCallback()
          // Replace, so the code never stays in history or in a shared URL.
          window.history.replaceState(null, '', redirect || '/')
        }
        setBoot({ at: 'ready', brand, signedIn: getSession().authenticated })
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
          <Button onPress={() => window.location.assign('/')}>Try again</Button>
        </Card>
      </div>
    )
  }

  if (!boot.signedIn) return <Gate brand={boot.brand} />

  return (
    <Router>
      <Shell brand={boot.brand} />
    </Router>
  )
}

function Gate({ brand }: { brand: api.Brand }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  return (
    <div className="gate">
      <Card>
        <Body centred>
          <span className="glyph">
            <Icon name="shield" />
          </span>
          <h1>{brand.display} AML</h1>
          <p>
            Transaction monitoring, case management and screening. Sign in with your{' '}
            {brand.display} account to continue.
          </p>
          <Fail error={error} />
          <Button
            tone="primary"
            busy={busy}
            disabled={busy}
            onPress={() => {
              setBusy(true)
              startLogin().catch((e: unknown) => {
                setError(e)
                setBusy(false)
              })
            }}
          >
            Sign in
          </Button>
        </Body>
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

function Shell({ brand }: { brand: api.Brand }) {
  const [path] = useLocation()
  const [who, setWho] = useState<IAMUser | null>(null)
  useEffect(() => {
    void getUser().then(setWho).catch(() => setWho(null))
  }, [])
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
          <div className="who org">{who?.owner || '—'}</div>
          <div className="who">{who?.name || who?.email || ''}</div>
          <SignOut />
        </div>
      </nav>

      <div className="main">
        <header className="bar">
          <h1>{active?.label ?? 'Not found'}</h1>
          <Grow />
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
    <Badge state={state ? (ok ? 'good' : 'critical') : 'plain'}>
      {state ? (ok ? 'Healthy' : `Degraded: ${state.records}`) : 'Unreachable'}
    </Badge>
  )
}


/**
 * Sign out of this console, and say only what actually happened.
 *
 * The tokens are revoked as far as a public client can revoke them and this tab
 * is cleared. The identity provider's own session is a separate thing and this
 * cannot end it, so the button does not pretend otherwise — it says where to go
 * and lets the reader decide.
 */
function SignOut() {
  const [busy, setBusy] = useState(false)
  return (
    <Button
      quiet
      busy={busy}
      icon={<Icon name="out" />}
      onPress={() => {
        setBusy(true)
        // @hanzo/iam owns what signing out means — RP-initiated logout and the
        // local clear, the same in every Hanzo app. When IAM's end-session
        // behaviour improves, every app gets it without touching this line.
        void logout().finally(() => window.location.assign('/'))
      }}
    >
      Sign out
    </Button>
  )
}
