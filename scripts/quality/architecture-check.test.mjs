import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { checkArchitecture } from './architecture-check.mjs'

test('current repository satisfies canonical architecture', () => {
  assert.deepEqual(checkArchitecture(fileURLToPath(new URL('../..', import.meta.url))), [])
})

test('rejects the historical short Go module path', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  mkdirSync(join(root, 'backend'), { recursive: true })
  writeFileSync(join(root, 'backend/go.mod'), 'module go-admin\n')

  assert.ok(checkArchitecture(root).includes(
    'Go module path must be github.com/NAMEWTA/go-admin-plus/backend'
  ))
})

test('rejects the historical frontend workspace scope', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  mkdirSync(join(root, 'frontend'), { recursive: true })
  writeFileSync(join(root, 'frontend/package.json'), '{"name":"@go-admin/workspace"}\n')

  assert.ok(checkArchitecture(root).includes(
    'frontend workspace name must be @go-admin-plus/workspace'
  ))
})

test('rejects removed standalone backend command directories', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const commandRoot = join(root, 'backend/cmd/config-check')
  mkdirSync(commandRoot, { recursive: true })
  writeFileSync(join(commandRoot, 'main.go'), 'package main\n')

  assert.ok(checkArchitecture(root).includes(
    'removed path still exists: backend/cmd/config-check'
  ))
})

test('does not treat optional SpecDev runtime state as product architecture', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const configRoot = join(root, 'speculo/.speculo/specdev')
  const oldFrontend = ['go-admin-ui', 'plus'].join('-')
  mkdirSync(configRoot, { recursive: true })
  writeFileSync(join(configRoot, 'config.json'), JSON.stringify({
    verification: {
      test: `cd ${oldFrontend} && pnpm test:unit`,
      typecheck: `cd ${oldFrontend} && pnpm type-check`,
      lint: `cd ${oldFrontend} && pnpm lint`,
      build: `cd ${oldFrontend} && pnpm build:prod`
    }
  }))

  const failures = checkArchitecture(root)
  assert.ok(!failures.some(message => message.includes('SpecDev')))
})

test('rejects command filters for nonexistent workspace packages', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  mkdirSync(join(root, 'frontend/apps/admin-web'), { recursive: true })
  writeFileSync(join(root, 'frontend/package.json'), '{"name":"@go-admin-plus/workspace"}\n')
  writeFileSync(join(root, 'frontend/apps/admin-web/package.json'), '{"name":"@go-admin-plus/admin-web"}\n')
  const unknownPackage = ['@go-admin-plus', 'missing'].join('/')
  writeFileSync(join(root, 'Taskfile.yml'), `pnpm --filter ${unknownPackage} build\n`)

  assert.ok(checkArchitecture(root).includes(
    `Taskfile.yml references unknown workspace package ${unknownPackage}`
  ))
})

test('rejects a root typecheck command that can omit workspace package checks', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const workspaceRoot = join(root, 'frontend')
  const packageRoot = join(workspaceRoot, 'packages/domains/audit')
  mkdirSync(packageRoot, { recursive: true })
  writeFileSync(join(workspaceRoot, 'package.json'), JSON.stringify({
    name: '@go-admin-plus/workspace',
    scripts: {
      typecheck: 'pnpm --filter @go-admin-plus/domain-iam typecheck'
    }
  }))
  writeFileSync(join(packageRoot, 'package.json'), JSON.stringify({
    name: '@go-admin-plus/domain-audit',
    scripts: {
      typecheck: 'tsc -p src/tsconfig.json'
    }
  }))

  assert.ok(checkArchitecture(root).includes(
    'frontend root typecheck must recursively run every workspace package typecheck script'
  ))
})

test('rejects a workspace package without an independent typecheck contract', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const packageRoot = join(root, 'frontend/packages/platform')
  mkdirSync(join(packageRoot, 'src'), { recursive: true })
  writeFileSync(join(packageRoot, 'package.json'), JSON.stringify({
    name: '@go-admin-plus/platform'
  }))
  writeFileSync(join(packageRoot, 'src/index.ts'), 'export type Runtime = "web" | "desktop"\n')

  assert.ok(checkArchitecture(root).includes(
    '@go-admin-plus/platform must declare a typecheck script'
  ))
})

test('rejects a workspace package spec without an independent test contract', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const packageRoot = join(root, 'frontend/packages/adapters/desktop')
  mkdirSync(join(packageRoot, 'src'), { recursive: true })
  writeFileSync(join(packageRoot, 'package.json'), JSON.stringify({
    name: '@go-admin-plus/adapter-desktop',
    scripts: { typecheck: 'tsc --noEmit -p tsconfig.json' }
  }))
  writeFileSync(join(packageRoot, 'src/index.spec.ts'), 'export {}\n')

  assert.ok(checkArchitecture(root).includes(
    '@go-admin-plus/adapter-desktop owns package specs and must declare a test script'
  ))
})

test('rejects a frontend test config that can omit workspace package specs', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const testRoot = join(root, 'frontend/tests/shell')
  mkdirSync(testRoot, { recursive: true })
  writeFileSync(join(testRoot, 'vitest.config.ts'), `export default {
    test: { include: ['packages/domains/iam/src/**/*.spec.ts'] }
  }\n`)

  assert.ok(checkArchitecture(root).includes(
    'frontend test discovery must include every workspace package spec'
  ))
})

test('rejects a frontend test config that omits E2E harness unit specs', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const testRoot = join(root, 'frontend/tests/shell')
  mkdirSync(testRoot, { recursive: true })
  writeFileSync(join(testRoot, 'vitest.config.ts'), `export default {
    test: { include: ['packages/**/*.spec.ts'] }
  }\n`)

  assert.ok(checkArchitecture(root).includes(
    'frontend test discovery must include E2E harness unit specs'
  ))
})

test('rejects a frontend root test command that omits Node unit test discovery', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const workspaceRoot = join(root, 'frontend')
  mkdirSync(workspaceRoot, { recursive: true })
  writeFileSync(join(workspaceRoot, 'package.json'), JSON.stringify({
    name: '@go-admin-plus/workspace',
    scripts: {
      test: 'vitest run --config tests/shell/vitest.config.ts',
      typecheck: 'pnpm --recursive --if-present typecheck'
    }
  }))

  assert.ok(checkArchitecture(root).includes(
    'frontend root test must run Node unit test discovery'
  ))
})

test('rejects a root typecheck command that omits an E2E driver project', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const workspaceRoot = join(root, 'frontend')
  const driverRoot = join(workspaceRoot, 'tests/e2e/audit')
  mkdirSync(driverRoot, { recursive: true })
  writeFileSync(join(workspaceRoot, 'package.json'), JSON.stringify({
    name: '@go-admin-plus/workspace',
    scripts: {
      typecheck: 'pnpm --recursive --if-present typecheck'
    }
  }))
  writeFileSync(join(driverRoot, 'tsconfig.json'), '{}\n')

  assert.ok(checkArchitecture(root).includes(
    'frontend root typecheck omits test project tests/e2e/audit/tsconfig.json'
  ))
})

test('rejects aggregate local frontend packaging', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const scriptRoot = join(root, 'scripts/frontend')
  mkdirSync(scriptRoot, { recursive: true })
  writeFileSync(join(scriptRoot, 'package.sh'), 'pnpm build:prod\n')

  assert.ok(checkArchitecture(root).includes(
    'local package script must not invoke the aggregate frontend build'
  ))
})

test('rejects a Desktop build that compiles only WebView assets', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const scriptRoot = join(root, 'scripts/frontend')
  mkdirSync(scriptRoot, { recursive: true })
  writeFileSync(join(scriptRoot, 'build.sh'), 'pnpm build:prod\n')

  const failures = checkArchitecture(root)
  assert.ok(failures.includes('Desktop build must stage the host Go sidecar'))
  assert.ok(failures.includes('Desktop build must compile the Tauri host without bundling'))
  assert.ok(failures.includes('Desktop build must verify production WebView, sidecar, and host artifacts'))
  assert.ok(failures.includes('aggregate product build must include native Desktop'))
  assert.ok(failures.includes('Desktop target must use the native build'))
})

test('rejects Desktop CI that checks Rust without linking the native host', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const workflowRoot = join(root, '.github/workflows')
  mkdirSync(workflowRoot, { recursive: true })
  writeFileSync(join(workflowRoot, 'ci.yml'), `jobs:
  desktop-rust:
    steps:
      - run: cargo check --locked --release --features custom-protocol
`)

  const failures = checkArchitecture(root)
  assert.ok(failures.includes('Desktop CI must install the frozen frontend workspace'))
  assert.ok(failures.includes('Desktop CI must stage the host Go sidecar'))
  assert.ok(failures.includes('Desktop CI must link the Tauri host without bundling'))
  assert.ok(failures.includes('Desktop CI must verify production WebView, sidecar, and host artifacts'))
})

test('rejects optional or incomplete PostgreSQL and supply-chain CI jobs', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const workflowRoot = join(root, '.github/workflows')
  mkdirSync(workflowRoot, { recursive: true })
  writeFileSync(join(workflowRoot, 'ci.yml'), `jobs:
  postgres-required:
    continue-on-error: true
    services:
      postgres:
        image: postgres:latest
  security-supply-chain:
    continue-on-error: true
    steps:
      - run: npm audit || true
`)

  const failures = checkArchitecture(root)
  assert.ok(failures.includes('required PostgreSQL CI must pin the service version and manifest digest'))
  assert.ok(failures.includes('required PostgreSQL CI must enable the non-skip contract'))
  assert.ok(failures.includes('required PostgreSQL CI must not allow failures'))
  assert.ok(failures.includes('security CI must pin govulncheck'))
  assert.ok(failures.includes('security CI must run the repository security gate'))
  assert.ok(failures.includes('security CI must not allow failures'))
})

test('rejects frontend task scripts that bypass managed pnpm resolution', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const scriptRoot = join(root, 'scripts/frontend')
  mkdirSync(scriptRoot, { recursive: true })
  writeFileSync(join(scriptRoot, 'test.sh'), 'exec pnpm test\n')

  assert.ok(checkArchitecture(root).includes(
    'frontend task script must use managed pnpm resolution: scripts/frontend/test.sh'
  ))
})

test('rejects an unpinned root command toolchain and incomplete setup docs', () => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-architecture-'))
  const workflowRoot = join(root, '.github/workflows')
  const scriptRoot = join(root, 'scripts/backend')
  const docsRoot = join(root, 'docs')
  mkdirSync(workflowRoot, { recursive: true })
  mkdirSync(scriptRoot, { recursive: true })
  mkdirSync(docsRoot, { recursive: true })
  writeFileSync(join(workflowRoot, 'ci.yml'), `jobs:
  quality:
    steps:
      - run: go install github.com/go-task/task/v3/cmd/task@latest
`)
  writeFileSync(join(scriptRoot, 'task-contract.sh'), `#!/bin/sh
task_command=$(command -v task)
"$task_command" --list --json
`)
  writeFileSync(join(root, 'README.md'), '# Product\n\nRun `task dev`.\n')
  writeFileSync(join(docsRoot, 'development.md'), '# Development\n\nInstall Node and Rust.\n')

  const failures = checkArchitecture(root)
  assert.ok(failures.includes('quality CI must install Go Task 3.48.0'))
  assert.ok(failures.includes('root command contract must require Go Task 3.48.0'))
  assert.ok(failures.includes('README must declare Go Task 3.48.0'))
  assert.ok(failures.includes('development guide must install Go Task 3.48.0 reproducibly'))
  assert.ok(failures.includes('development guide must record the Node.js 22.22.3 CI baseline'))
  assert.ok(failures.includes('development guide must record the Rust 1.96.0 CI baseline'))
  assert.ok(failures.includes('development guide must pin pnpm 11.1.3 in the Corepack command'))
})
