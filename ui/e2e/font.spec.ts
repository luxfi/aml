// The typeface: that it arrives, that it is the one on the screen, that it is
// the kit's rather than this console's, and what it costs the policy.
//
// A font is the easiest thing in a front end to be wrong about quietly. A
// family the browser cannot resolve is not an error — it silently takes the
// default, which is a serif, and everything renders. A stack written correctly
// but never loaded looks identical to a stack that loaded. `getComputedStyle`
// reports the LIST that was asked for and says nothing about which entry in it
// the glyphs came from, so a test written against it passes just as happily
// when the woff2 404s.
//
// So the load-bearing assertions here go around it, to the two places that
// cannot be talked into agreeing:
//
//   - `CSS.getPlatformFontsForNode`, which reports the face the renderer
//     actually rasterised those glyphs with. If Geist did not arrive, this says
//     Times New Roman, whatever the stylesheet says.
//   - the request log, which says where the bytes came from and how often.
//
// One warning for whoever runs this by hand: Geist may also be INSTALLED on the
// machine, and then the renderer resolves `Geist` locally and every assertion
// below passes with no webfont at all. playwright.config.ts therefore runs
// Chromium under a fontconfig that cannot see it (e2e/fontconfig.xml), and
// `the face on screen came over the wire` is the control that proves it worked.
//
// The faces are served by e2e/cdn.ts, from the `geist` package at the release
// the KIT names, so the run is offline and the cross-origin request is still
// genuinely made and still has to pass `font-src`.

import {
  GEIST_CDN_ORIGIN,
  GEIST_VERSION,
  geistMono,
  geistPreloadHrefs,
  geistSans,
} from '@hanzogui/font-geist'
import { expect, test, type Page } from '@playwright/test'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

import { published, serveFonts } from './cdn'
import { signIn } from './session'

const csp = readFileSync(new URL('../csp.txt', import.meta.url), 'utf8').trim()

/** The family a stack leads with, unquoted. */
const lead = (stack: string): string => (stack.split(',')[0] ?? '').trim().replace(/^["']|["']$/g, '')

/** The two stacks as the document holds them, and the family each one leads with. */
async function stacks(page: Page) {
  const [sans, mono] = await page.evaluate(() => {
    const root = getComputedStyle(document.documentElement)
    return [
      root.getPropertyValue('--hz-font-sans').trim(),
      root.getPropertyValue('--hz-font-mono').trim(),
    ]
  })
  return { sans, mono, sansFace: lead(sans), monoFace: lead(mono) }
}

/**
 * Which face the renderer actually used for an element's glyphs.
 *
 * The node has to be one that carries text of its own — the report is about
 * glyphs that were laid out, so an element whose text all lives in children
 * (`body`, most containers) reports nothing at all.
 */
async function rendered(page: Page, selector: string): Promise<string> {
  await expect(page.locator(selector).first()).not.toBeEmpty()
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('DOM.enable')
  await cdp.send('CSS.enable')
  const { root } = await cdp.send('DOM.getDocument')
  const { nodeId } = await cdp.send('DOM.querySelector', { nodeId: root.nodeId, selector })
  expect(nodeId, `no ${selector} on the page`).toBeTruthy()
  const { fonts } = await cdp.send('CSS.getPlatformFontsForNode', { nodeId })
  expect(fonts.length, `nothing was rasterised for ${selector}`).toBeGreaterThan(0)
  // The face that set most of the glyphs; a stray glyph off a fallback is not
  // what the element is set in.
  return [...fonts].sort((a, b) => b.glyphCount - a.glyphCount)[0].familyName
}

test.describe('the typeface', () => {
  test('both faces load, from our CDN, once each', async ({ page }) => {
    const seen: Array<{ url: string; type: string }> = []
    page.on('request', (r) => seen.push({ url: r.url(), type: r.resourceType() }))

    await signIn(page)
    await page.goto('/cases')
    await expect(page.locator('nav.rail')).toBeVisible()
    await page.evaluate(() => document.fonts.ready)

    // Installed and loaded — not merely declared. @font-face inside a
    // constructed stylesheet is the whole delivery mechanism (src/gui.ts), and
    // this is the assertion that it installs a real face rather than being
    // parsed and dropped.
    const { sansFace, monoFace } = await stacks(page)
    const loaded = await page.evaluate(() =>
      [...document.fonts].map((f) => `${f.family} ${f.status}`).sort(),
    )
    expect(loaded).toEqual([`${sansFace} loaded`, `${monoFace} loaded`].sort())

    // Once each. Both files are preloaded; a preload whose CORS mode disagrees
    // with the fetch that follows is not a saved request but a duplicated one,
    // and the only way to tell is to count.
    for (const href of geistPreloadHrefs()) {
      const hits = seen.filter((r) => r.url === href)
      expect(hits.length, `${href} was fetched ${hits.length} times`).toBe(1)
      expect(hits[0].type).toBe('font')
    }
  })

  test('the console is set in Geist, and its machine text in Geist Mono', async ({ page }) => {
    await signIn(page)
    await page.goto('/cases')
    await expect(page.locator('nav.rail')).toBeVisible()
    await page.evaluate(() => document.fonts.ready)

    // What the stylesheet asks for, and that it is what the KIT asks for. The
    // two stacks in the document must be @hanzo/gui's own strings, because the
    // whole point of the change that put them there is that this console no
    // longer has a typeface of its own to drift with.
    const { sans, mono, sansFace, monoFace } = await stacks(page)
    expect(sans, 'the sans stack is not the kit’s').toBe(geistSans)
    expect(mono, 'the mono stack is not the kit’s').toBe(geistMono)
    expect(sansFace, 'the UI face is not Geist').toBe('Geist')
    expect(monoFace, 'the monospace face is not Geist Mono').toBe('Geist Mono')

    // The tail is pinned too, because the whole point of the fallback is what
    // happens when Geist does not arrive, and the one answer that is never
    // acceptable is the browser's default.
    expect(sans, 'the sans stack can fall back to a serif').toMatch(
      /(^|,\s*)(ui-sans-serif|system-ui)\b/,
    )
    expect(sans.trimEnd()).toMatch(/sans-serif$/)
    expect(mono.trimEnd(), 'the mono stack can fall back to a serif').toMatch(/monospace$/)

    const body = await page.evaluate(() => getComputedStyle(document.body).fontFamily)
    expect(lead(body), 'body is not set in the sans stack').toBe(sansFace)

    // …and what the renderer did with it. These are the ones that a 404 on the
    // CDN, a face registered under a name the stack never asks for, or a rule
    // that was parsed and dropped would each break — and that no amount of
    // correct CSS can fake, because the answer comes from the rasteriser.
    //
    // What it reports is the family name INSIDE the woff2, which is not the
    // name the @font-face registered it under: a CSS family name is an alias.
    // So these two pin the FILE, the assertions above pin the ALIAS, and
    // together they say the stack asked for a face and was given this one. They
    // are also what separates "Geist arrived" from "something arrived": a
    // fallback here reads DejaVu Sans, or Times New Roman.
    //
    // The heading takes its family from nothing but the document, so what it is
    // set in is what body resolved to; it is measured instead of body because
    // an element whose text is all in its children rasterises no glyphs itself.
    expect(await rendered(page, '.bar h1'), 'the UI is not set in Geist').toBe('Geist')
    expect(await rendered(page, '.grid .id'), 'an id is not set in Geist Mono').toBe('Geist Mono')

    // The seam. `ui.tsx` exports a Mono that is a KIT component carrying the
    // same class, and the kit dresses its own components with single-class
    // atomic rules in a stylesheet the cascade places after app.css. A bare
    // `.mono` ties with those on specificity and loses on order, so machine
    // text asked for through the kit came out in the UI face — a rate in
    // proportional digits, in a column, which is exactly where it shows.
    const seam = await page.evaluate(() => {
      // `.font_body` is precisely the class the kit's own font-family rule is
      // written against, so this is the element the collision is about and not
      // one that merely happens to be near it.
      const kit = document.querySelector<HTMLElement>('.font_body')
      if (!kit) return null
      const was = getComputedStyle(kit).fontFamily
      kit.classList.add('mono')
      return { was, now: getComputedStyle(kit).fontFamily }
    })
    expect(seam, 'no kit-rendered element was found to test the seam with').not.toBeNull()
    expect(lead(seam!.was), 'a kit element is not in the sans stack to begin with').toBe(sansFace)
    expect(lead(seam!.now), 'a kit element marked as machine text is not in the mono stack').toBe(
      monoFace,
    )
  })

  test('the face on screen came over the wire', async ({ page }) => {
    // The negative control, and the reason every assertion above can be
    // believed.
    //
    // Geist is INSTALLED on plenty of machines, this one included. Where it is,
    // the renderer resolves the family with no webfont at all and the whole of
    // this file passes against a console that fetches nothing — which is
    // exactly the failure it was written to catch, wearing the costume of a
    // pass. e2e/fontconfig.xml exists to take the local copy away; this proves
    // it did, by removing the only other source and watching the face change.
    //
    // Nothing about the console is altered: the same bundle, the same rules,
    // the same policy. Only the CDN is unreachable, which is a thing that
    // happens.
    //
    // After signIn, which installs the mock CDN: a later route wins, so this
    // has to be registered second or it is the mock that answers and this test
    // proves nothing. It did, once.
    await signIn(page)
    await page.route(`${GEIST_CDN_ORIGIN}/**`, (route) => route.abort())
    await page.goto('/cases')
    await expect(page.locator('nav.rail')).toBeVisible()
    await page.evaluate(() => document.fonts.ready.catch(() => undefined))

    // The stack is unchanged — it still ASKS for Geist, which is the whole
    // reason getComputedStyle cannot answer this question — and the renderer
    // has fallen through it to something else.
    const { sans } = await stacks(page)
    expect(sans, 'the stack changed; this is not the same console').toBe(geistSans)

    const face = await rendered(page, '.bar h1')
    expect(
      face,
      'Geist rendered with the CDN blocked: it is installed on this machine and ' +
        'e2e/fontconfig.xml is not taking it away, so every other assertion here is vacuous',
    ).not.toBe('Geist')

    // And the fall is onto a sans face. A stack that ends in `sans-serif`
    // exists so that the answer is never the browser's default, which is the
    // serif the console shipped with.
    expect(face, 'the fallback is a serif').not.toMatch(/serif$/i)
  })

  test('quantities are set on a fixed advance', async ({ page }) => {
    await signIn(page)
    // The overview, because it is the screen that has both kinds at once: cells
    // a table calls a quantity, and stat tiles, which are not in a table and
    // need it just as much.
    await page.goto('/')
    await expect(page.locator('nav.rail')).toBeVisible()

    const quantities = page.locator('.num, .tile-value')

    // Counted before it is judged. A selector that matches nothing makes the
    // assertion below true and says nothing at all, which is the way a check
    // like this rots: the markup moves, the test keeps passing, and the column
    // it was written for has been ragged for a year.
    await expect(quantities.first()).toBeVisible()
    expect(await quantities.count(), 'no quantity was on screen to judge').toBeGreaterThanOrEqual(5)

    const off = await quantities.evaluateAll((els) =>
      els
        .map((el) => ({
          text: (el.textContent ?? '').trim().slice(0, 16),
          v: getComputedStyle(el).fontVariantNumeric,
        }))
        .filter((x) => !x.v.includes('tabular-nums')),
    )
    expect(off, 'a quantity is set in proportional figures').toEqual([])
  })

  test('the faces are the only thing this console loads from anywhere else', async ({ page }) => {
    const seen: Array<{ url: string; type: string }> = []
    page.on('request', (r) => seen.push({ url: r.url(), type: r.resourceType() }))

    await signIn(page)
    await page.goto('/')
    await expect(page.locator('nav.rail')).toBeVisible()
    await page.evaluate(() => document.fonts.ready)

    const here = new URL(page.url()).origin
    const away = seen.filter((r) => new URL(r.url).origin !== here)

    // Two kinds of cross-origin traffic, and the policy already draws the line
    // between them: `connect-src` governs what the console ASKS (the identity
    // it was already allowed to reach), `font-src` governs what it LOADS. This
    // change adds to the second, so that is what is pinned: every subresource
    // fetched from another origin is a font, and every one of them is ours.
    const loads = away.filter((r) => r.type !== 'xhr' && r.type !== 'fetch')
    expect([...new Set(loads.map((r) => new URL(r.url).origin))]).toEqual([GEIST_CDN_ORIGIN])
    expect([...new Set(loads.map((r) => r.type))]).toEqual(['font'])

    // And the CDN is asked for the two files and for nothing else — no
    // stylesheet, no script, no beacon riding along on the host we just let in.
    const paths = [
      ...new Set(
        away
          .filter((r) => new URL(r.url).origin === GEIST_CDN_ORIGIN)
          .map((r) => new URL(r.url).pathname),
      ),
    ]
    expect(paths.sort()).toEqual([...published].sort())
  })

  test('no face, and no preload, is refused by the policy', async ({ page }) => {
    const refusals: string[] = []
    await page.addInitScript(() => {
      const store: string[] = []
      ;(window as unknown as { __csp: string[] }).__csp = store
      document.addEventListener('securitypolicyviolation', (e) =>
        store.push(`${e.violatedDirective} blocked ${e.blockedURI || '(inline)'}`),
      )
    })
    page.on('console', (m) => {
      if (/Content Security Policy|Refused to (apply|load|execute)/i.test(m.text()))
        refusals.push(m.text())
    })
    page.on('requestfailed', (r) => refusals.push(`failed ${r.url()} ${r.failure()?.errorText}`))

    await signIn(page)
    await page.goto('/cases')
    await expect(page.locator('nav.rail')).toBeVisible()
    await page.evaluate(() => document.fonts.ready)

    expect(await page.evaluate(() => (window as unknown as { __csp: string[] }).__csp)).toEqual([])
    expect(refusals).toEqual([])
  })

  test('the console names no typeface of its own', () => {
    // The deletion IS the change. Every fact about the typeface — the families,
    // the release, the origin, the files, the @font-face rules — belongs to
    // @hanzo/gui, so that this console and the other hundred properties cannot
    // be set in different things. A stack copied back in here would not fail:
    // it would quietly stop matching, which is precisely why it is checked
    // rather than intended.
    //
    // Two shapes are forbidden, and they are the only two ways a typeface can
    // be re-stated: an @font-face RULE (delivery — which release, which files,
    // which origin) and a QUOTED family (the stack). Prose may name Geist and
    // an import specifier must, so neither is what is matched; a CSS family
    // token is always quoted, and a rule is always followed by its brace.
    const src = new URL('../src/', import.meta.url).pathname
    const walk = (at: string): string[] =>
      readdirSync(at, { withFileTypes: true }).flatMap((e) =>
        e.isDirectory() ? walk(join(at, e.name)) : [join(at, e.name)],
      )

    const files = walk(src)
    expect(files.length, 'no sources were read').toBeGreaterThan(5)
    const offend = (rule: RegExp) =>
      files.filter((f) => rule.test(readFileSync(f, 'utf8'))).map((f) => f.slice(src.length))

    expect(offend(/@font-face\s*\{/), 'a source installs a face of its own').toEqual([])
    expect(offend(/["']Geist/i), 'a source writes a Geist family').toEqual([])
  })

  test('the two URLs are the ones the fleet publishes', () => {
    // Written out in full, on purpose, and this is the one place in the suite
    // that does not derive them from the kit.
    //
    // These are not this console's to choose, nor even the kit's to change
    // quietly. They are the fleet's: the same two files, at the same two URLs,
    // cached once for every Hanzo property, published to our CDN by the kit
    // track. A kit release that renamed a file still passes every other test
    // here — the mock serves whatever is asked for — and then 404s in
    // production against the real CDN. So the contract is stated where changing
    // it means editing the agreement.
    //
    // Immutable by version: nothing at these paths is ever rewritten, which is
    // what earns them `max-age=31536000, immutable`. A new Geist is a new
    // directory beside this one, never a new file inside it.
    expect(geistPreloadHrefs()).toEqual([
      'https://cdn.hanzo.ai/fonts/geist/1.7.2/GeistVariable.woff2',
      'https://cdn.hanzo.ai/fonts/geist/1.7.2/GeistMonoVariable.woff2',
    ])
    expect(GEIST_VERSION).toBe('1.7.2')
  })

  test('the CDN costs the policy exactly one host, in exactly one directive', () => {
    // The policy is the deployed one (e2e/serve.mjs sends this file verbatim),
    // so this is a check on what ships. A font host is the cheapest possible
    // widening of a CSP and also the easiest to widen by more than intended —
    // one `*` or one extra directive and the fix has cost more than the bug.
    const directives = new Map(
      csp
        .split(';')
        .map((d) => d.trim())
        .filter(Boolean)
        .map((d) => {
          const [name, ...value] = d.split(/\s+/)
          return [name, value.join(' ')] as const
        }),
    )

    expect(directives.get('font-src')).toBe("'self' https://cdn.hanzo.ai")
    expect(directives.get('default-src')).toBe("'none'")
    expect(directives.get('style-src')).toBe(
      "'self' 'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='",
    )
    expect(directives.get('script-src')).toBe("'self'")
    expect(csp).not.toContain('unsafe-inline')
    expect(csp).not.toContain('unsafe-eval')
    expect(csp).not.toContain('*')

    // The CDN is in font-src and nowhere else: it may serve this console faces
    // and it may not serve it code, style, or a place to send data.
    for (const [name, value] of directives) {
      if (name === 'font-src') continue
      expect(value, `${name} names the CDN`).not.toContain('cdn.hanzo.ai')
    }

    // Every remote host the whole policy names, so a fourth cannot appear
    // without this failing.
    const hosts = [...new Set([...csp.matchAll(/https:\/\/[^\s;]+/g)].map((m) => m[0]))].sort()
    expect(hosts).toEqual(['https://api.hanzo.ai', 'https://cdn.hanzo.ai', 'https://hanzo.id'])
  })
})
