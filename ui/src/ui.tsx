// The pieces every screen is made of, on @hanzo/gui.
//
// Layout, surfaces, controls and type are the kit's: a stack is an XStack or a
// YStack, a control is a Button or an Input, and their styling is carried as
// props the runtime turns into atomic classes. Nothing here re-implements a
// button.
//
// This module is the screens' whole vocabulary. A page composes what is here
// and reaches for no HTML control and no stylesheet class of its own — which is
// checked, not merely intended: `ui/e2e/kit.spec.ts` reads these sources and
// fails on a raw <input>, <button>, <select> or <textarea> in a page. The kit
// arrives at the screens or it does not arrive at all.
//
// Four things are deliberately not the kit's, each because the kit's answer
// would be wrong here rather than because reaching for it was inconvenient:
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
//   - Responsive arrangement. Cols, Tiles and Split are `repeat(auto-fit,
//     minmax(...))` grids: they reflow on their OWN width with no breakpoint and
//     no measurement. The kit's stacks are flexbox and cannot express that, and
//     `flex-wrap` is not the same thing — it leaves a stranded last item at its
//     natural width instead of dividing the row. So the arrangement stays a
//     grid, in these three components and nowhere else.
//   - The chooser. The kit's Select is built on Sheet and Adapt, whose pan
//     responder needs a react-native runtime this bundle deliberately does not
//     carry. One native <select> lives in `Select` below; no page writes one.

import { useCallback, useEffect, useRef, useState, type JSX, type ReactNode } from 'react'
import { Link } from 'wouter'
// The kit, imported at the part rather than at the barrel. @hanzo/gui's index
// re-exports every component it has, including the ones built on react-native
// primitives this console never renders (Sheet's pan responder is the loudest),
// so reaching for the part keeps a browser bundle free of a native runtime it
// has no use for. Every one of these is @hanzo/gui's own package at @hanzo/gui's
// own version — the kit is still one dependency and one pin.
import { Button as KitButton } from '@hanzogui/button'
import { Aside, Header, Section } from '@hanzogui/elements'
import { Input as KitInput, TextArea as KitTextArea, type InputProps, type TextAreaProps } from '@hanzogui/input'
import { Label } from '@hanzogui/label'
import { Separator } from '@hanzogui/separator'
import { XStack, YStack } from '@hanzogui/stacks'
import { SizableText } from '@hanzogui/text'

export { Aside, Header, Label, Section, Separator, SizableText }

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

/** Stack is a screen's vertical rhythm: cards down a column, evenly spaced. */
export function Stack({ children }: { children: ReactNode }) {
  return (
    <YStack gap={16} minWidth={0}>
      {children}
    </YStack>
  )
}

/**
 * Cols, Tiles and Split are the three auto-fit grids. Each divides its own
 * width among its children with no breakpoint and no measurement — see the note
 * at the top of this file for why these are not kit stacks.
 */
export function Cols({ children }: { children: ReactNode }) {
  return <div className="cols">{children}</div>
}

export function Tiles({ children }: { children: ReactNode }) {
  return <div className="tiles">{children}</div>
}

export function Split({ children }: { children: ReactNode }) {
  return <div className="split">{children}</div>
}

/**
 * Scroll bounds a long table so the card keeps its place on the screen and the
 * rail stays reachable. It is a plain overflow container rather than the kit's
 * ScrollView: that one is react-native's, and it positions its content with
 * inline styles — which is exactly the shape `ui/e2e/console.spec.ts` asserts
 * the served policy refuses.
 */
export function Scroll({ children }: { children: ReactNode }) {
  return <div className="scroll">{children}</div>
}

/**
 * Frame is a nested region: a box with an accent edge, so a structure inside a
 * structure reads as one. The rule builder's groups are the only thing that
 * nests on these screens.
 */
export function Frame({ children }: { children: ReactNode }) {
  return (
    <YStack className="group" gap={10}>
      {children}
    </YStack>
  )
}

/** The space between what is on the left of a row and what is on the right. */
export function Grow() {
  return <YStack flex={1} />
}

/** Body is the padded region inside a flush Card — the same inset Card gives. */
export function Body({ children, centred }: { children: ReactNode; centred?: boolean }) {
  return (
    <YStack padding={centred ? 26 : 16} gap={centred ? 16 : 12} alignItems={centred ? 'center' : undefined}>
      {children}
    </YStack>
  )
}

export function Badge({ state = 'plain', children }: { state?: State; children: ReactNode }) {
  return (
    <XStack className={`badge ${state}`} display="inline-flex" alignItems="center" gap={6}>
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
            <Grow />
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

/**
 * The one button. The kit draws it; this names the three roles a screen has for
 * one, so no call site restates a scale.
 *
 * Two things about the kit's Button are worth knowing before changing this, and
 * neither shows up in a type error:
 *
 *   - `size` is a size TOKEN, and the kit derives the box AND the type from it.
 *     Pass 24 meaning "24px tall" and the label comes out 24px too. So the box
 *     and the type are given separately, from the console's own scale, once.
 *   - It does not forward `className`. A tone written as a stylesheet class is
 *     dropped in silence — the button renders, the policy is clean, and the one
 *     action a screen exists for looks like every other action on it. So tone is
 *     carried in the kit's own style props, which is where it belonged anyway.
 *
 * Both are invisible to a render test and to a policy check. `ui/e2e/kit.spec.ts`
 * therefore MEASURES a label and COMPARES a primary button against a plain one,
 * rather than trusting that either was asked for.
 */
export function Button({
  tone,
  quiet,
  busy,
  disabled,
  icon,
  onPress,
  children,
}: {
  /** The reserved tones: emphasis, and destruction. Absent is a plain action. */
  tone?: 'primary' | 'critical'
  /** Refresh, remove, close, sign out — present but not competing for the eye. */
  quiet?: boolean
  /** Working. Shows the spinner in place of the icon and refuses a second press. */
  busy?: boolean
  disabled?: boolean
  icon?: JSX.Element
  onPress: () => void
  children: ReactNode
}) {
  return (
    <KitButton
      size={quiet ? 24 : 30}
      fontSize={quiet ? 11 : 13}
      fontWeight={tone === 'primary' ? '500' : undefined}
      chromeless={quiet}
      // Tone is read through app.css's bridge, not through the kit's theme
      // tokens. `$color` and `$background` inside a Button resolve against the
      // kit's Button sub-theme, and in the default web config that sub-theme
      // carries placeholders — `--t0: dark`, `--t1: button` — which are names,
      // not colours, so a button styled from them comes out transparent. The
      // bridge is where this console reads the theme, and it reads it once.
      backgroundColor={tone === 'primary' ? 'var(--hz-primary)' : undefined}
      color={
        tone === 'primary'
          ? 'var(--hz-primary-foreground)'
          : tone === 'critical'
            ? 'var(--critical)'
            : undefined
      }
      borderColor={tone === 'primary' ? 'transparent' : tone === 'critical' ? 'var(--critical)' : undefined}
      disabled={disabled || busy}
      icon={busy ? <Spinner /> : icon}
      onPress={onPress}
    >
      {children}
    </KitButton>
  )
}

/**
 * The two text controls, at the console's scale. Same reason Button is wrapped:
 * the kit derives type from the size token, so a control asked for by height
 * comes out with type to match, and 24 call sites each restating a scale is 24
 * chances for them to disagree. A note and an expression are read as code, so
 * the TextArea is monospaced — that is a decision about the content, and it is
 * made once, here.
 */
export function Input(props: InputProps) {
  return <KitInput size={30} fontSize={13} {...props} />
}

export function TextArea(props: TextAreaProps) {
  return <KitTextArea fontSize={11} fontFamily="$mono" {...props} />
}

/** A chooser's options: a bare value, or a value shown under another name. */
export type Option = string | { value: string; label: string }

const optionValue = (o: Option) => (typeof o === 'string' ? o : o.value)
const optionLabel = (o: Option) => (typeof o === 'string' ? o : o.label)

/**
 * The one chooser. Native, for the reason given at the top of this file, and
 * therefore in exactly one place: a page passes values and gets a choice back,
 * and no page writes a <select> or an <option> of its own.
 */
export function Select({
  value,
  onChange,
  options,
  label,
  narrow,
}: {
  value: string
  onChange: (value: string) => void
  options: readonly Option[]
  label?: string
  narrow?: boolean
}) {
  return (
    <select
      className={narrow ? 'narrow' : undefined}
      value={value}
      aria-label={label}
      onChange={(e) => onChange(e.target.value)}
    >
      {options.map((o) => (
        <option key={optionValue(o)} value={optionValue(o)}>
          {optionLabel(o)}
        </option>
      ))}
    </select>
  )
}

/**
 * A segmented control. Tabs is the container and Tab is one segment, rather
 * than one component taking a list, because two of the three groups on these
 * screens are not a single choice: the rule builder's join is a choice and its
 * negation is an independent toggle, sitting in the same segment strip.
 */
export function Tabs({ children }: { children: ReactNode }) {
  return (
    <XStack className="tabs" alignItems="center">
      {children}
    </XStack>
  )
}

export function Tab({ on, onPress, children }: { on: boolean; onPress: () => void; children: ReactNode }) {
  return (
    <KitButton
      size={24}
      fontSize={11}
      chromeless
      // The pressed state is style props rather than a class for the reason
      // given on Button: the kit does not forward className, so a segment
      // strip styled that way would show no selection at all.
      backgroundColor={on ? 'var(--hz-accent)' : 'transparent'}
      color={on ? 'var(--ink)' : 'var(--ink-2)'}
      aria-pressed={on}
      onPress={onPress}
    >
      {children}
    </KitButton>
  )
}

/**
 * A link that looks like a button and stays a link. Navigation has to be an
 * anchor — a middle click opens it, a right click copies it, and a screen
 * reader announces where it goes — so `asChild` puts the kit's styling ON the
 * anchor instead of wrapping one interactive element around another.
 */
export function Go({ href, children }: { href: string; children: ReactNode }) {
  return (
    <KitButton asChild size={24} fontSize={11} chromeless>
      <Link href={href}>{children}</Link>
    </KitButton>
  )
}

/** Secondary prose: what a control means, or what a number was measured over. */
export function Hint({ children }: { children: ReactNode }) {
  return (
    <SizableText size={11} className="hint">
      {children}
    </SizableText>
  )
}

/** A second line under the first: what a row is, once you know which row. */
export function Sub({ children }: { children: ReactNode }) {
  return (
    <SizableText size={11} className="sub" display="block">
      {children}
    </SizableText>
  )
}

/** An identifier, an amount, or anything else read character by character. */
export function Mono({ children }: { children: ReactNode }) {
  return (
    <SizableText size={11} className="mono">
      {children}
    </SizableText>
  )
}

/** A block the engine wrote: an expression, or a model's own account of itself. */
export function Code({ children }: { children: ReactNode }) {
  return <pre className="code">{children}</pre>
}

/** Name/value pairs. A description list, because that is what this is. */
export function Kv({ children }: { children: ReactNode }) {
  return <dl className="kv">{children}</dl>
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
          <Grow />
          {actions}
          <Button quiet onPress={onClose}>
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
