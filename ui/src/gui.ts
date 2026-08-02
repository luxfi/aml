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
import { geistFontFace, geistMono, geistSans } from '@hanzogui/font-geist'

/**
 * The kit's configuration, unmodified.
 *
 * There is no `fonts` override here and there must never be one again. The kit
 * binds the UI face to `body` and `heading` and the monospace one to `mono`
 * itself, so every Hanzo property is set in the same two faces and this console
 * has no opinion left to hold.
 *
 * The override this replaces existed because the kit named its families
 * `System` and `Heading` — React Native family names, which that platform
 * resolves and the web does not. The kit emits the name verbatim as
 * `--f-family`, every component reads `font-family: var(--f-family)`, and a
 * family no browser knows leaves the document on its default, which is a serif.
 * That is how the whole console came to render in Times New Roman while the
 * build, the typecheck, the CSP run and the render tests all passed. The fix
 * belonged in the kit, and is now in the kit.
 */
export const config = createGui(getDefaultGuiConfig('web'))
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
 * The kit's two stacks, under the names this console's stylesheet uses.
 *
 * The kit dresses its own components from the config above. Everything it does
 * not own — body, tables, ids, code — reads `--hz-font-sans` and
 * `--hz-font-mono` from app.css, and the kit publishes its own font variables
 * as `:root .font_body { … }`, a DESCENDANT selector, so they are always a
 * subtree and can never reach :root for a plain element to read. This is the
 * adapter across that seam, and it is the same shape app.css already uses for
 * the theme.
 *
 * It states no typeface: both values are imported, so there is one answer to
 * "what is this console set in" and no second place to change it.
 *
 * At `:root`, which beats the floor app.css declares on `html` on SPECIFICITY
 * rather than on source order — so it does not matter where in the cascade an
 * adopted stylesheet lands.
 */
const stacks = `:root{--hz-font-sans:${geistSans};--hz-font-mono:${geistMono}}`

/**
 * Put the console's stylesheets on the document without writing markup.
 *
 * Called once, before the first render, so the first paint is styled and the
 * faces are already being fetched. It reports whether they went on: a browser
 * with no constructable stylesheets renders unstyled, which is safe and
 * obvious, rather than falling back to a <style> element the policy would
 * refuse anyway — and app.css carries a floor for that case, because the
 * alternative to "no typeface" is not nothing, it is a serif.
 *
 * The face rules are the kit's: which release, which files, which origin, and
 * the rules that install them. They are returned as text rather than injected
 * precisely so that a console under a strict `style-src` can put them through
 * the CSSOM, which is what happens here. Such a rule installs a real face when
 * the sheet is adopted on the Document (a shadow root is the case that never
 * worked), and e2e/font.spec.ts proves the face loads and is what the renderer
 * used, rather than that a rule was written.
 *
 * Two sheets, because they answer different questions — what the typeface is
 * and where its bytes are, then everything the config generates — and in the
 * order the cascade needs: faces before the rules that ask for them.
 */
export function adoptStyles(doc: Document = document): boolean {
  if (typeof CSSStyleSheet === 'undefined' || !('adoptedStyleSheets' in doc)) return false
  const type = new CSSStyleSheet()
  type.replaceSync(geistFontFace() + stacks)
  const kit = new CSSStyleSheet()
  kit.replaceSync(config.getCSS())
  doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, type, kit]
  return true
}
