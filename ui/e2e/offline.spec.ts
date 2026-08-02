// The console with no way out: the air-gapped build.
//
// Some deployments of this console sit inside a bank's perimeter and cannot
// reach cdn.hanzo.ai, or anything else of ours. They serve the same two faces
// from their own origin. That is a build argument — `FONT_ORIGIN=` — and not a
// fork: same family names, same token names, same @font-face rules, same
// stylesheet, and `font-src 'self'` already covers it.
//
// An escape hatch nobody exercises is a broken escape hatch, and this one is
// found to be broken by a customer, on an install, with no network to debug it
// over. So it is built and driven here rather than reasoned about: its own
// build, in its own directory, on its own port, with the same strict policy.

import { expect, test } from '@playwright/test'
import { execFileSync, spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

import { cdn, dir, faces } from '../src/font'
import { signIn } from './session'

// One worker for this file: it owns a build directory and a port, and two
// workers racing to create both would be testing each other.
test.describe.configure({ mode: 'serial' })

const ui = fileURLToPath(new URL('..', import.meta.url))
const OUT = 'dist-offline'
const PORT = 4320
const at = `http://127.0.0.1:${PORT}`

let server: ReturnType<typeof spawn>

test.beforeAll(async () => {
  execFileSync('npx', ['vite', 'build', '--outDir', OUT], {
    cwd: ui,
    env: { ...process.env, FONT_ORIGIN: '' },
    stdio: 'pipe',
  })
  server = spawn('node', ['e2e/serve.mjs'], {
    cwd: ui,
    env: { ...process.env, PORT: String(PORT), ROOT: OUT },
    stdio: 'pipe',
  })
  // Up when it answers, rather than after a sleep.
  const until = Date.now() + 30_000
  for (;;) {
    try {
      if ((await fetch(`${at}/v1/aml/health`)).ok) break
    } catch {
      /* not yet */
    }
    if (Date.now() > until) throw new Error('the offline build never came up')
    await new Promise((r) => setTimeout(r, 100))
  }
})

test.afterAll(() => server?.kill())

test.describe('the air-gapped build', () => {
  test('serves its own faces, and reaches nothing', async ({ page }) => {
    const seen: string[] = []
    const refused: string[] = []
    page.on('request', (r) => seen.push(r.url()))
    page.on('console', (m) => {
      if (/Content Security Policy|Refused to (apply|load|execute)/i.test(m.text())) refused.push(m.text())
    })
    page.on('requestfailed', (r) => refused.push(`failed ${r.url()}`))

    await signIn(page)
    await page.goto(at)
    await expect(page.locator('nav.rail')).toBeVisible()
    await page.evaluate(() => document.fonts.ready)

    // The same two faces, installed and loaded — from here.
    const loaded = await page.evaluate(() => [...document.fonts].map((f) => f.status).sort())
    expect(loaded).toEqual(['loaded', 'loaded'])

    for (const f of faces) {
      expect(seen, `${f.file} was not served from this origin`).toContain(`${at}${dir}/${f.file}`)
    }

    // And nothing at all left the building. `hanzo.id` is the exception the
    // console is entitled to — it is the identity provider, and an install
    // behind a perimeter points it at one inside the perimeter — but our CDN
    // must not be reached for, not even to fail.
    expect(seen.filter((u) => u.startsWith(cdn)), 'the air-gapped build still asks our CDN').toEqual([])
    expect(refused).toEqual([])
  })

  test('the licence travels with the files', async () => {
    // Geist is Vercel's, under the SIL Open Font License 1.1, which permits
    // redistribution and requires the notice to go with it. Hosting the bytes
    // without the licence beside them is the one way this ships wrong, and it
    // is invisible until somebody asks.
    const ofl = await fetch(`${at}${dir}/OFL.txt`)
    expect(ofl.status).toBe(200)
    const text = await ofl.text()
    expect(text).toContain('SIL OPEN FONT LICENSE Version 1.1')
    expect(text).toContain('Copyright (c) 2023 Vercel')
  })
})
