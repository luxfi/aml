// Our CDN, answered locally.
//
// The console fetches its two faces from cdn.hanzo.ai. That is a real origin
// this machine may or may not be able to reach, and a suite whose result
// depends on which is a suite that reports on the network. So the request is
// still MADE — it still has to pass `font-src`, it is still cross-origin, and
// the tests that count cross-origin requests still see it — and it is answered
// here with the real bytes, from the `geist` package at the release the kit
// names.
//
// Same reasoning as the issuer in session.ts: keep the run offline without
// relaxing anything.

import { GEIST_CDN_ORIGIN, GEIST_VERSION, geistPreloadHrefs } from '@hanzogui/font-geist'
import type { Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { basename, dirname, join } from 'node:path'

const require_ = createRequire(import.meta.url)

// The package publishes no subpath for its font files — nor for its own
// manifest — so the package root is found through the one specifier it does
// export, and everything else is read off the filesystem from there.
const root = dirname(dirname(require_.resolve('geist/font')))

/**
 * The `geist` release this suite serves, and the proof it is the kit's.
 *
 * The kit publishes the version as part of the URL; the devDependency here is
 * where the bytes for that version come from. Two places name a release, so
 * they are compared rather than trusted: a devDependency bumped without the kit
 * would leave this suite serving 1.7.3 bytes at the 1.7.2 URL and passing,
 * while the console 404s against the real CDN.
 */
const installed = (JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as { version: string })
  .version
if (installed !== GEIST_VERSION) {
  throw new Error(
    `the geist devDependency is ${installed} but @hanzo/gui serves ${GEIST_VERSION}: ` +
      'pin them together, or this suite tests bytes the CDN does not have',
  )
}

/**
 * Where each published file comes from inside the `geist` package.
 *
 * The published names are the fleet's — one URL per face, one cache entry
 * shared by every property — and the package's are Vercel's. This is the only
 * fact about the typeface left on this side, and it is not really about the
 * typeface: it is about the layout of the package these test bytes are read out
 * of. Nothing the browser sees comes from here.
 */
const FROM: Record<string, string> = {
  'GeistVariable.woff2': 'dist/fonts/geist-sans/Geist-Variable.woff2',
  'GeistMonoVariable.woff2': 'dist/fonts/geist-mono/GeistMono-Variable.woff2',
}

/** The paths the fleet publishes the two faces at. */
export const published = geistPreloadHrefs().map((href) => new URL(href).pathname)

const bytes = new Map(published.map((path) => [path, readFileSync(join(root, FROM[basename(path)]))]))

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
  await page.route(`${GEIST_CDN_ORIGIN}/**`, (route) => {
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
