import assert from 'node:assert/strict'
import path from 'node:path'
import test from 'node:test'

import { buildEnvironment, hostTriple, outputName, parseBuildRequest, parseTargets, targets } from './build.mjs'

test('maps supported Tauri target triples to exact external binary names', () => {
  assert.deepEqual(parseTargets(['--all']), Object.keys(targets))
  assert.equal(outputName('aarch64-apple-darwin'), 'go-admin-sidecar-aarch64-apple-darwin')
  assert.equal(outputName('x86_64-apple-darwin'), 'go-admin-sidecar-x86_64-apple-darwin')
  assert.equal(outputName('x86_64-pc-windows-msvc'), 'go-admin-sidecar-x86_64-pc-windows-msvc.exe')
  assert.equal(hostTriple('darwin', 'arm64'), 'aarch64-apple-darwin')
  assert.equal(hostTriple('darwin', 'x64'), 'x86_64-apple-darwin')
  assert.equal(hostTriple('win32', 'x64'), 'x86_64-pc-windows-msvc')
  assert.deepEqual(parseTargets(['--host']), [hostTriple()])
  assert.deepEqual(parseBuildRequest(['--native-e2e', '--target', 'aarch64-apple-darwin']), {
    selected: ['aarch64-apple-darwin'], nativeE2E: true
  })
})

test('uses a deterministic secret-free Go build environment', () => {
  const environment = buildEnvironment(
    targets['aarch64-apple-darwin'], '/private/tmp/build', '/opt/go/bin/go', '/opt/go-mod', path.posix
  )
  assert.equal(environment.GOWORK, 'off')
  assert.equal(environment.GOPROXY, 'off')
  assert.equal(environment.HOME, '/private/tmp/build/home')
  assert.equal(environment.PATH, '/opt/go/bin')
  assert.equal(environment.GOMODCACHE, '/opt/go-mod')
  assert.equal(Object.hasOwn(environment, 'SSH_AUTH_SOCK'), false)
  assert.equal(Object.hasOwn(environment, 'GOPRIVATE'), false)
  assert.equal(Object.hasOwn(environment, 'GOAUTH'), false)

  const windows = buildEnvironment(
    targets['x86_64-pc-windows-msvc'], 'C:\\build', 'C:\\Go\\bin\\go.exe', 'C:\\go-mod', path.win32
  )
  assert.equal(windows.HOME, 'C:\\build\\home')
  assert.equal(windows.PATH, 'C:\\Go\\bin')

  const crossCompiledWindows = buildEnvironment(
    targets['x86_64-pc-windows-msvc'], '/build', '/usr/local/go/bin/go', '/go-mod', path.posix
  )
  assert.equal(crossCompiledWindows.HOME, '/build/home')
  assert.equal(crossCompiledWindows.PATH, '/usr/local/go/bin')
})

test('rejects arbitrary targets and extra arguments', () => {
  assert.throws(() => parseTargets(['--target', '../../escape']))
  assert.throws(() => parseTargets(['--target', 'aarch64-unknown-linux-gnu']))
  assert.throws(() => parseTargets(['--all', '--target', 'aarch64-apple-darwin']))
  assert.throws(() => parseBuildRequest(['--native-e2e', '--all']))
  assert.equal(hostTriple('linux', 'x64'), 'x86_64-unknown-linux-gnu')
})
