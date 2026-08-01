// The kit, and the one way its stylesheet reaches the document.
//
// @hanzo/gui is the Hanzo design system. Its components carry their styling as
// props, which the runtime turns into atomic CSS rules — so the console wears
// the fleet's identity, spacing scale and themes rather than a lookalike
// assembled here.
//
// The whole of the CSP question for this console is in this file.
//
// GuiProvider's default is to render the config's stylesheet as
// `<style>{config.getCSS()}</style>`. That is an inline style element: its text
// is parsed from markup, so `style-src` governs it, and allowing it needs
// either 'unsafe-inline' or a hash that changes whenever the theme does.
// Neither is acceptable. 'unsafe-inline' for styles is what makes CSS-based
// exfiltration and UI-redress work on a page that renders text it did not
// write, and a build-coupled hash in a server-side header is a version skew
// waiting to blank the console.
//
// So `disableInjectCSS` turns that off and the same CSS goes on through the
// CSSOM instead. A constructed stylesheet is a scripting API, not markup:
// `style-src` does not govern it, because the script that called it had already
// passed `script-src`. The kit's own per-component rules take the same route at
// runtime, into empty <style> holders — which is the one thing the served
// policy still has to allow, and it allows exactly that and nothing else, by
// the SHA-256 of the empty string.

import { getDefaultGuiConfig } from '@hanzogui/config-default'
import { createGui } from '@hanzogui/core'
import { createGeistMonoFont } from '@hanzogui/font-geist-mono'
import { createGeistSansFont } from '@hanzogui/font-geist-sans'

import { origin, sheet } from './font'

/**
 * The typeface, taken from the kit rather than spelled again here.
 *
 * Geist Sans is the UI face and Geist Mono the monospace one across every Hanzo
 * property, so the STACK — Geist first, then what to try when it has not
 * arrived — is @hanzo/gui's to state and this console's to read. A stack copied
 * into an app is a second source of truth: it does not fail when the kit's
 * changes, it just quietly stops matching the other hundred properties.
 *
 * The kit's default config names its families `System` and `Heading`. Those are
 * React Native family names: on that platform the OS resolves them, and on this
 * one nothing does. The kit emits the name verbatim as `--f-family`, every
 * component reads `font-family: var(--f-family)`, and a family no browser knows
 * falls back to the default serif — which is how the whole console came to
 * render in Times New Roman while the build, the typecheck, the CSP run and the
 * render tests all passed.
 *
 * `mono` is a third role rather than a spare: src/ui.tsx asks the kit for
 * `$mono` on the one control whose content is machine text, and a font token
 * the config does not define resolves to nothing at all.
 */
const sans = createGeistSansFont().family
const mono = createGeistMonoFont().family

const base = getDefaultGuiConfig('web')

export const config = createGui({
  ...base,
  fonts: {
    body: { ...base.fonts.body, family: sans },
    heading: { ...base.fonts.heading, family: sans },
    mono: { ...base.fonts.body, family: mono },
  },
})
export default config

/**
 * The one theme this console renders in. Named here rather than at the provider
 * because two places need it and they must not be able to disagree: the
 * provider, which themes the app, and the document, which has to be in the same
 * theme for the reason below.
 */
export const theme = 'dark'

type Conf = typeof config
declare module '@hanzogui/core' {
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  interface GuiCustomConfig extends Conf {}
}

/**
 * The typeface, said once, to both sides of the seam.
 *
 * The kit dresses its own components from the config above. Everything the kit
 * does not own — body, tables, ids, code — reads `--hz-font-sans` and
 * `--hz-font-mono` from app.css. Both stacks come from the same two constants,
 * so there is one answer to "what is this console set in" and no way for the
 * two halves of the screen to disagree.
 *
 * At `:root`, which beats the floor app.css declares on `html` on specificity
 * rather than on source order — so it does not matter where in the cascade an
 * adopted stylesheet lands.
 */
const typeface = `:root{--hz-font-sans:${sans};--hz-font-mono:${mono}}`

/**
 * Put the console's stylesheets on the document without writing markup.
 *
 * Called once, before the first render, so the first paint is styled and the
 * faces are already being fetched. It reports whether they went on: a browser
 * with no constructable stylesheets renders unstyled, which is safe and
 * obvious, rather than falling back to a <style> element the policy would
 * refuse anyway.
 *
 * Two sheets, because they answer to different owners: the faces and the two
 * stacks are this console's, and the rest is whatever the kit's config
 * generates. @font-face in a constructed stylesheet installs a real face — the
 * rules are adopted on the Document, not on a shadow root, which is the case
 * that never worked — and e2e/font.spec.ts proves the face actually loads
 * rather than that the rule was merely written.
 *
 * The kit publishes a theme as `:root .t_dark` — a descendant selector — so a
 * theme is always a subtree and never reaches :root. app.css bridges the theme
 * to its own names on the app's roots for that reason; see the note there. A
 * typeface is not a theme, which is why it is at :root here and not in that
 * bridge.
 */
export function adoptStyles(doc: Document = document): boolean {
  if (typeof CSSStyleSheet === 'undefined' || !('adoptedStyleSheets' in doc)) return false
  const type = new CSSStyleSheet()
  type.replaceSync(sheet(origin(), [sans, mono]) + typeface)
  const kit = new CSSStyleSheet()
  kit.replaceSync(config.getCSS())
  doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, type, kit]
  return true
}
