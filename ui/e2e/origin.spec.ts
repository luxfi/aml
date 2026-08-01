// Where this console is willing to send an authenticated request.
//
// The rule used to be a comment. It is a table now, because the interesting
// cases are the ones nobody writes down: a four-label host that reads as
// "aml.hanzo.ai with something in front", a trailing dot, an address literal.
// Each row is a host somebody could serve this bundle from, and the origin it
// would then talk to.

import { expect, test } from '@playwright/test'

import { apiOrigin } from '../src/config'

const https = 'https:'

test.describe('apiOrigin', () => {
  test('derives api.<domain> from aml.<domain>, and from nothing else', () => {
    const table: Array<[string, string]> = [
      // The console, on the hosts it is served on.
      ['aml.hanzo.ai', 'https://api.hanzo.ai'],
      ['aml.lux.network', 'https://api.lux.network'],
      ['aml.zoo.ngo', 'https://api.zoo.ngo'],
      ['aml.pars.network', 'https://api.pars.network'],
      ['aml.co.uk', 'https://api.co.uk'],

      // Same origin: the dev server proxies /v1/aml, so an empty string is the
      // right answer, not a failure.
      ['localhost', ''],
      ['hanzo.ai', ''],
      ['127.0.0.1', ''],
      ['10.0.0.7', ''],
      ['[::1]', ''],
      ['fe80::1', ''],

      // A host in front of the console's own. Four labels, and the old rule
      // read it as a domain to derive from.
      ['evil.aml.hanzo.ai', ''],
      ['a.b.c.d.hanzo.ai', ''],
      ['aml.hanzo.ai.evil.test', 'https://api.hanzo.ai.evil.test'],

      // Not this console.
      ['api.hanzo.ai', ''],
      ['kms.hanzo.ai', ''],
      ['AML.hanzo.ai', ''],

      // Degenerate labels.
      ['aml.hanzo.ai.', ''],
      ['aml..ai', ''],
      ['.aml.hanzo.ai', ''],
      ['aml.hanzo.123', ''],
    ]

    for (const [host, want] of table) {
      expect(apiOrigin(https, host), host).toBe(want)
    }
  })

  test('a host in front of the console gets nothing, whatever the scheme', () => {
    // The one row worth stating twice. `aml.hanzo.ai.evil.test` above IS
    // derived from — correctly: its first label is `aml` and its registrable
    // domain is evil.test, so a console genuinely served there talks to
    // api.hanzo.ai.evil.test and to nothing of ours. What must never derive is
    // a host that puts something in FRONT of ours, because that one resolves
    // inside *.hanzo.ai.
    expect(apiOrigin('http:', 'evil.aml.hanzo.ai')).toBe('')
    expect(apiOrigin('https:', 'evil.aml.hanzo.ai')).toBe('')
  })
})
