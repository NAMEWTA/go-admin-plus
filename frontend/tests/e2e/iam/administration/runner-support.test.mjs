import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { test } from 'node:test'
import { CDPClient, spawnTracked, terminateChild } from './runner-support.mjs'

class FakeSocket extends EventTarget {
  sent = []
  send(value) { this.sent.push(JSON.parse(value)) }
  message(value) { this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(value) })) }
  close() { this.dispatchEvent(new Event('close')) }
}

test('CDP client resolves commands and rejects every pending command on close', async () => {
  const socket = new FakeSocket(); const client = new CDPClient(socket, 1_000)
  const first = client.send('Runtime.enable')
  socket.message({ id: socket.sent[0].id, result: { ready: true } })
  assert.deepEqual(await first, { ready: true })
  const pending = client.send('Runtime.evaluate')
  socket.close()
  await assert.rejects(pending, /CDP connection closed/)
  assert.equal(client.pending.size, 0)
})

test('tracked child termination is bounded and leaves no live process', async () => {
  const child = spawnTracked(spawn, process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' })
  await once(child, 'spawn')
  const result = await terminateChild(child, undefined, 50)
  assert.equal(result.exited, true)
  assert.equal(child.exitCode !== null || child.signalCode !== null, true)
})

test('termination recognizes pre-exited, pre-signaled and spawn-failed children', async () => {
  const exited = spawnTracked(spawn, process.execPath, ['-e', ''], { stdio: 'ignore' })
  await once(exited, 'close')
  assert.equal((await terminateChild(exited, undefined, 20)).preExited, true)

  const signaled = spawnTracked(spawn, process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' })
  await once(signaled, 'spawn'); signaled.kill('SIGTERM'); await once(signaled, 'close')
  assert.equal((await terminateChild(signaled, undefined, 20)).preExited, true)

  const failed = spawnTracked(spawn, 'go-admin-command-that-does-not-exist', [], { stdio: 'ignore' })
  await once(failed, 'error')
  const failedResult = await terminateChild(failed, undefined, 20)
  assert.equal(failedResult.preExited, true)
  assert.equal(failedResult.shutdownFailed, true)
})
