import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// Builds a static SPA embedded into the Go binary (go:embed web/dist).
//   - base './' so assets resolve no matter where the server roots them.
//   - outDir ../dist is what web/embed.go embeds.
//   - dev proxy forwards /api to a locally-running `coinrollhunter serve`.
// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  base: './',
  resolve: {
    alias: {
      $lib: fileURLToPath(new URL('./src/lib', import.meta.url)),
    },
  },
  build: {
    outDir: '../dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8787',
    },
  },
})
