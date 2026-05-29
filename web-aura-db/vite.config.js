import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Builds the Aura DB workstation SPA. Output goes to dist/ (the Makefile
// copies it to ../internal/dbadmin/webui/dist for go:embed).
//
// Mounted at /dbadmin/ in auracpd; cohabits with the panel SPA mounted at /.
// Dev: same /api proxy as the panel so the daemon serves both shells.
export default defineConfig({
  plugins: [svelte()],
  base: '/dbadmin/',
  build: {
    target: 'es2022',
    cssCodeSplit: false,
    sourcemap: false,
    minify: 'esbuild',
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8443',
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.js', 'tests/**/*.test.js'],
    // The svelte plugin's resolveId hook lets us import .svelte.js modules
    // (runes-enabled) the same way `vite build` does.
    server: { deps: { inline: ['svelte'] } },
  },
})
