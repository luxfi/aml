import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'

import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

import { dir, faces, url } from './src/font.ts'

/**
 * Where this build fetches its typeface from.
 *
 * SELF-HOSTED by default: vite emits the two faces out of `@hanzo/font` into
 * this bundle and serves them same-origin, which `font-src 'self'` already
 * covers. That is what every other Hanzo surface does — the package IS the one
 * copy of Zen, and a build that carries its own faces cannot be broken by
 * anything outside it.
 *
 * The default USED to be `cdn`, on the reasoning that one shared copy beats one
 * per property. The reasoning is sound and the path does not exist: nothing
 * publishes Zen to it, and every `/fonts/zen/*` URL answers 404 today while
 * `/fonts/geist/1.7.2/` still answers 200. So the shipped default fetched two
 * faces that are not there and every first paint fell to the system stack. The
 * kit reached the same conclusion and removed its own CDN rules for it.
 *
 * `FONT_ORIGIN=https://cdn.hanzo.ai` opts back in, unchanged, for the day the
 * fleet publishes that path — a build argument rather than a fork, same family
 * names, same token names, same @font-face rules, only the origin moves.
 *
 * It is fixed at build because a face is needed for the first paint, and
 * anything the console would have to ask for first arrives too late to set the
 * first frame in.
 */
const origin = process.env.FONT_ORIGIN ?? ''

/**
 * The typeface, delivered.
 *
 * Two jobs, both of which have to agree with src/font.ts about where the files
 * are, which is why they read the paths from it rather than restating them.
 */
function typeface(from: string): Plugin {
  // `@hanzo/font` exports its font files by subpath, but not the licence beside
  // them, so the package root is found through a specifier it does export and
  // both are read from there. The bytes served are that package's, at the
  // version pinned in package.json — which is what makes the licence checkable:
  // the OFL text below is the one shipped with the exact files that ship.
  const root = dirname(dirname(createRequire(import.meta.url).resolve('@hanzo/font/css')))
  const file = (rel: string) => readFileSync(join(root, rel))

  return {
    name: 'typeface',

    // The two files the first paint needs, asked for before the parser has
    // reached the bundle. `crossorigin` is required even when the origin is
    // this one: fonts are always fetched in CORS mode, and a preload whose mode
    // disagrees with the fetch is a second download rather than a saved one.
    transformIndexHtml: () =>
      faces.map((f) => ({
        tag: 'link',
        attrs: {
          rel: 'preload',
          as: 'font',
          type: 'font/woff2',
          href: url(from, f),
          crossorigin: '',
        },
        injectTo: 'head' as const,
      })),

    // Self-hosted: the same two files at the same paths, emitted into the
    // bundle so the app's own origin serves them, with the licence beside them.
    // In the shipped configuration this emits nothing at all, so a console that
    // uses the CDN carries no font bytes.
    generateBundle() {
      if (from !== '') return
      const at = dir.slice(1)
      for (const f of faces) {
        this.emitFile({ type: 'asset', fileName: `${at}/${f.file}`, source: file(`dist/fonts/${f.cut}/${f.from}`) })
      }
      this.emitFile({ type: 'asset', fileName: `${at}/OFL.txt`, source: file('LICENSE.txt') })
    },
  }
}

// The console is served from the root of its own host (aml.hanzo.ai) by
// hanzoai/static in SPA mode, so every deep link — /cases/abc, /callback —
// resolves to index.html and the router takes it from there. It is not mounted
// under a path prefix: the API lives on api.<brand>/v1/aml and this bundle
// lives on its own host, and neither is inside the other.
export default defineConfig({
  plugins: [react(), typeface(origin)],
  base: '/',
  // The origin the @font-face rules point at, resolved once here and read by
  // src/font.ts. A constant rather than an environment lookup, so the bundle
  // carries the answer and not the question.
  define: { __FONT_ORIGIN__: JSON.stringify(origin) },
  // @hanzo/gui is one kit for web and native, so its default configuration
  // reaches for the react-native animation driver even on this side. The kit
  // ships the web stand-in for exactly that; pointing the two react-native
  // specifiers at it keeps one dependency out of a browser bundle that has no
  // use for it. Nothing in this app imports react-native itself.
  resolve: {
    alias: {
      'react-native': '@hanzogui/fake-react-native',
      'react-native-web': '@hanzogui/fake-react-native',
    },
  },
  build: { outDir: 'dist', emptyOutDir: true, sourcemap: false },
  server: {
    port: 3000,
    // Development answers from a real deployment rather than a mock. The
    // shipped config.json leaves `api` empty, which means same origin, so this
    // is the prefix the browser asks the dev server for.
    proxy: {
      '/v1/aml': { target: 'https://aml.hanzo.ai', changeOrigin: true },
    },
  },
})
