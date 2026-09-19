import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

const output = process.env.GO_ADMIN_IAM_ADMIN_E2E_OUT_DIR
if (!output) throw new Error('GO_ADMIN_IAM_ADMIN_E2E_OUT_DIR is required')
const workspaceRoot = fileURLToPath(new URL('../../../..', import.meta.url))
export default defineConfig({ root: fileURLToPath(new URL('.', import.meta.url)), resolve: { alias: { vue: resolve(workspaceRoot, 'packages/web-domains/iam/node_modules/vue') } }, plugins: [vue()], build: { emptyOutDir: true, outDir: output } })
