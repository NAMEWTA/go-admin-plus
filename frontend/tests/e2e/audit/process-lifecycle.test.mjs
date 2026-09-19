import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { cleanupTrackedChildren, didSpawnFail, spawnTracked, terminateChild, waitForExit } from './process-lifecycle.mjs'

test('waitForExit observes a graceful process exit', async () => {
  const child = spawn(process.execPath, ['-e', 'setTimeout(() => process.exit(0), 20)'], { stdio: 'ignore' })
  const result = await waitForExit(child, 2_000)
  assert.equal(result.exited, true)
  assert.equal(result.code, 0)
})

test('terminateChild never returns before the child exit event', async () => {
  const child = spawn(process.execPath, ['-e', 'process.on("SIGTERM", () => {}); console.log("ready"); setInterval(() => {}, 1000)'], { stdio: ['ignore', 'pipe', 'ignore'] })
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('child readiness timed out')), 2_000)
    child.stdout.once('data', () => { clearTimeout(timer); resolve() })
    child.once('exit', () => { clearTimeout(timer); reject(new Error('child exited before readiness')) })
  })
  const result = await terminateChild(child, 50)
  assert.equal(result.code !== null || result.signal !== null, true)
  assert.equal(child.exitCode !== null || child.signalCode !== null, true)
  if (process.platform !== 'win32') assert.equal(result.forced, true)
})

for (const role of ['host', 'browser']) {
  test(`tracked invalid ${role} executable is observed and cleanup completes`, async () => {
    const temporaryRoot = mkdtempSync(join(tmpdir(), `go-admin-audit-${role}-spawn-`))
    const activeChildren = new Set()
    try {
      const child = spawnTracked(`go-admin-definitely-missing-${role}`, [], { stdio: 'ignore' }, activeChildren)
      await once(child, 'error')
      assert.equal(didSpawnFail(child), true)
    } finally {
      await cleanupTrackedChildren(activeChildren, 100)
      rmSync(temporaryRoot, { recursive: true, force: true })
    }
    assert.equal(activeChildren.size, 0)
    assert.equal(existsSync(temporaryRoot), false)
  })
}
