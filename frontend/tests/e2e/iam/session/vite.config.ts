import { fileURLToPath } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

const output = process.env.GO_ADMIN_IAM_E2E_OUT_DIR
if (!output) throw new Error('GO_ADMIN_IAM_E2E_OUT_DIR is required')

export default defineConfig({
  root: fileURLToPath(new URL('.', import.meta.url)),
  plugins: [vue()],
  build: {
    emptyOutDir: true,
    outDir: output,
  },
})
