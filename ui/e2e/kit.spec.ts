// Whether the kit actually reaches the screens.
//
// "The console is on @hanzo/gui" is easy to say and easy to half-do: swap the
// shell's primitives, leave the six pages on raw HTML, and every other check in
// this suite still passes. It renders, the policy is clean, the types compile.
// So this file asks the two questions those checks cannot.
//
// 1. SOURCE — does a page reach past the kit? A page composes what `src/ui.tsx`
//    exports and writes no control and no stylesheet-class arrangement of its
//    own. `ui.tsx` is where the exceptions live, each with the reason it is one.
//
// 2. RENDER — did the kit's props mean what they said? Three defects in this
//    console got through a build, a typecheck, a CSP run and a render test, and
//    were found only by looking at it:
//
//      - `size` on a kit Button is a size TOKEN. Asking for a 24px-tall button
//        gave a 24px LABEL in it.
//      - The kit's Button does not forward `className`. Tone written as a class
//        was dropped in silence, so the one action a screen exists for looked
//        like every other action, and a segment strip showed no selection.
//      - A custom property is substituted where it is DECLARED. The theme bridge
//        sat at `:root`, which the kit's `:root .t_dark` can never reach, so a
//        control took a white ground and white text and disappeared into itself.
//
//    Each is silent, each is a lie about what the screen says, and each has an
//    assertion below. They are written as invariants rather than as three
//    regression cases, because the next one of these will not be the same bug.

import { expect, test } from '@playwright/test'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

import { signIn } from './session'

const SRC = new URL('../src', import.meta.url).pathname

const pages = () => {
  const dir = join(SRC, 'pages')
  return [
    ...readdirSync(dir).map((f) => ['pages/' + f, readFileSync(join(dir, f), 'utf8')] as const),
    ['app.tsx', readFileSync(join(SRC, 'app.tsx'), 'utf8')] as const,
  ]
}

test.describe('the kit reaches the screens', () => {
  test('no screen writes a control of its own', () => {
    // Every one of these has a kit-backed equivalent exported from ui.tsx. A
    // page reaching for the raw element is a page that has left the kit, and
    // it takes the console's focus ring, sizing and disabled semantics with it.
    for (const [name, src] of pages()) {
      const raw = src.match(/<(input|button|select|textarea|option)\b/g) ?? []
      expect(raw, `${name} writes ${raw.join(', ')} instead of the kit's`).toEqual([])
    }
  })

  test('no screen arranges itself out of the stylesheet', () => {
    // Layout is a value at the use site — Row, Col, Stack, Cols, Tiles, Split,
    // Scroll, Body — not a class name that has to be looked up in app.css. The
    // three grids the kit cannot express are named components in ui.tsx and are
    // the only place these class names appear.
    const arrangements = /className="(row|cols|stack|split|tiles|scroll|body|grow|tabs|group|head|kids|cond|tile)"/g
    for (const [name, src] of pages()) {
      const found = src.match(arrangements) ?? []
      expect(found, `${name} arranges itself with ${found.join(', ')}`).toEqual([])
    }
  })

  test('the pages take their vocabulary from one place', () => {
    // One import, so there is one answer to "what is a button here".
    for (const [name, src] of pages()) {
      if (name === 'app.tsx') continue
      const imports = src.match(/^import .* from '([^']+)'$/gm) ?? []
      const local = imports.filter((i) => /from '\.\./.test(i))
      expect(local.length, `${name} imports locally from more than api and ui`).toBeLessThanOrEqual(2)
      expect(src, `${name} should compose ../ui`).toContain("from '../ui'")
      expect(src, `${name} must not import the kit directly`).not.toContain('@hanzogui/')
    }
  })

  test('a label is the size it was asked for, not the size of its box', async ({ page }) => {
    await signIn(page)
    await page.goto('/cases')
    await expect(page.locator('nav.rail')).toBeVisible()

    // The console has two type steps for a control: 13px and 11px. A kit Button
    // derives type from its size token, so a button asked for by height comes
    // out with type to match unless the two are given separately. Measured on
    // the text node, because the button's own font-size is not the label's.
    const labels = await page.locator('button').evaluateAll((els) =>
      els
        .map((el) => {
          const span = el.querySelector('span')
          return span ? { text: (span.textContent ?? '').trim().slice(0, 20), px: parseFloat(getComputedStyle(span).fontSize) } : null
        })
        .filter((x): x is { text: string; px: number } => x !== null),
    )
    expect(labels.length).toBeGreaterThan(3)
    for (const l of labels) {
      expect(l.px, `"${l.text}" is ${l.px}px — the console's control type is 11 or 13`).toBeLessThanOrEqual(13)
      expect(l.px, `"${l.text}" is ${l.px}px`).toBeGreaterThanOrEqual(10)
    }
  })

  test('emphasis is visible, and a pressed segment is visible', async ({ page }) => {
    await signIn(page)
    await page.goto('/sanctions')
    await expect(page.locator('nav.rail')).toBeVisible()

    // A tone that does not survive the kit is worse than no tone: the screen
    // still reads as though it has one. So the assertion is a comparison, not a
    // colour — whatever primary is, it must not be what plain is.
    const tones = await page.locator('button').evaluateAll((els) => {
      const of = (re: RegExp) => {
        const el = els.find((e) => re.test(e.textContent ?? ''))
        if (!el) return null
        const cs = getComputedStyle(el)
        return { bg: cs.backgroundColor, color: cs.color }
      }
      return { primary: of(/^Search$/i), plain: of(/refresh/i) }
    })
    expect(tones.primary, 'the primary action was not found').not.toBeNull()
    expect(tones.plain, 'a plain action was not found').not.toBeNull()
    expect(tones.primary!.bg, 'a primary button looks exactly like a plain one').not.toBe(tones.plain!.bg)

    await page.goto('/cases')
    const segments = await page.locator('.tabs button').evaluateAll((els) =>
      els.map((el) => ({
        on: el.getAttribute('aria-pressed') === 'true',
        bg: getComputedStyle(el).backgroundColor,
        color: getComputedStyle(el).color,
      })),
    )
    const on = segments.filter((s) => s.on)
    const off = segments.filter((s) => !s.on)
    expect(on.length, 'exactly one segment should be selected').toBe(1)
    expect(off.length).toBeGreaterThan(0)
    expect(
      on[0].bg !== off[0].bg || on[0].color !== off[0].color,
      'the selected segment is indistinguishable from the unselected ones',
    ).toBe(true)
  })

  test('nothing is invisible against itself', async ({ page }) => {
    await signIn(page)
    await page.goto('/relationships')
    await expect(page.locator('nav.rail')).toBeVisible()

    // The theme-bridge failure mode: a control whose ground and whose text both
    // resolve through the same variable, read in a scope where that variable is
    // the wrong theme's. It renders, it is styled, it violates no policy, and
    // it cannot be read. Every control that paints a ground must differ from
    // the ink on it.
    const unreadable = await page.locator('select, input, textarea, button').evaluateAll((els) =>
      els
        .map((el) => {
          const cs = getComputedStyle(el)
          return { tag: el.tagName, bg: cs.backgroundColor, color: cs.color }
        })
        // A transparent ground takes the surface underneath, which is the
        // surface the rest of the screen was already judged against.
        .filter((s) => !/rgba\(0, 0, 0, 0\)|transparent/.test(s.bg))
        .filter((s) => s.bg === s.color),
    )
    expect(unreadable, 'a control paints its text in its own background colour').toEqual([])
  })
})
