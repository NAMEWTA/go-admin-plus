import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig(({ mode }) => ({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@desktop-entry': fileURLToPath(new URL(mode === 'native-e2e' ? './src/native-e2e/App.vue' : './src/App.vue', import.meta.url))
    }
  },
  build: { outDir: 'dist', emptyOutDir: true },
  server: { host: '127.0.0.1', port: 1420, strictPort: true }
}))
