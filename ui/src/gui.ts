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

export const config = createGui(getDefaultGuiConfig('web'))
export default config

type Conf = typeof config
declare module '@hanzogui/core' {
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  interface GuiCustomConfig extends Conf {}
}

/**
 * Put the kit's stylesheet on the document without writing markup.
 *
 * Called once, before the first render, so the first paint is styled. It
 * reports whether it worked: a browser with no constructable stylesheets
 * renders unstyled, which is safe and obvious, rather than falling back to a
 * <style> element the policy would refuse anyway.
 */
export function adoptStyles(doc: Document = document): boolean {
  if (typeof CSSStyleSheet === 'undefined' || !('adoptedStyleSheets' in doc)) return false
  const sheet = new CSSStyleSheet()
  sheet.replaceSync(config.getCSS())
  doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, sheet]
  return true
}
