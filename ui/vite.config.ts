import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The console is served from the root of its own host (aml.hanzo.ai) by
// hanzoai/static in SPA mode, so every deep link — /cases/abc, /callback —
// resolves to index.html and the router takes it from there. It is not mounted
// under a path prefix: the API lives on api.<brand>/v1/aml and this bundle
// lives on its own host, and neither is inside the other.
export default defineConfig({
  plugins: [react()],
  base: '/',
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
