// Our CDN, answered locally.
//
// The console fetches its two faces from cdn.hanzo.ai. That is a real origin
// this machine may or may not be able to reach, and a suite whose result
// depends on which is a suite that reports on the network. So the request is
// still MADE — it still has to pass `font-src`, it is still cross-origin, and
// the tests that count cross-origin requests still see it — and it is answered
// here from the `geist` package at the version the bundle pins.
//
// Same reasoning as the issuer in session.ts: keep the run offline without
// relaxing anything.

import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'

import type { Page } from '@playwright/test'

import { cdn, dir, faces } from '../src/font'

const root = dirname(dirname(createRequire(import.meta.url).resolve('geist/font')))

/** The bytes the CDN is expected to be serving, keyed by the path they live at. */
const bytes = new Map(
  faces.map((f) => [`${dir}/${f.file}`, readFileSync(join(root, 'dist', 'fonts', f.cut, f.from))]),
)

/**
 * Serve the faces the way the CDN serves them.
 *
 * `access-control-allow-origin: *` is not decoration: a cross-origin face is
 * always fetched in CORS mode, so a font host without that header serves
 * nothing usable however correct its CSP entry is. cdn.hanzo.ai returns it,
 * which is why it is a font host and not merely a static one.
 *
 * Anything else under that host is a 404, so a test can prove the console asks
 * it for the two files and for nothing else.
 */
export async function serveFonts(page: Page) {
  await page.route(`${cdn}/**`, (route) => {
    const body = bytes.get(new URL(route.request().url()).pathname)
    if (!body) return route.fulfill({ status: 404, body: 'not found' })
    return route.fulfill({
      status: 200,
      headers: {
        'content-type': 'font/woff2',
        'access-control-allow-origin': '*',
        'cache-control': 'public, max-age=31536000, immutable',
      },
      body,
    })
  })
}
