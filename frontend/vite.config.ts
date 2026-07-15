import {defineConfig, Plugin} from 'vite'
import react from '@vitejs/plugin-react'
import {mkdirSync, writeFileSync} from 'node:fs'
import {resolve} from 'node:path'

// main.go embeds this directory (//go:embed all:frontend/dist), so it must
// exist before any Go code compiles — a tracked dist/.gitkeep is what makes
// `go test ./...` work on a fresh clone. Vite empties outDir on every build,
// which would delete that placeholder and leave the tree dirty (release.sh
// refuses to run on a dirty tree). Put it back once the bundle is written.
function keepDistPlaceholder(): Plugin {
  let placeholderDir = ''
  return {
    name: 'keep-dist-placeholder',
    apply: 'build',
    configResolved(config) {
      placeholderDir = resolve(config.root, config.build.outDir)
    },
    closeBundle() {
      mkdirSync(placeholderDir, {recursive: true})
      writeFileSync(resolve(placeholderDir, '.gitkeep'), '')
    },
  }
}

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react(), keepDistPlaceholder()]
})
