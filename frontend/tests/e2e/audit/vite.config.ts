import { fileURLToPath } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

const output = process.env.GO_ADMIN_AUDIT_E2E_OUT_DIR
if (!output) throw new Error('GO_ADMIN_AUDIT_E2E_OUT_DIR is required')

export default defineConfig({
  root: fileURLToPath(new URL('.', import.meta.url)),
  plugins: [vue()],
  resolve: {
    alias: {
      '@go-admin-plus/domain-audit': fileURLToPath(new URL('../../../packages/domains/audit/src/index.ts', import.meta.url)),
      '@go-admin-plus/web-domain-audit': fileURLToPath(new URL('../../../packages/web-domains/audit/src/index.ts', import.meta.url)),
    },
  },
  build: {
    emptyOutDir: true,
    outDir: output,
  },
})
