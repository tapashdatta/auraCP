import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Builds to ../cmd/auracpd/dist later for Go embed; default dist/ for now.
export default defineConfig({
  plugins: [svelte()],
  build: {
    target: 'es2022',
    cssCodeSplit: false,
  },
})
