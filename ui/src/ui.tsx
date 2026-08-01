// The pieces every screen is made of, on @hanzo/gui.
//
// Layout, surfaces, controls and type are the kit's: a stack is an XStack or a
// YStack, a control is a Button or an Input, and their styling is carried as
// props the runtime turns into atomic classes. Nothing here re-implements a
// button.
//
// Two things are deliberately not the kit's:
//
//   - State colour. Four fixed steps for good / warning / serious / critical,
//     each clearing 3:1 on this surface, none of which changes with the theme —
//     a supervisor's copy of a screenshot has to mean what the screen meant. It
//     NEVER carries meaning alone: every badge, accent and dot ships with its
//     label, so a reader who cannot separate the hues reads the same thing.
//   - Magnitude. A meter is drawn in SVG, where geometry is an ATTRIBUTE. A
//     width that varies with data is the one thing that would otherwise reach
//     the DOM as an inline style, and an inline style is what the served policy
//     refuses.

import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
// The kit, imported at the part rather than at the barrel. @hanzo/gui's index
// re-exports every component it has, including the ones built on react-native
// primitives this console never renders (Sheet's pan responder is the loudest),
// so reaching for the part keeps a browser bundle free of a native runtime it
// has no use for. Every one of these is @hanzo/gui's own package at @hanzo/gui's
// own version — the kit is still one dependency and one pin.
import { Button } from '@hanzogui/button'
import { Aside, Header, Section } from '@hanzogui/elements'
import { Input, TextArea } from '@hanzogui/input'
import { Label } from '@hanzogui/label'
import { Separator } from '@hanzogui/separator'
import { XStack, YStack } from '@hanzogui/stacks'
import { SizableText } from '@hanzogui/text'

export { Aside, Button, Header, Input, Label, Section, Separator, SizableText, TextArea, XStack, YStack }

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

/** Row and Col are the two arrangements every screen is built out of. */
export function Row({ children, gap = 8, wrap }: { children: ReactNode; gap?: number; wrap?: boolean }) {
  return (
    <XStack gap={gap} alignItems="center" flexWrap={wrap ? 'wrap' : 'nowrap'}>
      {children}
    </XStack>
  )
}

export function Col({ children, gap = 8 }: { children: ReactNode; gap?: number }) {
  return <YStack gap={gap}>{children}</YStack>
}

export function Badge({ state = 'plain', children }: { state?: State; children: ReactNode }) {
  return (
    <XStack className={`badge ${state}`} alignItems="center" gap={6}>
      <i className="dot" aria-hidden="true" />
      <SizableText size={11}>{children}</SizableText>
    </XStack>
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
    <YStack className="tile" gap={4} padding={14} borderWidth={1} borderColor="$borderColor" borderRadius={10}>
      <SizableText size={11} className="tile-label">
        {label}
      </SizableText>
      <SizableText size={22} className="tile-value">
        {value}
      </SizableText>
      {(note || state) && (
        <XStack alignItems="center">
          {state ? <Badge state={state}>{note}</Badge> : <SizableText size={11}>{note}</SizableText>}
        </XStack>
      )}
    </YStack>
  )
}

/**
 * Meter draws a 0..1 magnitude. Geometry is an SVG attribute, so a value can be
 * shown without a style attribute, and the data end is a rounded cap on a thin
 * track rather than a chunky bar.
 */
export function Meter({ value, state = 'plain', width = 76 }: { value: number; state?: State; width?: number }) {
  const v = Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0))
  const w = Math.max(2, Math.round(v * width))
  return (
    <svg
      className="meter"
      width={width}
      height={6}
      viewBox={`0 0 ${width} 6`}
      role="img"
      aria-label={`${Math.round(v * 100)} percent`}
    >
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
    <Section className="card" borderWidth={1} borderColor="$borderColor" borderRadius={12} overflow="hidden">
      {(title || actions) && (
        <>
          <Header className="card-head" flexDirection="row" alignItems="center" gap={10} paddingHorizontal={16} paddingVertical={12}>
            {title && <h2 className="card-title">{title}</h2>}
            <YStack flex={1} />
            {actions}
          </Header>
          <Separator />
        </>
      )}
      {flush ? (
        children
      ) : (
        <YStack className="card-body" padding={16} gap={12}>
          {children}
        </YStack>
      )}
    </Section>
  )
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <YStack className="field" gap={5}>
      <Label size={11} className="field-label">
        {label}
      </Label>
      {children}
      {hint && (
        <SizableText size={11} className="hint">
          {hint}
        </SizableText>
      )}
    </YStack>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <YStack className="empty" padding={28} alignItems="center" justifyContent="center">
      <SizableText size={13}>{children}</SizableText>
    </YStack>
  )
}

export function Fail({ error }: { error: unknown }) {
  if (!error) return null
  return (
    <XStack className="note bad" role="alert" alignItems="flex-start" gap={8} padding={10} borderRadius={8}>
      <i className="dot" aria-hidden="true" />
      <SizableText size={13}>{error instanceof Error ? error.message : String(error)}</SizableText>
    </XStack>
  )
}

/**
 * The one component still drawn here rather than taken from the kit. The kit's
 * Spinner is react-native's ActivityIndicator, which needs a native runtime this
 * bundle deliberately does not carry; a CSS rotation is the same thing on the
 * web and costs nothing.
 */
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
  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', esc)
    return () => window.removeEventListener('keydown', esc)
  }, [onClose])
  return (
    <>
      <button className="scrim" aria-label="Close" onClick={onClose} />
      <Aside className="panel" role="dialog" aria-label={title} tabIndex={-1}>
        <Header className="panel-head" flexDirection="row" alignItems="center" gap={10} paddingHorizontal={16} paddingVertical={12}>
          <h2 className="panel-title">{title}</h2>
          <YStack flex={1} />
          {actions}
          <Button size={28} chromeless onPress={onClose}>
            Close
          </Button>
        </Header>
        <Separator />
        <YStack className="panel-body" padding={16} gap={14} flex={1}>
          {children}
        </YStack>
      </Aside>
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
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: ccy,
      maximumFractionDigits: 2,
    }).format(n)
  } catch {
    return `${n.toLocaleString()} ${ccy}`
  }
}

export const pct = (n?: number) => (n === undefined || n === null ? '—' : `${(n * 100).toFixed(1)}%`)

/** Icons: one 16px stroke set, no icon dependency. */
export function Icon({ name }: { name: keyof typeof paths }) {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name]}
    </svg>
  )
}

const paths = {
  overview: (
    <>
      <rect x="3" y="3" width="7" height="9" rx="1" />
      <rect x="14" y="3" width="7" height="5" rx="1" />
      <rect x="14" y="12" width="7" height="9" rx="1" />
      <rect x="3" y="16" width="7" height="5" rx="1" />
    </>
  ),
  cases: (
    <>
      <path d="M4 7h16v13H4z" />
      <path d="M9 7V4h6v3" />
      <path d="M4 12h16" />
    </>
  ),
  rules: (
    <>
      <path d="M4 6h6" />
      <path d="M14 6h6" />
      <path d="M4 18h6" />
      <path d="M14 18h6" />
      <circle cx="12" cy="6" r="2" />
      <circle cx="12" cy="18" r="2" />
    </>
  ),
  flow: (
    <>
      <path d="M3 12h5l3 7 4-14 3 7h3" />
    </>
  ),
  screen: (
    <>
      <circle cx="11" cy="11" r="6" />
      <path d="m20 20-3.5-3.5" />
    </>
  ),
  graph: (
    <>
      <circle cx="6" cy="7" r="2.4" />
      <circle cx="18" cy="6" r="2.4" />
      <circle cx="12" cy="18" r="2.4" />
      <path d="M8 8.5 10.6 16M16.2 8 13.4 16M8.4 6.6h7.2" />
    </>
  ),
  shield: (
    <>
      <path d="M12 3 5 6v5.5c0 4.2 2.9 7.6 7 9.5 4.1-1.9 7-5.3 7-9.5V6z" />
    </>
  ),
  out: (
    <>
      <path d="M9 4H5v16h4" />
      <path d="M15 8l4 4-4 4" />
      <path d="M19 12H9" />
    </>
  ),
} as const
