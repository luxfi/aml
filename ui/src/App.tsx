import { Router, Route, Switch, Link, useLocation } from 'wouter'
import { useHashLocation } from 'wouter/use-hash-location'
import { Dashboard } from './pages/Dashboard'
import { Cases } from './pages/Cases'
import { Rules } from './pages/Rules'
import { Alerts } from './pages/Alerts'
import { Sanctions } from './pages/Sanctions'

const navItems = [
  { href: '/', label: 'Dashboard' },
  { href: '/cases', label: 'Cases' },
  { href: '/rules', label: 'Rules' },
  { href: '/alerts', label: 'Alerts' },
  { href: '/sanctions', label: 'Sanctions' },
] as const

function NavLink({ href, label }: { href: string; label: string }) {
  const [location] = useLocation()
  const active = href === '/' ? location === '/' : location.startsWith(href)
  return (
    <Link
      href={href}
      style={{
        display: 'block',
        padding: '6px 12px',
        borderRadius: 6,
        color: active ? '#fff' : '#a3a3a3',
        background: active ? '#262626' : 'transparent',
        textDecoration: 'none',
        fontSize: 14,
      }}
    >
      {label}
    </Link>
  )
}

export function App() {
  return (
    <Router hook={useHashLocation}>
      <div style={{ display: 'flex', height: '100vh', background: '#000', color: '#e5e5e5' }}>
        <aside
          style={{
            width: 200,
            borderRight: '1px solid #262626',
            padding: 16,
            display: 'flex',
            flexDirection: 'column',
            gap: 2,
          }}
        >
          <div style={{ fontWeight: 600, fontSize: 16, marginBottom: 16, color: '#fff' }}>
            AML Admin
          </div>
          <nav style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            {navItems.map((n) => (
              <NavLink key={n.href} href={n.href} label={n.label} />
            ))}
          </nav>
        </aside>
        <main style={{ flex: 1, overflow: 'auto', padding: 24 }}>
          <Switch>
            <Route path="/" component={Dashboard} />
            <Route path="/cases" component={Cases} />
            <Route path="/rules" component={Rules} />
            <Route path="/alerts" component={Alerts} />
            <Route path="/sanctions" component={Sanctions} />
            <Route>
              <div style={{ color: '#737373' }}>Not found</div>
            </Route>
          </Switch>
        </main>
      </div>
    </Router>
  )
}
