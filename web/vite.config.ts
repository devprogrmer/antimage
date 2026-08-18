import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { Plugin } from 'vite'
// vitest/config re-exports Vite's defineConfig with the `test` block typed;
// importing it from 'vite' would reject the block below.
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The bundle is served by Go through go:embed, and an embed pattern resolves
// relative to the directory holding the .go file. Building into
// internal/panel/webui/dist is what makes `//go:embed all:dist` in embed.go
// pick this output up; building into web/dist would embed nothing.
const outDir = resolve(import.meta.dirname, '../internal/panel/webui/dist')

// emptyOutDir wipes everything in outDir, including the committed .gitkeep
// that lets the embed compile on a checkout where the UI was never built.
// Writing it back keeps a routine build from showing up as a deleted file.
function keepEmbedPlaceholder(): Plugin {
  return {
    name: 'antimage-keep-embed-placeholder',
    closeBundle() {
      writeFileSync(resolve(outDir, '.gitkeep'), '')
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss(), keepEmbedPlaceholder()],
  build: {
    outDir,
    emptyOutDir: true,
  },
  // Component tests need a DOM; the pure i18n and parser tests do not care.
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/setupTests.ts'],
  },
})
