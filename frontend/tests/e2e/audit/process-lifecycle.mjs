import { spawn } from 'node:child_process'

const hasExited = (child) => child.exitCode !== null || child.signalCode !== null
const spawnFailures = new WeakSet()

export const spawnTracked = (command, args, options, activeChildren) => {
  let child
  try {
    child = spawn(command, args, options)
  } catch {
    throw new Error('child process spawn failed')
  }
  activeChildren.add(child)
  child.once('error', () => {
    spawnFailures.add(child)
    activeChildren.delete(child)
  })
  child.once('exit', () => activeChildren.delete(child))
  return child
}

export const didSpawnFail = (child) => spawnFailures.has(child)

export const waitForExit = (child, timeoutMilliseconds) => new Promise((resolve) => {
  if (hasExited(child)) {
    resolve({ exited: true, code: child.exitCode, signal: child.signalCode })
    return
  }
  const onExit = (code, signal) => {
    clearTimeout(timer)
    resolve({ exited: true, code, signal })
  }
  const timer = setTimeout(() => {
    child.removeListener('exit', onExit)
    resolve({ exited: false, code: null, signal: null })
  }, timeoutMilliseconds)
  child.once('exit', onExit)
})

export const terminateChild = async (child, timeoutMilliseconds = 5_000) => {
  if (didSpawnFail(child)) return { code: null, signal: null, forced: false }
  if (hasExited(child)) return { code: child.exitCode, signal: child.signalCode, forced: false }
  child.kill('SIGTERM')
  let result = await waitForExit(child, timeoutMilliseconds)
  if (result.exited) return { ...result, forced: false }
  child.kill('SIGKILL')
  result = await waitForExit(child, timeoutMilliseconds)
  if (!result.exited) throw new Error('child process did not exit after SIGKILL')
  return { ...result, forced: true }
}

export const cleanupTrackedChildren = async (activeChildren, timeoutMilliseconds = 5_000) => {
  let firstError
  for (const child of activeChildren) {
    if (hasExited(child) || didSpawnFail(child)) continue
    try { await terminateChild(child, timeoutMilliseconds) } catch (error) { firstError ??= error }
  }
  if (firstError) throw firstError
}
