import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Builds to ../cmd/auracpd/dist later for Go embed; default dist/ for now.
export default defineConfig({
  plugins: [svelte()],
  build: {
    target: 'es2022',
    cssCodeSplit: false,
  },
  // Dev: proxy API calls to the auracpd daemon so HMR works against real data.
  server: {
    proxy: {
      '/api': 'http://localhost:8443',
    },
  },
})
