import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The dev server proxies the API routes to the Go backend so the frontend runs
// same-origin in development, matching both the production single-binary build
// and the nginx image. No CORS surprises between the three.
//
// The ports come from the environment rather than being literals. They were
// literals, and it broke: the backend's default moved from 8080 to 8090 and
// this file kept pointing at 8080, so every request from the SPA failed with a
// proxy ECONNREFUSED that looked like a backend fault. Reading them here means
// `make dev API_PORT=8091 WEB_PORT=5174` moves both halves together.
const apiPort = process.env.API_PORT ?? '8090'
const webPort = Number(process.env.WEB_PORT ?? 5173)
const apiTarget = process.env.VITE_API_PROXY_TARGET ?? `http://localhost:${apiPort}`

const proxy = {
  target: apiTarget,
  changeOrigin: true,
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: webPort,
    // Fail rather than silently sliding to the next free port. A dev server on
    // an unexpected port is the kind of thing you notice twenty minutes later.
    strictPort: true,
    proxy: {
      '/api': proxy,
      // GraphQL and its explorer are served by the same backend, so they need
      // the same treatment — without these the SPA's GraphQL client 404s
      // against the Vite dev server instead of reaching the API.
      '/graphql': proxy,
      '/graphiql': proxy,
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
})
