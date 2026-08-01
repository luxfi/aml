// What the console does under the policy it is actually served with, SIGNED IN.
//
// This is the test that was previously asserted rather than run. Two things
// make it worth having:
//
//  1. The signed-in tree is where nearly all of this app is. A CSP check on the
//     sign-in gate exercises one card; the screens behind it are the six that
//     render every control, every panel and every table the kit draws.
//  2. @hanzo/gui styles by inserting rules at runtime. That is precisely the
//     kind of thing a strict style-src refuses, and whether the arrangement in
//     src/gui.ts actually avoids it is a question about a browser, not about a
//     bundle. So a browser answers it.
//
// A violation is caught two ways, because either alone can miss: the
// `securitypolicyviolation` event fires for a blocked resource or style, and
// console messages catch the report Chromium prints even where no event
// listener was attached yet.

import { expect, test, type ConsoleMessage, type Page } from '@playwright/test'

/** A session, seeded the way the SDK stores one, before any script runs. */
async function signIn(page: Page) {
  await page.addInitScript(() => {
    const hour = Date.now() + 60 * 60 * 1000
    sessionStorage.setItem('hanzo_iam_access_token', 'at-e2e')
    sessionStorage.setItem('hanzo_iam_refresh_token', 'rt-e2e')
    sessionStorage.setItem('hanzo_iam_expires_at', String(hour))
  })
  // The issuer is a real origin the policy allows and this machine cannot
  // reach. Answering it here keeps the run offline without relaxing anything:
  // the request is still made, and still has to pass connect-src to be made.
  await page.route('https://hanzo.id/**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        sub: 'u-1',
        name: 'A. Mensah',
        email: 'a.mensah@acme.test',
        owner: 'hanzo/acme',
      }),
    }),
  )
}

type Watch = { violations: string[]; errors: string[] }

function watch(page: Page): Watch {
  const w: Watch = { violations: [], errors: [] }
  page.on('console', (m: ConsoleMessage) => {
    const text = m.text()
    if (/Content Security Policy|Refused to (apply|load|execute)/i.test(text)) w.violations.push(text)
    else if (m.type() === 'error') w.errors.push(text)
  })
  page.on('pageerror', (e) => w.errors.push(`pageerror: ${e.message}`))
  return w
}

/** The listener has to be installed before the document's own scripts run. */
async function listen(page: Page) {
  await page.addInitScript(() => {
    const store: string[] = []
    ;(window as unknown as { __csp: string[] }).__csp = store
    document.addEventListener('securitypolicyviolation', (e) => {
      store.push(`${e.violatedDirective} blocked ${e.blockedURI || '(inline)'}`)
    })
  })
}

async function reported(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? [])
}

test.describe('the console under the served CSP, signed in', () => {
  test('the shell mounts and the policy is the deployed one', async ({ page }) => {
    await listen(page)
    await signIn(page)
    const w = watch(page)

    const res = await page.goto('/')
    expect(res?.status()).toBe(200)

    // The header under test is the one the chart sets. If this drifts, every
    // other assertion in this file is about the wrong policy.
    const policy = res?.headers()['content-security-policy'] ?? ''
    expect(policy).toContain("default-src 'none'")
    expect(policy).toContain("script-src 'self'")
    expect(policy).toContain("style-src 'self' 'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='")
    expect(policy).not.toContain('unsafe-inline')
    expect(policy).not.toContain('unsafe-eval')

    // Signed in: the rail is showing, not the sign-in card.
    await expect(page.locator('nav.rail')).toBeVisible()
    await expect(page.getByRole('button', { name: /sign in/i })).toHaveCount(0)

    expect(await reported(page)).toEqual([])
    expect(w.violations).toEqual([])
  })

  test('the kit is actually styled — the stylesheet went on without markup', async ({ page }) => {
    await listen(page)
    await signIn(page)
    await page.goto('/')
    await expect(page.locator('nav.rail')).toBeVisible()

    // The kit's CSS reaches the document through a constructed stylesheet
    // (src/gui.ts). If that had been refused, or skipped, this app would render
    // as unstyled markup — which is the failure mode a CSP-violation count of
    // zero would otherwise hide.
    const adopted = await page.evaluate(() => document.adoptedStyleSheets.length)
    expect(adopted).toBeGreaterThan(0)

    // And no <style> element carrying text was ever parsed from markup. Empty
    // holders are what the kit inserts rules into at runtime, and the empty
    // string is what the policy's hash allows; a non-empty one would mean the
    // provider wrote its stylesheet as markup after all.
    const inlineStyleText = await page.evaluate(() =>
      Array.from(document.querySelectorAll('style'))
        .map((s) => s.textContent ?? '')
        .filter((t) => t.trim() !== '').length,
    )
    expect(inlineStyleText).toBe(0)

    // Style attributes need care, because two different things produce one and
    // the policy treats them differently — correctly.
    //
    // The kit's theme root carries `color: var(--color); display: contents`.
    // React writes that through element.style, which is the CSSOM: the script
    // doing it already passed script-src, so style-src does not govern it and
    // the declaration APPLIES. A style attribute PARSED FROM MARKUP is the
    // dangerous one — that is the shape an injection takes — and that one is
    // refused. Both halves are asserted, because "no violations" means nothing
    // without evidence that a violation would have been noticed.
    const styled = await page.evaluate(() =>
      Array.from(document.querySelectorAll('[style]')).map((el) => ({
        cls: el.className,
        display: getComputedStyle(el).display,
      })),
    )
    for (const el of styled) {
      expect(el.cls, 'only the kit theme root may carry a style attribute').toContain('is_Theme')
      expect(el.display, 'the kit theme root is inert, so its display must have applied').toBe('contents')
    }

    // The positive control. If this stopped being refused, the policy would
    // have gone soft and every other assertion here would still pass.
    const injected = await page.evaluate(() => {
      const host = document.createElement('div')
      host.innerHTML = '<b id="csp-probe" style="display:none">probe</b>'
      document.body.appendChild(host)
      return getComputedStyle(document.getElementById('csp-probe') as Element).display
    })
    expect(injected, 'a style attribute parsed from markup was APPLIED — style-src is not enforcing').toBe('inline')
    expect((await reported(page)).join(' ')).toContain('style-src-attr')

    // The layout is real: the rail has the width the stylesheet gives it.
    const railWidth = await page.evaluate(() => {
      const el = document.querySelector('nav.rail')
      return el ? Math.round(el.getBoundingClientRect().width) : 0
    })
    expect(railWidth).toBeGreaterThan(100)
  })

  test('every screen the rail offers renders with no violation', async ({ page }) => {
    await listen(page)
    await signIn(page)
    const w = watch(page)
    await page.goto('/')
    await expect(page.locator('nav.rail')).toBeVisible()

    // Taken from the rail rather than hard-coded, so a screen added without a
    // test is still a screen this covers.
    const paths = await page.locator('nav.rail .nav a').evaluateAll((as) =>
      as.map((a) => new URL((a as HTMLAnchorElement).href).pathname),
    )
    expect(paths.length).toBeGreaterThanOrEqual(6)

    for (const path of paths) {
      await page.goto(path)
      await expect(page.locator('nav.rail')).toBeVisible()
      // The screen produced something: a heading, and a body that is not empty.
      await expect(page.locator('main.view')).not.toBeEmpty()
      expect(await reported(page), `${path} violated the policy`).toEqual([])
    }

    // A deep link, which is what -spa has to resolve.
    await page.goto('/cases/c-1')
    await expect(page.locator('nav.rail')).toBeVisible()
    expect(await reported(page)).toEqual([])

    expect(w.violations).toEqual([])
    expect(w.errors).toEqual([])
  })

  test('the failure UI does not repeat what the URL told it', async ({ page }) => {
    await listen(page)
    await signIn(page)

    // The callback error branch, reached with an error this browser never
    // started. @hanzo/iam checks the state before it reads the query string
    // (v0.21.5); this is the console-side proof that it does.
    const lure = 'Session expired. Call IT on 555-0100 to restore access'
    await page.goto(`/callback?error=access_denied&error_description=${encodeURIComponent(lure)}`)
    await expect(page.locator('body')).toContainText(/cannot start|did not start/i)
    await expect(page.locator('body')).not.toContainText('555-0100')

    expect(await reported(page)).toEqual([])
  })
})
