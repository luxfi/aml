// The pieces every screen is made of.
//
// State colour appears in exactly one place — the dot inside a Badge and the
// accent on a row — and always beside the word it means. Magnitude is drawn in
// SVG rather than styled, so nothing in this app needs an inline style and the
// served CSP can refuse them outright.

import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'

/** The four reserved state steps. Severity and readiness both map onto them. */
export type State = 'good' | 'warning' | 'serious' | 'critical' | 'plain'

/** severity() maps the engine's vocabulary onto the reserved steps. */
export function severity(s: string | undefined): State {
  switch ((s ?? '').toLowerCase()) {
    case 'critical':
      return 'critical'
    case 'high':
      return 'serious'
    case 'medium':
      return 'warning'
    case 'low':
      return 'good'
    default:
      return 'plain'
  }
}

/** action() maps a decision onto the same steps: blocking is the loudest. */
export function action(a: string | undefined): State {
  switch ((a ?? '').toLowerCase()) {
    case 'block':
      return 'critical'
    case 'report':
      return 'serious'
    case 'review':
      return 'warning'
    case 'allow':
      return 'good'
    default:
      return 'plain'
  }
}

export function Badge({ state = 'plain', children }: { state?: State; children: ReactNode }) {
  return (
    <span className={`badge ${state}`}>
      <i className="dot" aria-hidden="true" />
      {children}
    </span>
  )
}

export function Tile({
  label,
  value,
  note,
  state,
}: {
  label: string
  value: ReactNode
  note?: ReactNode
  state?: State
}) {
  return (
    <div className="tile">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {(note || state) && (
        <div className="cap">{state ? <Badge state={state}>{note}</Badge> : note}</div>
      )}
    </div>
  )
}

/**
 * Meter draws a 0..1 magnitude. Geometry is an SVG attribute, so a value can be
 * shown without a style attribute, and the data end is a rounded 4px cap on a
 * thin track rather than a chunky bar.
 */
export function Meter({ value, state = 'plain', width = 76 }: { value: number; state?: State; width?: number }) {
  const v = Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0))
  const w = Math.max(2, Math.round(v * width))
  return (
    <svg className="meter" width={width} height={6} viewBox={`0 0 ${width} 6`} role="img" aria-label={`${Math.round(v * 100)} percent`}>
      <rect className="track" x="0" y="2" width={width} height="2" rx="1" />
      <rect className={`fill ${state}`} x="0" y="1" width={w} height="4" rx="2" />
    </svg>
  )
}

export function Card({
  title,
  actions,
  children,
  flush,
}: {
  title?: string
  actions?: ReactNode
  children: ReactNode
  flush?: boolean
}) {
  return (
    <section className="card">
      {(title || actions) && (
        <header>
          {title && <h2>{title}</h2>}
          <div className="grow" />
          {actions}
        </header>
      )}
      {flush ? children : <div className="body">{children}</div>}
    </section>
  )
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {hint && <div className="hint">{hint}</div>}
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>
}

export function Fail({ error }: { error: unknown }) {
  if (!error) return null
  return (
    <div className="note bad" role="alert">
      <i className="dot" aria-hidden="true" />
      <span>{error instanceof Error ? error.message : String(error)}</span>
    </div>
  )
}

export function Spinner() {
  return <i className="spin" aria-label="loading" />
}

export function Panel({
  title,
  onClose,
  actions,
  children,
}: {
  title: string
  onClose: () => void
  actions?: ReactNode
  children: ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.focus()
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', esc)
    return () => window.removeEventListener('keydown', esc)
  }, [onClose])
  return (
    <>
      <button className="scrim" aria-label="Close" onClick={onClose} />
      <aside className="panel" role="dialog" aria-label={title} tabIndex={-1} ref={ref}>
        <header>
          <h2>{title}</h2>
          <div className="grow" />
          {actions}
          <button className="btn ghost small" onClick={onClose}>
            Close
          </button>
        </header>
        <div className="body">{children}</div>
      </aside>
    </>
  )
}

/** One load, with its pending and failed states, and a way to run it again. */
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(true)
  const call = useRef(fn)
  call.current = fn

  const run = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      setData(await call.current())
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    void run()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  return { data, error, busy, reload: run }
}

// ── Formatting ──────────────────────────────────────────────────────────────

export const when = (t?: string | null) =>
  !t ? '—' : new Date(t).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export const day = (t?: string | null) =>
  !t ? '—' : new Date(t).toLocaleDateString(undefined, { dateStyle: 'medium' })

export const money = (n: number, ccy = 'USD') => {
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency: ccy, maximumFractionDigits: 2 }).format(n)
  } catch {
    return `${n.toLocaleString()} ${ccy}`
  }
}

export const pct = (n?: number) => (n === undefined || n === null ? '—' : `${(n * 100).toFixed(1)}%`)

/** Icons: one 16px stroke set, no icon dependency. */
export function Icon({ name }: { name: keyof typeof paths }) {
  return (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {paths[name]}
    </svg>
  )
}

const paths = {
  overview: <><rect x="3" y="3" width="7" height="9" rx="1" /><rect x="14" y="3" width="7" height="5" rx="1" /><rect x="14" y="12" width="7" height="9" rx="1" /><rect x="3" y="16" width="7" height="5" rx="1" /></>,
  cases: <><path d="M4 7h16v13H4z" /><path d="M9 7V4h6v3" /><path d="M4 12h16" /></>,
  rules: <><path d="M4 6h6" /><path d="M14 6h6" /><path d="M4 18h6" /><path d="M14 18h6" /><circle cx="12" cy="6" r="2" /><circle cx="12" cy="18" r="2" /></>,
  flow: <><path d="M3 12h5l3 7 4-14 3 7h3" /></>,
  screen: <><circle cx="11" cy="11" r="6" /><path d="m20 20-3.5-3.5" /></>,
  graph: <><circle cx="6" cy="7" r="2.4" /><circle cx="18" cy="6" r="2.4" /><circle cx="12" cy="18" r="2.4" /><path d="M8 8.5 10.6 16M16.2 8 13.4 16M8.4 6.6h7.2" /></>,
  shield: <><path d="M12 3 5 6v5.5c0 4.2 2.9 7.6 7 9.5 4.1-1.9 7-5.3 7-9.5V6z" /></>,
  out: <><path d="M9 4H5v16h4" /><path d="M15 8l4 4-4 4" /><path d="M19 12H9" /></>,
} as const
