import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    globals: true,
    include: [
      'tests/shell/**/*.spec.ts',
      'tests/e2e/**/*.spec.ts',
      'packages/**/*.spec.ts'
    ]
  }
})
