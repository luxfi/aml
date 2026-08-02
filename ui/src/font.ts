/*
 * The typeface this console is set in, and where its bytes come from.
 *
 * The STACK — which families to try and in what order — is NOT decided here. It
 * is @hanzo/gui's, read from the kit's font packages in src/gui.ts, so every
 * Hanzo property is set in the same list and this console cannot drift from it.
 * What is decided here is DELIVERY: which release, which files, which origin,
 * and the @font-face rules that install them.
 *
 * Geist is Vercel's, published under the SIL Open Font License 1.1. The bytes
 * served are the ones in the `geist` package at the version below, pinned — so
 * the licence that was read is the licence of the files that ship — and the OFL
 * text travels with them: beside the woff2 on the CDN, and emitted beside them
 * into the bundle when this console self-hosts (see vite.config.ts).
 *
 * One variable file per family carries every weight from 100 to 900, which is
 * why there are two requests here and not the eighteen a static-weight cut of
 * the same two families would need.
 */

/**
 * Our own CDN, and the only origin this console will fetch a face from unless
 * it is told otherwise.
 *
 * It is ours — the `cdn` app in front of our S3 — which is the whole difference
 * between this and a third-party font host: one copy, one version, one cache,
 * every property, and no request to anybody else's infrastructure. It already
 * answers with `access-control-allow-origin: *`, which a cross-origin face
 * requires and which is the reason a font host cannot simply be any static host.
 */
export const cdn = 'https://cdn.hanzo.ai'

/**
 * The Geist release these files are cut from.
 *
 * It is part of the path, so a new release is a NEW path and never a new copy
 * at an old one. That is what lets the files be cached for a year: nothing at
 * a given path is ever rewritten, so nothing cached from it can go stale.
 */
export const version = '1.7.2'

/** The immutable directory the two faces live in, on whichever origin serves them. */
export const dir = `/fonts/geist/${version}`

/**
 * The two files, in the order first paint needs them: the UI face, then the one
 * ids, hashes, code and monospace columns are set in.
 *
 * `file` is the name the fleet serves and every property fetches — one URL, so
 * one cache entry shared across all of them. It is the fleet's name and not the
 * upstream one, which is what `cut` and `from` record: where in the `geist`
 * package these exact bytes come from, for the self-hosted build and for the
 * suite, so both serve what the CDN serves rather than something like it.
 */
export const faces = [
  { file: 'GeistVariable.woff2', cut: 'geist-sans', from: 'Geist-Variable.woff2' },
  { file: 'GeistMonoVariable.woff2', cut: 'geist-mono', from: 'GeistMono-Variable.woff2' },
] as const

export type Face = (typeof faces)[number]

/** Where a face's bytes are, on a given origin. An empty origin is this app's own. */
export const url = (origin: string, face: Face): string => `${origin}${dir}/${face.file}`

/**
 * The family a stack leads with, unquoted.
 *
 * The name an @font-face registers and the name a stack asks for have to be the
 * same string or the face is installed and never used — a failure that renders
 * perfectly, in the fallback. So neither is written down twice: the stack is the
 * kit's, and the face takes its name from the front of it. When the kit renames
 * the family, the face is renamed with it and there is nothing to keep in step.
 */
export const lead = (stack: string): string => (stack.split(',')[0] ?? '').trim().replace(/^["']|["']$/g, '')

/**
 * The origin this build fetches its faces from, fixed at build time by
 * vite.config.ts.
 *
 * It has to be fixed at build rather than fetched at run: a face is needed for
 * the first paint, and anything the console would have to ask for first arrives
 * too late to set the first frame in.
 *
 * A function, not a constant, so importing this module in Node — which
 * vite.config.ts and the suite both do, for the paths and the face list — never
 * evaluates a symbol that only exists inside the bundle.
 */
declare const __FONT_ORIGIN__: string
export const origin = (): string => __FONT_ORIGIN__

/**
 * The @font-face rules that install the two faces, each under the name the
 * stack it belongs to leads with.
 *
 * `font-display: swap` so the text of a risk console is never invisible: it is
 * painted immediately in the fallback and re-set when the face arrives. With
 * both files preloaded that window is short, and a swap that never happens
 * because the network is gone still leaves a readable screen — which is why the
 * stack behind Geist ends in a system face and never in a serif.
 *
 * These go on through the CSSOM with the kit's own stylesheet (src/gui.ts), so
 * `style-src` never sees a stylesheet parsed from markup and the served policy
 * needs no hash for this and no 'unsafe-inline'.
 */
export const sheet = (from: string, stacks: readonly string[]): string =>
  faces
    .map(
      (f, i) =>
        `@font-face{font-family:"${lead(stacks[i])}";` +
        `src:url("${url(from, f)}") format("woff2");` +
        `font-weight:100 900;font-style:normal;font-display:swap}`,
    )
    .join('')
