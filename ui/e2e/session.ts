// A signed-in console, seeded the way the SDK stores a session.
//
// Shared by every spec that needs the signed-in tree, because the seeding has
// to match what @hanzo/iam actually reads. Two copies of it would drift, and a
// drifted copy does not fail — it silently tests the sign-in gate instead of
// the six screens behind it.

import type { Page } from '@playwright/test'

import { serveFonts } from './cdn'

export async function signIn(page: Page) {
  // Every origin this console reaches is answered locally, so the run is
  // offline and its result is about the console rather than about the network.
  // The faces come first because they are fetched at first paint, before
  // anything below has been read.
  await serveFonts(page)

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
