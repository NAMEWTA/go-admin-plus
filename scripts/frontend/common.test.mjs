import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, dirname, join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

const frontendCommon = fileURLToPath(new URL('./common.sh', import.meta.url))
const backendCommon = fileURLToPath(new URL('../backend/common.sh', import.meta.url))

const executable = (path, body) => {
  writeFileSync(path, `#!/bin/sh\n${body}\n`)
  chmodSync(path, 0o755)
}

const probe = ({
  commonPath = frontendCommon,
  withPnpm = false,
  withCorepack = true,
  pnpmVersion = '11.1.3',
  functionName = 'run_pnpm',
  exitStatus = 0,
  argument = 'verify'
}) => {
  const root = mkdtempSync(join(tmpdir(), 'go-admin-pnpm-contract-'))
  const output = join(root, 'output')
  if (withCorepack) executable(join(root, 'corepack'), `
test "$1" = pnpm@11.1.3
shift
if [ "$1" = --version ]; then printf '%s\\n' '11.1.3'; exit 0; fi
if [ "$1" = outer ]; then exec pnpm nested; fi
printf 'corepack-shim:%s\\n' "$*" > "$GO_ADMIN_PNPM_PROBE"
`)
  if (withPnpm) {
    executable(join(root, 'pnpm'), `if [ "$1" = --version ]; then printf '%s\\n' '${pnpmVersion}'; exit 0; fi
printf 'pnpm:%s\\n' "$*" > "$GO_ADMIN_PNPM_PROBE"
exit ${exitStatus}`)
  }
  const commandName = join(dirname(commonPath), 'common-probe')
  const result = spawnSync('/bin/sh', ['-c', `. "$1"; ${functionName} "$2"`, commandName, commonPath, argument], {
    env: {
      ...process.env,
      GO_ADMIN_ARTIFACTS_DIR: join(root, 'artifacts'),
      GO_ADMIN_PNPM_PROBE: output,
      PATH: [root, '/usr/bin', '/bin'].join(delimiter)
    },
    encoding: 'utf8'
  })
  return { result, output: existsSync(output) ? readFileSync(output, 'utf8').trim() : null }
}

test('uses Corepack when pnpm is unavailable to the managed shell', { skip: process.platform === 'win32' }, () => {
  const { result, output } = probe({})
  assert.equal(result.status, 0, result.stderr)
  assert.equal(output, 'corepack-shim:verify')
})

test('exports the Corepack shim for nested pnpm package scripts', { skip: process.platform === 'win32' }, () => {
  const { result, output } = probe({ argument: 'outer' })
  assert.equal(result.status, 0, result.stderr)
  assert.equal(output, 'corepack-shim:nested')
})

test('prefers an installed pnpm command over Corepack', { skip: process.platform === 'win32' }, () => {
  const { result, output } = probe({ withPnpm: true })
  assert.equal(result.status, 0, result.stderr)
  assert.equal(output, 'pnpm:verify')
})

test('exec_pnpm preserves the package manager exit status', { skip: process.platform === 'win32' }, () => {
  const { result, output } = probe({ withPnpm: true, functionName: 'exec_pnpm', exitStatus: 23 })
  assert.equal(result.status, 23, result.stderr)
  assert.equal(output, 'pnpm:verify')
})

test('fails deterministically when neither pnpm nor Corepack is installed', { skip: process.platform === 'win32' }, () => {
  const { result, output } = probe({ withCorepack: false })
  assert.equal(result.status, 1)
  assert.equal(output, null)
  assert.match(result.stderr, /required tool is not installed: pnpm or Corepack/)
})

test('exports the pinned Corepack shim to nested package scripts', { skip: process.platform === 'win32' }, () => {
  const { result, output } = probe({ commonPath: backendCommon })
  assert.equal(result.status, 0, result.stderr)
  assert.equal(output, 'corepack-shim:verify')
})

test('rejects a directly installed pnpm with a drifted version', { skip: process.platform === 'win32' }, () => {
  const { result, output } = probe({ withPnpm: true, withCorepack: false, pnpmVersion: '11.2.0' })
  assert.equal(result.status, 1)
  assert.equal(output, null)
  assert.match(result.stderr, /pnpm 11\.1\.3 is required; found 11\.2\.0/)
})
